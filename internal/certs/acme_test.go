package certs

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/acme"
)

// TestACMEConfigValidate — настройки выпуска проверяются без сети: способ
// проверки, хук для DNS и адрес каталога.
func TestACMEConfigValidate(t *testing.T) {
	valid := ACMEConfig{
		Names:          []string{"example.com", "panel.example.com"},
		Email:          "admin@example.com",
		AccountKeyPath: "/tmp/account.key",
		CertPath:       "/tmp/fullchain.pem",
		KeyPath:        "/tmp/privkey.pem",
		Mode:           ModeHTTP,
	}

	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}

	tests := []struct {
		name   string
		change func(*ACMEConfig)
	}{
		{"нет имён", func(c *ACMEConfig) { c.Names = nil }},
		{"пустое имя", func(c *ACMEConfig) { c.Names = []string{" "} }},
		{"нет почты", func(c *ACMEConfig) { c.Email = "" }},
		{"нет ключа учётной записи", func(c *ACMEConfig) { c.AccountKeyPath = "" }},
		{"нет пути сертификата", func(c *ACMEConfig) { c.CertPath = "" }},
		{"неизвестный режим", func(c *ACMEConfig) { c.Mode = "tls-alpn" }},
		{"dns без хука", func(c *ACMEConfig) { c.Mode = ModeDNS }},
		{"http с хуком", func(c *ACMEConfig) { c.DNSHook = "/usr/local/bin/hook" }},
		{"http с токеном", func(c *ACMEConfig) { c.CloudflareAPIToken = "token" }},
		{"неизвестный провайдер", func(c *ACMEConfig) { c.Mode = ModeDNS; c.DNSProvider = "route53" }},
		{"cloudflare без токена", func(c *ACMEConfig) { c.Mode = ModeDNS; c.DNSProvider = DNSProviderCloudflare }},
		{"cloudflare с хуком", func(c *ACMEConfig) {
			c.Mode = ModeDNS
			c.DNSProvider = DNSProviderCloudflare
			c.CloudflareAPIToken = "token"
			c.DNSHook = "/usr/local/bin/hook"
		}},
		{"плохой каталог", func(c *ACMEConfig) { c.DirectoryURL = "example.com" }},
		{"каталог с путём", func(c *ACMEConfig) { c.DirectoryURL = "ftp://example.com/dir" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.change(&config)

			if err := config.Validate(); err == nil {
				t.Fatal("Validate() = nil, ожидалась ошибка")
			}
		})
	}
}

// TestACMEConfigCloudflare — встроенный провайдер не требует хука, а пустой
// провайдер для DNS-01 означает старый режим с хуком.
func TestACMEConfigCloudflare(t *testing.T) {
	config := ACMEConfig{
		Names:              []string{"example.com"},
		Email:              "admin@example.com",
		AccountKeyPath:     "/tmp/account.key",
		CertPath:           "/tmp/fullchain.pem",
		KeyPath:            "/tmp/privkey.pem",
		Mode:               ModeDNS,
		DNSProvider:        DNSProviderCloudflare,
		CloudflareAPIToken: "token",
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}

	legacy := config
	legacy.DNSProvider = ""
	legacy.CloudflareAPIToken = ""
	legacy.DNSHook = "/usr/local/bin/hook"
	if err := legacy.Validate(); err != nil {
		t.Fatalf("Validate() старого режима = %v", err)
	}
	if normalized := legacy.normalized(); normalized.DNSProvider != DNSProviderHook {
		t.Errorf("провайдер по умолчанию = %q, ожидался hook", normalized.DNSProvider)
	}
}

// TestACMEConfigNormalized — незаданное получает умолчания: HTTP-01, Let's
// Encrypt, порт 80.
func TestACMEConfigNormalized(t *testing.T) {
	config := ACMEConfig{}.normalized()

	if config.Mode != ModeHTTP {
		t.Errorf("режим = %q, ожидался http", config.Mode)
	}
	if config.DirectoryURL != acme.LetsEncryptURL {
		t.Errorf("каталог = %q, ожидался Let's Encrypt", config.DirectoryURL)
	}
	if config.HTTPAddr != ":80" {
		t.Errorf("адрес проверки = %q, ожидался :80", config.HTTPAddr)
	}
	if config.DNSWait != defaultDNSWait {
		t.Errorf("ожидание DNS = %v, ожидалось %v", config.DNSWait, defaultDNSWait)
	}
	if config.Logger == nil {
		t.Error("журнал не задан")
	}
}

// TestAccountKeyIsCreatedOnce — ключ учётной записи заводится один раз: с новым
// ключом ACME завёл бы вторую учётную запись и упёрся бы в её пределы.
func TestAccountKeyIsCreatedOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acme-account.key")

	first, err := accountKey(path)
	if err != nil {
		t.Fatalf("accountKey() = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() = %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("права ключа = %o, ожидались 600", mode)
	}

	second, err := accountKey(path)
	if err != nil {
		t.Fatalf("accountKey() повторно = %v", err)
	}

	if !first.PublicKey.Equal(&second.PublicKey) {
		t.Error("повторный вызов завёл новый ключ")
	}
}

// TestAccountKeyRejectsGarbage — испорченный файл ключа не должен молча
// заменяться новым: тогда панель потеряла бы учётную запись ACME.
func TestAccountKeyRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acme-account.key")
	if err := os.WriteFile(path, []byte("не ключ"), 0o600); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}

	if _, err := accountKey(path); err == nil {
		t.Fatal("accountKey() на мусоре = nil, ожидалась ошибка")
	}
}

// TestCovers — сертификат годится, только если выписан на все нужные имена:
// после смены домена прежний сертификат покрывает не то, что нужно.
func TestCovers(t *testing.T) {
	certificate, err := SelfSigned("example.com", "panel.example.com")
	if err != nil {
		t.Fatalf("SelfSigned() = %v", err)
	}

	if !covers(certificate, []string{"example.com"}) {
		t.Error("сертификат не покрывает своё имя")
	}
	if !covers(certificate, []string{"example.com", "panel.example.com"}) {
		t.Error("сертификат не покрывает оба имени")
	}
	if covers(certificate, []string{"other.example"}) {
		t.Error("сертификат считает покрытым чужое имя")
	}
	if covers(certificate, []string{"example.com", "other.example"}) {
		t.Error("сертификат считает покрытым набор с чужим именем")
	}
	if covers(tls.Certificate{}, []string{"example.com"}) {
		t.Error("пустой сертификат считает имена покрытыми")
	}
}

// TestPickChallenge — из предложенных проверок выбирается та, которую умеет
// панель, а не первая попавшаяся.
func TestPickChallenge(t *testing.T) {
	challenges := []*acme.Challenge{
		{Type: "tls-alpn-01", Token: "один"},
		{Type: "http-01", Token: "два"},
	}

	picked := pickChallenge(challenges, "http-01")
	if picked == nil || picked.Token != "два" {
		t.Fatalf("pickChallenge() = %+v, ожидалась проверка http-01", picked)
	}
	if pickChallenge(challenges, "dns-01") != nil {
		t.Error("pickChallenge() нашёл проверку, которой нет")
	}
}

// TestHTTPChallengerServesResponse — ответ проверки отдаётся по её пути, а
// остальное — 404: порт 80 не должен превращаться в открытый сервер.
func TestHTTPChallengerServesResponse(t *testing.T) {
	challenger := &httpChallenger{responses: map[string]string{
		"/.well-known/acme-challenge/token": "ответ",
	}}

	found := httptest.NewRecorder()
	challenger.ServeHTTP(found, httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/token", nil))

	if found.Code != http.StatusOK || found.Body.String() != "ответ" {
		t.Errorf("ответ проверки = %d %q, ожидались 200 и %q", found.Code, found.Body.String(), "ответ")
	}

	missing := httptest.NewRecorder()
	challenger.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/", nil))

	if missing.Code != http.StatusNotFound {
		t.Errorf("посторонний путь = %d, ожидался 404", missing.Code)
	}
}

// TestDNSHookGetsArguments — хук получает действие, имя и значение: по ним он
// заводит и убирает запись TXT.
func TestDNSHookGetsArguments(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "hook.log")
	hookPath := filepath.Join(dir, "hook.sh")

	script := "#!/bin/sh\nprintf '%s %s %s\\n' \"$1\" \"$2\" \"$3\" >> " + logPath + "\n"
	if err := os.WriteFile(hookPath, []byte(script), 0o700); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}

	ctx := context.Background()
	if err := runHook(ctx, hookPath, "present", "example.com", "значение"); err != nil {
		t.Fatalf("runHook(present) = %v", err)
	}
	if err := runHook(ctx, hookPath, "cleanup", "example.com", "значение"); err != nil {
		t.Fatalf("runHook(cleanup) = %v", err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile() = %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("хук вызван %d раз, ожидалось 2: %q", len(lines), raw)
	}
	if lines[0] != "present example.com значение" {
		t.Errorf("первый вызов = %q", lines[0])
	}
	if lines[1] != "cleanup example.com значение" {
		t.Errorf("второй вызов = %q", lines[1])
	}
}

// TestDNSHookReportsOutput — ошибка хука должна нести его вывод: иначе причина
// отказа выпуска теряется.
func TestDNSHookReportsOutput(t *testing.T) {
	dir := t.TempDir()
	hookPath := filepath.Join(dir, "hook.sh")

	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\necho 'провайдер отказал'\nexit 1\n"), 0o700); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}

	err := runHook(context.Background(), hookPath, "present", "example.com", "значение")
	if err == nil {
		t.Fatal("runHook() = nil, ожидалась ошибка")
	}
	if !strings.Contains(err.Error(), "провайдер отказал") {
		t.Errorf("в ошибке нет вывода хука: %v", err)
	}
}

// TestRunStopsOnInvalidConfig — негодные настройки не запускают цикл выпуска, а
// оставляют понятную причину.
func TestRunStopsOnInvalidConfig(t *testing.T) {
	manager := NewManager("", "")
	done := make(chan struct{})

	go func() {
		Run(context.Background(), manager, ACMEConfig{}, nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run() не завершился на негодных настройках")
	}

	if manager.Status().LastError == "" {
		t.Error("причина отказа не записана")
	}
}
