package hycore

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// MasqueradeMode — что показывать постороннему запросу, пришедшему не по
// протоколу Hysteria.
type MasqueradeMode string

const (
	// MasqueradeOff — не отвечать подставным содержимым: ядро отвечает 404, как
	// обычный сервер без такой страницы.
	MasqueradeOff MasqueradeMode = "off"

	// MasqueradeFile — отдавать свой сайт из каталога.
	MasqueradeFile MasqueradeMode = "file"

	// MasqueradeProxy — служить зеркалом чужого сайта.
	MasqueradeProxy MasqueradeMode = "proxy"
)

// Masquerade — подставной ответ на посторонний запрос.
type Masquerade struct {
	Mode  MasqueradeMode
	File  string
	Proxy string
}

// Validate проверяет, что masquerade можно включить.
//
// В отличие от config, здесь проверяется ещё и диск: каталог сайта должен
// существовать. Вызывается до остановки работающего ядра, поэтому опечатка в
// пути не уносит с собой живой сервер.
func (m Masquerade) Validate() error {
	switch m.Mode {
	case "", MasqueradeOff:
		return nil
	case MasqueradeFile:
		if m.File == "" {
			return errors.New("masquerade: не задан каталог сайта")
		}

		info, err := os.Stat(m.File)
		if err != nil {
			return fmt.Errorf("masquerade: каталог сайта: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("masquerade: %q — не каталог", m.File)
		}

		return nil
	case MasqueradeProxy:
		_, err := proxyTarget(m.Proxy)

		return err
	default:
		return fmt.Errorf("masquerade: неизвестный режим %q", m.Mode)
	}
}

// masqueradeHandler собирает подставной ответ. nil означает выключенный
// masquerade: ядро в этом случае отвечает 404 само.
//
// Запросы к сайту и зеркалу получают только заголовки обычного посетителя:
// заголовки протокола Hysteria снимаются. Иначе пароль клиента, промахнувшегося
// мимо авторизации, ушёл бы на чужой сервер, а ответ выдал бы, что здесь прокси.
func masqueradeHandler(m Masquerade) (http.Handler, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}

	var next http.Handler

	switch m.Mode {
	case "", MasqueradeOff:
		return nil, nil
	case MasqueradeFile:
		next = newFileHandler(m.File)
	case MasqueradeProxy:
		target, err := proxyTarget(m.Proxy)
		if err != nil {
			return nil, err
		}

		next = newProxyHandler(target)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stripProtocolHeaders(r.Header)
		next.ServeHTTP(w, r)
	}), nil
}

// stripProtocolHeaders убирает заголовки Hysteria из запроса постороннего
// посетителя. Протокол использует их для авторизации и управления полосой, и
// подставному сайту они не нужны.
func stripProtocolHeaders(header http.Header) {
	const prefix = "Hysteria-"

	for name := range header {
		if len(name) >= len(prefix) && strings.EqualFold(name[:len(prefix)], prefix) {
			header.Del(name)
		}
	}
}

// newFileHandler отдаёт файлы из каталога и ничего больше.
//
// Списка каталогов нет: он выдал бы, что за сайт лежит на диске, и что сервер
// вообще не обычный веб-сервер. Выход за пределы каталога закрыт: путь
// очищается до абсолютного, поэтому «..» в запросе никуда не уводит.
func newFileHandler(dir string) http.Handler {
	root, err := filepath.Abs(dir)
	if err != nil {
		root = dir
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)

			return
		}

		file := filepath.Join(root, filepath.FromSlash(path.Clean("/"+r.URL.Path)))

		info, err := os.Stat(file)
		if err != nil {
			http.NotFound(w, r)

			return
		}

		if info.IsDir() {
			if !strings.HasSuffix(r.URL.Path, "/") {
				http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)

				return
			}

			file = filepath.Join(file, "index.html")
			if info, err = os.Stat(file); err != nil || info.IsDir() {
				http.NotFound(w, r)

				return
			}
		}

		http.ServeFile(w, r, file)
	})
}

// newProxyHandler собирает зеркало чужого сайта.
//
// Host подменяется на адрес зеркала: сайт отвечает по своему имени, а не по
// имени нашего сервера. Ошибка зеркала превращается в обычный 502: рассказывать
// посетителю, что здесь обратный прокси, незачем.
func newProxyHandler(target *url.URL) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			// SetURL подставляет и путь зеркала, и его Host: чужой сайт должен
			// видеть своё имя, а не имя нашего сервера.
			r.SetURL(target)
			r.SetXForwarded()
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
		},
	}

	return proxy
}

// proxyTarget разбирает адрес зеркала. Схема и хост обязательны: без них
// обратный прокси не знает, куда идти.
func proxyTarget(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, errors.New("masquerade: не задан адрес зеркала")
	}

	target, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("masquerade: адрес зеркала: %w", err)
	}
	if (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return nil, errors.New("masquerade: адрес зеркала должен быть вида http(s)://хост")
	}

	return target, nil
}
