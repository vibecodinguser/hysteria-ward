package store_test

import (
	"strings"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// TestACMESettingsDefault — новая база получает HTTP-01: он работает на любом
// VPS без доступа к DNS-провайдеру.
func TestACMESettingsDefault(t *testing.T) {
	s := openStore(t)

	settings, err := s.ACMESettings()
	if err != nil {
		t.Fatalf("ACMESettings() = %v", err)
	}
	if settings.Mode != "http" {
		t.Errorf("режим = %q, ожидался http", settings.Mode)
	}
	if settings.Directory != "" {
		t.Errorf("каталог = %q, ожидался пустой (Let's Encrypt)", settings.Directory)
	}
}

// TestSaveACMEEmail — адрес сохраняется нормализованным и читается обратно.
func TestSaveACMEEmail(t *testing.T) {
	s := openStore(t)

	if err := s.SaveACMEEmail(" admin@example.com "); err != nil {
		t.Fatalf("SaveACMEEmail() = %v", err)
	}

	email, err := s.ACMEEmail()
	if err != nil {
		t.Fatalf("ACMEEmail() = %v", err)
	}
	if email != "admin@example.com" {
		t.Errorf("e-mail = %q, ожидался admin@example.com", email)
	}

	if err := s.SaveACMEEmail("admin@localhost"); err == nil {
		t.Error("SaveACMEEmail() принял адрес без домена")
	}
}

// TestSaveACMESettings — настройки выпуска читаются обратно теми же методами,
// которыми пользуется панель.
func TestSaveACMESettings(t *testing.T) {
	s := openStore(t)

	want := store.ACMESettings{
		Mode:        "dns",
		Directory:   "https://acme-staging.example/dir",
		DNSProvider: "hook",
		DNSHook:     "/usr/local/bin/dns-hook",
	}
	if err := s.SaveACMESettings(want); err != nil {
		t.Fatalf("SaveACMESettings() = %v", err)
	}

	got, err := s.ACMESettings()
	if err != nil {
		t.Fatalf("ACMESettings() = %v", err)
	}
	if got != want {
		t.Errorf("настройки = %+v, ожидались %+v", got, want)
	}
}

// TestSaveCloudflareACMESettings — токен и зона сохраняются и читаются обратно;
// токен не теряется при нормализации старых настроек.
func TestSaveCloudflareACMESettings(t *testing.T) {
	s := openStore(t)

	want := store.ACMESettings{
		Mode:               "dns",
		DNSProvider:        "cloudflare",
		CloudflareAPIToken: "secret-token",
		CloudflareZoneID:   "zone-1",
	}
	if err := s.SaveACMESettings(want); err != nil {
		t.Fatalf("SaveACMESettings() = %v", err)
	}

	got, err := s.ACMESettings()
	if err != nil {
		t.Fatalf("ACMESettings() = %v", err)
	}
	if got != want {
		t.Errorf("настройки = %+v, ожидались %+v", got, want)
	}
}

// TestACMESettingsStringHidesToken — печать настроек не раскрывает секрет.
func TestACMESettingsStringHidesToken(t *testing.T) {
	settings := store.ACMESettings{
		Mode:               "dns",
		DNSProvider:        "cloudflare",
		CloudflareAPIToken: "secret-token",
	}

	if text := settings.String(); strings.Contains(text, "secret-token") {
		t.Errorf("в строке настроек оказался токен: %s", text)
	}
}

// TestLegacyDNSHookGetsProvider — база без поля dns_provider продолжает работать:
// пустой провайдер при наличии хука означает hook.
func TestLegacyDNSHookGetsProvider(t *testing.T) {
	s := openStore(t)

	if err := s.SaveACMESettings(store.ACMESettings{
		Mode:    "dns",
		DNSHook: "/usr/local/bin/dns-hook",
	}); err != nil {
		t.Fatalf("SaveACMESettings() = %v", err)
	}

	got, err := s.ACMESettings()
	if err != nil {
		t.Fatalf("ACMESettings() = %v", err)
	}
	if got.DNSProvider != "hook" {
		t.Errorf("провайдер = %q, ожидался hook", got.DNSProvider)
	}
}

// TestACMESettingsValidate — способ проверки и адрес каталога проверяются до
// записи: иначе панель узнала бы о них только при выпуске.
func TestACMESettingsValidate(t *testing.T) {
	tests := []struct {
		name     string
		settings store.ACMESettings
	}{
		{"неизвестный режим", store.ACMESettings{Mode: "tls-alpn"}},
		{"dns без хука", store.ACMESettings{Mode: "dns"}},
		{"http с хуком", store.ACMESettings{Mode: "http", DNSHook: "/hook"}},
		{"http с токеном", store.ACMESettings{Mode: "http", CloudflareAPIToken: "token"}},
		{"неизвестный провайдер", store.ACMESettings{Mode: "dns", DNSProvider: "route53"}},
		{"cloudflare без токена", store.ACMESettings{Mode: "dns", DNSProvider: "cloudflare"}},
		{"cloudflare с хуком", store.ACMESettings{
			Mode: "dns", DNSProvider: "cloudflare", CloudflareAPIToken: "token", DNSHook: "/hook",
		}},
		{"хук с токеном", store.ACMESettings{
			Mode: "dns", DNSProvider: "hook", DNSHook: "/hook", CloudflareAPIToken: "token",
		}},
		{"каталог без схемы", store.ACMESettings{Mode: "http", Directory: "acme.example/dir"}},
		{"каталог не http", store.ACMESettings{Mode: "http", Directory: "ftp://acme.example/dir"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.settings.Validate(); err == nil {
				t.Fatal("Validate() = nil, ожидалась ошибка")
			}
		})
	}
}

// TestCloudflareACMESettingsValid — рабочий набор с токеном не требует хука.
func TestCloudflareACMESettingsValid(t *testing.T) {
	settings := store.ACMESettings{
		Mode:               "dns",
		DNSProvider:        "cloudflare",
		CloudflareAPIToken: "token",
	}
	if err := settings.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

// TestInitialSetupStoresACME — ответы конфигуратора про ACME сохраняются вместе с
// остальными: наполовину записанная настройка выглядела бы как работающая.
func TestInitialSetupStoresACME(t *testing.T) {
	s := openStore(t)

	setup := initialSetup(t)
	setup.ACME = store.ACMESettings{
		Mode:        "dns",
		Directory:   "https://acme.example/dir",
		DNSProvider: "hook",
		DNSHook:     "/usr/local/bin/dns-hook",
	}

	if err := s.ApplyInitialSetup(setup); err != nil {
		t.Fatalf("ApplyInitialSetup() = %v", err)
	}

	settings, err := s.ACMESettings()
	if err != nil {
		t.Fatalf("ACMESettings() = %v", err)
	}
	if settings != setup.ACME {
		t.Errorf("настройки ACME = %+v, ожидались %+v", settings, setup.ACME)
	}
}

// TestInitialSetupDefaultsACME — старая форма ответов без ACME получает HTTP-01:
// установщик прежней версии не должен оставлять панель без выпуска.
func TestInitialSetupDefaultsACME(t *testing.T) {
	s := openStore(t)

	setup := initialSetup(t)
	if err := s.ApplyInitialSetup(setup); err != nil {
		t.Fatalf("ApplyInitialSetup() = %v", err)
	}

	settings, err := s.ACMESettings()
	if err != nil {
		t.Fatalf("ACMESettings() = %v", err)
	}
	if settings.Mode != "http" {
		t.Errorf("режим = %q, ожидался http", settings.Mode)
	}
}

// TestInitialSetupRejectsBadACME — негодные настройки ACME не записываются вместе
// с остальными ответами.
func TestInitialSetupRejectsBadACME(t *testing.T) {
	setup := initialSetup(t)
	setup.ACME = store.ACMESettings{Mode: "dns"}

	if _, err := store.ValidateInitialSetup(setup); err == nil {
		t.Fatal("ValidateInitialSetup() = nil, ожидалась ошибка")
	}
}
