package hycore_test

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apernet/quic-go/http3"

	"github.com/vibecodinguser/hysteria-ward/internal/hycore"
)

// h3Do отправляет обычный HTTP/3-запрос на QUIC-порт ядра: так выглядит
// посторонний посетитель, который ничего не знает про Hysteria.
func h3Do(t *testing.T, srv *hycore.Server, method, target, host string, headers map[string]string) (*http.Response, string) {
	t.Helper()

	transport := &http3.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	t.Cleanup(func() { _ = transport.Close() })

	req, err := http.NewRequest(method, "https://"+srv.Addr().String()+target, nil)
	if err != nil {
		t.Fatalf("запрос: %v", err)
	}
	if host != "" {
		req.Host = host
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HTTP/3-запрос %s %s: %v", method, target, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("тело ответа: %v", err)
	}

	return resp, string(body)
}

// TestMasqueradeOffAnswersNotFound — выключенный masquerade ничего не
// подставляет: посторонний запрос получает обычный 404.
func TestMasqueradeOffAnswersNotFound(t *testing.T) {
	srv := startServer(t, hycore.Config{Authenticator: fakeAuth{"secret": "alice"}})

	resp, body := h3Do(t, srv, http.MethodGet, "/", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("код %d, ожидался 404", resp.StatusCode)
	}
	if strings.Contains(body, "подставной") {
		t.Errorf("выключенный masquerade отдал подставной ответ: %q", body)
	}
}

// TestMasqueradeFileAnswersStrangers — включённый masquerade отдаёт свой сайт:
// посторонний запрос видит страницу, а не пустоту.
func TestMasqueradeFileAnswersStrangers(t *testing.T) {
	site := t.TempDir()
	if err := os.WriteFile(filepath.Join(site, "index.html"), []byte("подставной сайт"), 0o600); err != nil {
		t.Fatalf("index.html: %v", err)
	}

	srv := startServer(t, hycore.Config{
		Authenticator: fakeAuth{"secret": "alice"},
		Masquerade:    hycore.Masquerade{Mode: hycore.MasqueradeFile, File: site},
	})

	resp, body := h3Do(t, srv, http.MethodGet, "/", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("код %d, ожидался 200", resp.StatusCode)
	}
	if body != "подставной сайт" {
		t.Errorf("тело %q, ожидался подставной сайт", body)
	}
}

// TestMasqueradeHidesFailedAuth — промах авторизации неотличим от обычного
// запроса к зеркалу: тот же ответ, те же заголовки, и пароль клиента не уходит
// на чужой сайт.
func TestMasqueradeHidesFailedAuth(t *testing.T) {
	var leaks []string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Hysteria-Auth"); auth != "" {
			leaks = append(leaks, auth)
		}
		_, _ = io.WriteString(w, "чужой сайт")
	}))
	defer upstream.Close()

	srv := startServer(t, hycore.Config{
		Authenticator: fakeAuth{"secret": "alice"},
		Masquerade:    hycore.Masquerade{Mode: hycore.MasqueradeProxy, Proxy: upstream.URL},
	})

	// Обычный POST на тот же путь: так выглядит посетитель, который ничего не
	// знает про протокол.
	plain, plainBody := h3Do(t, srv, http.MethodPost, "/auth", "", nil)

	// А так — клиент Hysteria, который ошибся паролем.
	wrong, wrongBody := h3Do(t, srv, http.MethodPost, "/auth", "hysteria",
		map[string]string{"Hysteria-Auth": "неверный-пароль"})

	if wrong.StatusCode != plain.StatusCode || wrongBody != plainBody {
		t.Errorf("промах авторизации отличается от обычного запроса: %d %q против %d %q",
			wrong.StatusCode, wrongBody, plain.StatusCode, plainBody)
	}
	if wrong.StatusCode != http.StatusOK || wrongBody != "чужой сайт" {
		t.Errorf("промах авторизации: код %d, тело %q — ожидался подставной ответ зеркала",
			wrong.StatusCode, wrongBody)
	}
	if len(leaks) != 0 {
		t.Errorf("на зеркало ушли пароли клиентов: %q", leaks)
	}
}
