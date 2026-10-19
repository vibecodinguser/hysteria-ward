package hycore

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// get прогоняет запрос через подставной ответ и возвращает код и тело.
func get(t *testing.T, handler http.Handler, method, target string, header http.Header) (*http.Response, string) {
	t.Helper()

	req := httptest.NewRequest(method, target, nil)
	for name, values := range header {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("чтение ответа: %v", err)
	}
	_ = resp.Body.Close()

	return resp, string(body)
}

// TestMasqueradeOffHasNoHandler — выключенный masquerade не подставляет ничего:
// ядро отвечает 404 само, как обычный сервер без такой страницы.
func TestMasqueradeOffHasNoHandler(t *testing.T) {
	for _, mode := range []MasqueradeMode{"", MasqueradeOff} {
		handler, err := masqueradeHandler(Masquerade{Mode: mode, File: "/нет-такого", Proxy: "https://example.com"})
		if err != nil {
			t.Fatalf("masquerade %q: %v", mode, err)
		}
		if handler != nil {
			t.Errorf("masquerade %q: получен обработчик, ожидался nil", mode)
		}
	}
}

// TestMasqueradeFileServesSite — включённый режим отдаёт свой сайт: посторонний
// запрос видит страницу, а не признак прокси.
func TestMasqueradeFileServesSite(t *testing.T) {
	site := t.TempDir()
	if err := os.WriteFile(filepath.Join(site, "index.html"), []byte("подставной сайт"), 0o600); err != nil {
		t.Fatalf("index.html: %v", err)
	}

	handler, err := masqueradeHandler(Masquerade{Mode: MasqueradeFile, File: site})
	if err != nil {
		t.Fatalf("masqueradeHandler() = %v", err)
	}

	resp, body := get(t, handler, http.MethodGet, "http://example.com/", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("главная: код %d, ожидался 200", resp.StatusCode)
	}
	if body != "подставной сайт" {
		t.Errorf("главная: тело %q, ожидался подставной сайт", body)
	}

	// Чего нет — того нет: обычный 404, а не рассказ о том, что это прокси.
	resp, _ = get(t, handler, http.MethodGet, "http://example.com/no-such-page", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("незнакомый путь: код %d, ожидался 404", resp.StatusCode)
	}
}

// TestMasqueradeFileHidesDirectoryListing — список каталога выдал бы, что за
// сайт лежит на диске. Такого быть не должно.
func TestMasqueradeFileHidesDirectoryListing(t *testing.T) {
	site := t.TempDir()
	if err := os.Mkdir(filepath.Join(site, "private"), 0o700); err != nil {
		t.Fatalf("private: %v", err)
	}
	if err := os.WriteFile(filepath.Join(site, "private", "secret.txt"), []byte("секрет"), 0o600); err != nil {
		t.Fatalf("secret.txt: %v", err)
	}

	handler, err := masqueradeHandler(Masquerade{Mode: MasqueradeFile, File: site})
	if err != nil {
		t.Fatalf("masqueradeHandler() = %v", err)
	}

	resp, body := get(t, handler, http.MethodGet, "http://example.com/private/", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("каталог без index.html: код %d, ожидался 404", resp.StatusCode)
	}
	if strings.Contains(body, "secret.txt") {
		t.Errorf("ответ перечисляет содержимое каталога: %q", body)
	}
}

// TestMasqueradeFileStaysInsideRoot — «..» в запросе не выводит за каталог сайта.
func TestMasqueradeFileStaysInsideRoot(t *testing.T) {
	parent := t.TempDir()
	site := filepath.Join(parent, "site")
	if err := os.Mkdir(site, 0o700); err != nil {
		t.Fatalf("site: %v", err)
	}
	if err := os.WriteFile(filepath.Join(site, "index.html"), []byte("сайт"), 0o600); err != nil {
		t.Fatalf("index.html: %v", err)
	}
	if err := os.WriteFile(filepath.Join(parent, "outside.txt"), []byte("снаружи"), 0o600); err != nil {
		t.Fatalf("outside.txt: %v", err)
	}

	handler, err := masqueradeHandler(Masquerade{Mode: MasqueradeFile, File: site})
	if err != nil {
		t.Fatalf("masqueradeHandler() = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.URL.Path = "/../outside.txt"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("выход за каталог: код %d, ожидался 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "снаружи") {
		t.Error("из-за пределов каталога отдан файл")
	}
}

// TestMasqueradeFileRejectsOtherMethods — подставной сайт отвечает как обычный
// статический сервер: POST ему нечего делать.
func TestMasqueradeFileRejectsOtherMethods(t *testing.T) {
	site := t.TempDir()
	if err := os.WriteFile(filepath.Join(site, "index.html"), []byte("сайт"), 0o600); err != nil {
		t.Fatalf("index.html: %v", err)
	}

	handler, err := masqueradeHandler(Masquerade{Mode: MasqueradeFile, File: site})
	if err != nil {
		t.Fatalf("masqueradeHandler() = %v", err)
	}

	resp, _ := get(t, handler, http.MethodPost, "http://example.com/auth", nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST: код %d, ожидался 405", resp.StatusCode)
	}
}

// TestMasqueradeProxyMirrorsAndHidesProtocol — зеркало отдаёт чужой сайт, не
// пересылает ему заголовки Hysteria и подставляет его Host. Иначе пароль
// промахнувшегося клиента ушёл бы на чужой сервер, а ответ выдал бы прокси.
func TestMasqueradeProxyMirrorsAndHidesProtocol(t *testing.T) {
	var gotHost string
	var gotAuth string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotAuth = r.Header.Get("Hysteria-Auth")
		w.Header().Set("X-Mirror", "yes")
		_, _ = io.WriteString(w, "чужой сайт")
	}))
	defer upstream.Close()

	handler, err := masqueradeHandler(Masquerade{Mode: MasqueradeProxy, Proxy: upstream.URL})
	if err != nil {
		t.Fatalf("masqueradeHandler() = %v", err)
	}

	header := http.Header{}
	header.Set("Hysteria-Auth", "пароль-клиента")
	header.Set("Hysteria-CC-RX", "1000000")

	resp, body := get(t, handler, http.MethodGet, "http://panel.example.com/news", header)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("зеркало: код %d, ожидался 200", resp.StatusCode)
	}
	if body != "чужой сайт" {
		t.Errorf("зеркало: тело %q, ожидался чужой сайт", body)
	}
	if gotHost == "" || gotHost != strings.TrimPrefix(upstream.URL, "http://") {
		t.Errorf("зеркало получило Host %q, ожидался хост зеркала", gotHost)
	}
	if gotAuth != "" {
		t.Errorf("на зеркало ушёл заголовок авторизации %q", gotAuth)
	}
	if resp.Header.Get("X-Mirror") != "yes" {
		t.Error("ответ зеркала потерял заголовок чужого сайта")
	}
}

// TestMasqueradeValidate — негодный режим не должен доходить до ядра: опечатку в
// пути видно до остановки работающего сервера.
func TestMasqueradeValidate(t *testing.T) {
	site := t.TempDir()
	file := filepath.Join(site, "index.html")
	if err := os.WriteFile(file, []byte("сайт"), 0o600); err != nil {
		t.Fatalf("index.html: %v", err)
	}

	cases := map[string]Masquerade{
		"file без каталога":             {Mode: MasqueradeFile},
		"file с несуществующим":         {Mode: MasqueradeFile, File: filepath.Join(site, "нет")},
		"file с файлом вместо каталога": {Mode: MasqueradeFile, File: file},
		"proxy без адреса":              {Mode: MasqueradeProxy},
		"proxy без схемы":               {Mode: MasqueradeProxy, Proxy: "example.com"},
		"proxy с чужой схемой":          {Mode: MasqueradeProxy, Proxy: "ftp://example.com"},
		"неизвестный режим":             {Mode: "какой-то"},
	}

	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if err := m.Validate(); err == nil {
				t.Error("Validate() = nil, ожидалась ошибка")
			}
		})
	}

	for _, m := range []Masquerade{
		{Mode: MasqueradeOff},
		{},
		{Mode: MasqueradeFile, File: site},
		{Mode: MasqueradeProxy, Proxy: "https://example.com"},
	} {
		if err := m.Validate(); err != nil {
			t.Errorf("%+v: Validate() = %v, ожидалось отсутствие ошибки", m, err)
		}
	}
}
