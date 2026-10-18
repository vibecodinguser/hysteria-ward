package ui_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/ui"
)

// assetPattern вылавливает из точки входа ссылки на файлы сборки.
var assetPattern = regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`)

// response выполняет запрос к обработчику интерфейса.
func response(t *testing.T, method, target string) *http.Response {
	t.Helper()

	rec := httptest.NewRecorder()
	ui.Handler().ServeHTTP(rec, httptest.NewRequest(method, target, nil))

	return rec.Result()
}

// body читает тело ответа.
func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("тело ответа не прочитано: %v", err)
	}

	return string(content)
}

// TestServesEntryPoint — корень отдаёт точку входа приложения, и её нужно
// перепроверять при каждом заходе: имя файла не меняется от сборки к сборке.
func TestServesEntryPoint(t *testing.T) {
	resp := response(t, http.MethodGet, "/")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d, ожидалось 200", resp.StatusCode)
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Errorf("Content-Type = %q, ожидался HTML", contentType)
	}
	if cache := resp.Header.Get("Cache-Control"); cache != "no-cache" {
		t.Errorf("Cache-Control = %q, ожидалось \"no-cache\"", cache)
	}
	if content := body(t, resp); !strings.Contains(content, `id="app"`) {
		t.Errorf("в точке входа нет места для приложения: %q", content)
	}
}

// TestServesBuiltAssets — скрипты и стили лежат во вшитой сборке, а не рядом с
// бинарём: панель обязана работать на сервере без единого внешнего файла.
func TestServesBuiltAssets(t *testing.T) {
	entry := body(t, response(t, http.MethodGet, "/"))

	matches := assetPattern.FindAllStringSubmatch(entry, -1)
	if len(matches) == 0 {
		t.Fatalf("в точке входа нет ссылок на файлы сборки: %q", entry)
	}

	for _, match := range matches {
		url := match[1]

		resp := response(t, http.MethodGet, url)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, ожидалось 200", url, resp.StatusCode)
			resp.Body.Close()
			continue
		}

		// Содержимое файла неизменно, поэтому его можно не перепроверять.
		if cache := resp.Header.Get("Cache-Control"); cache != "public, max-age=31536000, immutable" {
			t.Errorf("GET %s: Cache-Control = %q, ожидалось вечное кэширование", url, cache)
		}

		contentType := resp.Header.Get("Content-Type")
		if strings.HasSuffix(url, ".js") && !strings.Contains(contentType, "javascript") {
			// Модуль с чужим типом содержимого браузер откажется исполнять.
			t.Errorf("GET %s: Content-Type = %q, ожидался JavaScript", url, contentType)
		}

		if content := body(t, resp); strings.TrimSpace(content) == "" {
			t.Errorf("GET %s: файл пуст", url)
		}
	}
}

// TestServesEntryPointForUnknownPath — незнакомый путь получает точку входа:
// маршруты знает клиент, и переход по прямой ссылке должен работать.
func TestServesEntryPointForUnknownPath(t *testing.T) {
	entry := body(t, response(t, http.MethodGet, "/"))

	for _, target := range []string{"/users/42", "/settings", "/чего-то-нет"} {
		resp := response(t, http.MethodGet, target)

		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, ожидалось 200", target, resp.StatusCode)
			resp.Body.Close()
			continue
		}

		if content := body(t, resp); content != entry {
			t.Errorf("GET %s: отдано не то же, что корень", target)
		}
	}
}

// TestUnknownAssetIsNotFound — пропавший файл сборки должен быть честной ошибкой:
// отдав вместо скрипта разметку, панель сломалась бы непонятно.
func TestUnknownAssetIsNotFound(t *testing.T) {
	resp := response(t, http.MethodGet, "/assets/нет-такого.js")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET пропавшего файла сборки = %d, ожидалось 404", resp.StatusCode)
	}
}

// TestDoesNotEscapeBuild — запрос за пределы вшитой сборки не должен ничего
// находить: интерфейс отдаёт только своё.
func TestDoesNotEscapeBuild(t *testing.T) {
	resp := response(t, http.MethodGet, "/../../../../etc/passwd")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET с «..» = %d, ожидалась точка входа", resp.StatusCode)
	}
	if content := body(t, resp); !strings.Contains(content, `id="app"`) {
		t.Errorf("GET с «..» отдал не точку входа: %q", content)
	}
}

// TestRejectsOtherMethods — интерфейс только забирают, поэтому запрос чужого
// метода должен получить отказ, а не разметку.
func TestRejectsOtherMethods(t *testing.T) {
	resp := response(t, http.MethodPost, "/")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, ожидалось 405", resp.StatusCode)
	}
	if allow := resp.Header.Get("Allow"); !strings.Contains(allow, http.MethodGet) {
		t.Errorf("Allow = %q, ожидался GET", allow)
	}
}
