package api_test

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/url"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/api"
	"github.com/vibecodinguser/hysteria-ward/internal/certs"
	"github.com/vibecodinguser/hysteria-ward/internal/keys"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// certificateJSON — состояние сертификата, каким его отдаёт панель.
type certificateJSON struct {
	Ready       bool     `json:"ready"`
	Placeholder bool     `json:"placeholder"`
	Managed     bool     `json:"managed"`
	Mode        string   `json:"mode"`
	Names       []string `json:"names"`
	LastError   string   `json:"last_error"`
}

// certificateManager собирает держатель сертификата с настоящим (не временным)
// самоподписанным сертификатом: для проверки отчёта этого достаточно.
func certificateManager(t *testing.T) (*certs.Manager, []string) {
	t.Helper()

	names := []string{"example.com", "panel.example.com"}

	certificate, err := certs.SelfSigned(names...)
	if err != nil {
		t.Fatalf("SelfSigned() = %v", err)
	}

	manager := certs.NewManager("", "")
	manager.Set(certificate)

	return manager, names
}

// TestCertificateStatus — панель рассказывает, что с сертификатом: готов ли он,
// какие имена покрывает и каким способом выпускается.
func TestCertificateStatus(t *testing.T) {
	manager, names := certificateManager(t)

	opts := options(t)
	opts.Certificates = manager
	opts.Background = func(ctx context.Context) { <-ctx.Done() }

	s := start(t, opts)
	cookie, _ := enter(t, s, "admin", "admin")

	resp := do(t, s, http.MethodGet, "/api/certificate", "", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("состояние сертификата: код %d, ожидался 200", resp.StatusCode)
	}

	answer := decode[certificateJSON](t, resp)
	if !answer.Ready {
		t.Error("сертификат не помечен готовым")
	}
	if answer.Placeholder {
		t.Error("настоящий сертификат помечен временным")
	}
	if !answer.Managed {
		t.Error("панель не признаёт, что управляет сертификатом")
	}
	if answer.Mode != "http" {
		t.Errorf("способ проверки = %q, ожидался http", answer.Mode)
	}
	if len(answer.Names) != len(names) {
		t.Errorf("имена = %v, ожидались %v", answer.Names, names)
	}
}

// TestCertificateRetry — повтор после ошибки доступен кнопкой: команда не должна
// ждать следующей автоматической попытки.
func TestCertificateRetry(t *testing.T) {
	manager, _ := certificateManager(t)

	opts := options(t)
	opts.Certificates = manager
	opts.Background = func(ctx context.Context) { <-ctx.Done() }

	s := start(t, opts)
	cookie, csrf := enter(t, s, "admin", "admin")

	resp := do(t, s, http.MethodPost, "/api/certificate/issue", "", cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("повтор выпуска: код %d, ожидался 202", resp.StatusCode)
	}
}

// TestDomainChangeAsksForNewCertificate — новый домен обновляет имена, на
// которые выписывается сертификат: прежний покрывает не то, что нужно.
func TestDomainChangeAsksForNewCertificate(t *testing.T) {
	manager, _ := certificateManager(t)

	opts := options(t)
	opts.Certificates = manager
	opts.Background = func(ctx context.Context) { <-ctx.Done() }

	s := start(t, opts)
	cookie, csrf := enter(t, s, "admin", "admin")

	resp := do(t, s, http.MethodPut, "/api/server", `{"domain":"other.example"}`, cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("запись домена: код %d, ожидался 200", resp.StatusCode)
	}

	names := manager.DesiredNames()
	if len(names) != 2 || names[0] != "other.example" || names[1] != "panel.other.example" {
		t.Errorf("имена сертификата = %v, ожидались other.example и panel.other.example", names)
	}
}

// TestCertificateWithoutManagement — если панель не управляет сертификатом,
// попытка повторить выпуск объясняет это, а не делает вид, что начала работу.
func TestCertificateWithoutManagement(t *testing.T) {
	s := server(t)
	cookie, csrf := enter(t, s, "admin", "admin")

	resp := do(t, s, http.MethodGet, "/api/certificate", "", cookie, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("состояние без управления: код %d, ожидался 409", resp.StatusCode)
	}

	resp = do(t, s, http.MethodPost, "/api/certificate/issue", "", cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("повтор без управления: код %d, ожидался 409", resp.StatusCode)
	}
}

// TestKeyPinFollowsCertificate — ключ получает отпечаток текущего сертификата:
// после продления новый ключ не должен указывать на прежний.
func TestKeyPinFollowsCertificate(t *testing.T) {
	manager, _ := certificateManager(t)

	opts := options(t)
	opts.Certificates = manager
	opts.Background = func(ctx context.Context) { <-ctx.Done() }

	if err := opts.Store.SaveDomain("example.com"); err != nil {
		t.Fatalf("SaveDomain() = %v", err)
	}

	user, err := opts.Store.CreateUser(store.User{Name: "alice", Password: "пароль", Enabled: true})
	if err != nil {
		t.Fatalf("CreateUser() = %v", err)
	}

	s := start(t, opts)
	cookie, _ := enter(t, s, "admin", "admin")

	first := keyPin(t, s, cookie, user.ID)
	if want := pinOf(t, manager.Current()); first != want {
		t.Errorf("отпечаток ключа = %q, ожидался %q", first, want)
	}

	renewed, err := certs.SelfSigned("example.com", "panel.example.com")
	if err != nil {
		t.Fatalf("SelfSigned() = %v", err)
	}
	manager.Set(renewed)

	second := keyPin(t, s, cookie, user.ID)
	if want := pinOf(t, renewed); second != want {
		t.Errorf("отпечаток после продления = %q, ожидался %q", second, want)
	}
	if first == second {
		t.Error("отпечаток не изменился вместе с сертификатом")
	}
}

// keyPin спрашивает ключ пользователя и вытаскивает из него pinSHA256.
func keyPin(t *testing.T, s *api.Server, cookie *http.Cookie, id string) string {
	t.Helper()

	uri := userKey(t, s, cookie, id)

	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("ключ %q не разобран: %v", uri, err)
	}

	return parsed.Query().Get("pinSHA256")
}

// pinOf считает отпечаток так же, как его считает ключ.
func pinOf(t *testing.T, certificate tls.Certificate) string {
	t.Helper()

	pin, err := keys.Pin(certificate)
	if err != nil {
		t.Fatalf("Pin() = %v", err)
	}

	return pin
}
