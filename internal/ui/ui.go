// Пакет ui отдаёт собранный интерфейс панели.
//
// Сборка вшита в бинарь: на сервере не появляется ни Node.js, ни файлов рядом с
// бинарём, ни внешнего веб-сервера.
package ui

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// built — собранный интерфейс. Каталог наполняет `make ui`.
//
//go:embed all:dist
var built embed.FS

const (
	// root — каталог сборки внутри вшитого дерева.
	root = "dist"

	// indexName — точка входа приложения.
	indexName = "index.html"

	// assetsDir — каталог с файлами сборки. Имена в нём содержат хеш содержимого.
	assetsDir = "assets/"
)

// Handler отдаёт интерфейс.
//
// Незнакомый путь получает точку входа: маршруты знает клиент, и сервер про них
// знать не должен. Исключение — файлы сборки: если такого файла нет, значит
// сборка и точка входа разъехались, и молча отдавать вместо скрипта HTML нельзя.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Интерфейс только забирают: ни отправлять формы, ни звать API через этот
		// обработчик нечего, и запрос не того метода лучше отвергнуть, чем отдать
		// ему разметку.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = indexName
		}

		if data, err := asset(name); err == nil {
			write(w, r, name, data)
			return
		}

		if strings.HasPrefix(name, assetsDir) {
			http.NotFound(w, r)
			return
		}

		data, err := asset(indexName)
		if err != nil {
			http.Error(w, "интерфейс не собран", http.StatusInternalServerError)
			return
		}

		write(w, r, indexName, data)
	})
}

// asset читает файл сборки. Пути с «..» сюда не проходят: их отвергает fs.
func asset(name string) ([]byte, error) {
	return fs.ReadFile(built, root+"/"+name)
}

// write отдаёт файл сборки с заголовками кэша.
func write(w http.ResponseWriter, r *http.Request, name string, data []byte) {
	w.Header().Set("Cache-Control", cacheControl(name))
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

// cacheControl: точка входа одна и та же независимо от содержимого, поэтому её
// нужно перепроверять при каждом заходе; имена файлов сборки содержат хеш
// содержимого, поэтому их можно кэшировать навсегда.
func cacheControl(name string) string {
	if strings.HasPrefix(name, assetsDir) {
		return "public, max-age=31536000, immutable"
	}

	return "no-cache"
}
