package store_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/authn"
	"github.com/vibecodinguser/hysteria-ward/internal/config"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// initialSetup — годные ответы конфигуратора: домен, почта, сертификат, публичный
// доступ и masquerade своим сайтом.
func initialSetup(t *testing.T) store.InitialSetup {
	t.Helper()

	hash, err := authn.Hash("пароль администратора")
	if err != nil {
		t.Fatalf("Hash() = %v", err)
	}

	cfg := config.Default()
	cfg.TLS = config.TLS{
		Cert: "/etc/hysteria-ward/certs/fullchain.pem",
		Key:  "/etc/hysteria-ward/certs/privkey.pem",
	}
	cfg.Masquerade = config.Masquerade{Mode: config.MasqueradeFile, File: t.TempDir()}

	return store.InitialSetup{
		Domain:    "Example.COM",
		ACMEEmail: " admin@example.com ",
		Config:    cfg,
		Panel:     store.PanelSettings{Access: store.AccessPublic, Port: 8443},
		Admin:     authn.Credential{Name: authn.DefaultName, Hash: hash},
	}
}

// TestApplyInitialSetup — ответы конфигуратора сохраняются вместе и читаются
// обратно теми же методами, которыми пользуется панель.
func TestApplyInitialSetup(t *testing.T) {
	s := openStore(t)

	setup := initialSetup(t)
	if err := s.ApplyInitialSetup(setup); err != nil {
		t.Fatalf("ApplyInitialSetup() = %v", err)
	}

	domain, err := s.Domain()
	if err != nil {
		t.Fatalf("Domain() = %v", err)
	}
	if domain != "example.com" {
		t.Errorf("домен = %q, ожидался нормализованный example.com", domain)
	}

	email, err := s.ACMEEmail()
	if err != nil {
		t.Fatalf("ACMEEmail() = %v", err)
	}
	if email != "admin@example.com" {
		t.Errorf("e-mail = %q, ожидался admin@example.com", email)
	}

	cfg, err := s.Config()
	if err != nil {
		t.Fatalf("Config() = %v", err)
	}
	if !reflect.DeepEqual(cfg, setup.Config) {
		t.Errorf("конфигурация = %+v, ожидалась %+v", cfg, setup.Config)
	}

	panel, err := s.PanelSettings()
	if err != nil {
		t.Fatalf("PanelSettings() = %v", err)
	}
	if panel != setup.Panel {
		t.Errorf("настройки панели = %+v, ожидались %+v", panel, setup.Panel)
	}

	credential, ok, err := s.Credential()
	if err != nil {
		t.Fatalf("Credential() = %v", err)
	}
	if !ok {
		t.Fatal("пароль администратора не сохранён")
	}
	if credential.Name != authn.DefaultName {
		t.Errorf("имя администратора = %q, ожидалось %q", credential.Name, authn.DefaultName)
	}

	matched, err := authn.Verify(credential.Hash, "пароль администратора")
	if err != nil {
		t.Fatalf("Verify() = %v", err)
	}
	if !matched {
		t.Error("сохранённый пароль не подошёл")
	}
}

// TestApplyInitialSetupRefusesSecondTime — повторная настройка уже настроенной
// базы не переписывает домен, почту и пароль. Иначе случайный повторный запуск
// установщика с другими ответами унёс бы настройки работающей панели.
func TestApplyInitialSetupRefusesSecondTime(t *testing.T) {
	s := openStore(t)

	if err := s.ApplyInitialSetup(initialSetup(t)); err != nil {
		t.Fatalf("ApplyInitialSetup() = %v", err)
	}

	second := initialSetup(t)
	second.Domain = "other.example"
	second.ACMEEmail = "other@example.com"

	err := s.ApplyInitialSetup(second)
	if !errors.Is(err, store.ErrAlreadyConfigured) {
		t.Fatalf("ApplyInitialSetup() = %v, ожидалась ErrAlreadyConfigured", err)
	}

	domain, err := s.Domain()
	if err != nil {
		t.Fatalf("Domain() = %v", err)
	}
	if domain != "example.com" {
		t.Errorf("домен после отказа = %q, ожидался прежний example.com", domain)
	}

	email, err := s.ACMEEmail()
	if err != nil {
		t.Fatalf("ACMEEmail() = %v", err)
	}
	if email != "admin@example.com" {
		t.Errorf("e-mail после отказа = %q, ожидался прежний admin@example.com", email)
	}
}

// TestValidateInitialSetup — ответы проверяются до записи. Таблица держит рядом
// причину отказа и то, что в ответе сломано.
func TestValidateInitialSetup(t *testing.T) {
	tests := []struct {
		name   string
		change func(*store.InitialSetup)
		want   string
	}{
		{
			name:   "нет домена",
			change: func(s *store.InitialSetup) { s.Domain = " " },
			want:   "домен сервера не задан",
		},
		{
			name:   "домен со схемой",
			change: func(s *store.InitialSetup) { s.Domain = "https://example.com" },
			want:   "недопустимый домен",
		},
		{
			name:   "IP вместо домена",
			change: func(s *store.InitialSetup) { s.Domain = "192.0.2.1" },
			want:   "для ACME нужен домен",
		},
		{
			name:   "нет почты",
			change: func(s *store.InitialSetup) { s.ACMEEmail = "" },
			want:   "e-mail для ACME не задан",
		},
		{
			name:   "почта без домена",
			change: func(s *store.InitialSetup) { s.ACMEEmail = "admin@localhost" },
			want:   "недопустимый e-mail",
		},
		{
			name:   "нет сертификата",
			change: func(s *store.InitialSetup) { s.Config.TLS = config.TLS{} },
			want:   "tls",
		},
		{
			name:   "негодный порт панели",
			change: func(s *store.InitialSetup) { s.Panel.Port = 70000 },
			want:   "порт панели",
		},
		{
			name: "публичная панель на домене сервера",
			change: func(s *store.InitialSetup) {
				s.Panel.Access = store.AccessPublic
				s.Panel.Mode = store.PanelModeServer
			},
			want: "публичная панель",
		},
		{
			name: "server с отдельным доменом",
			change: func(s *store.InitialSetup) {
				s.Panel.Mode = store.PanelModeServer
				s.Panel.Domain = "panel.example.com"
			},
			want: "в режиме server",
		},
		{
			name: "поддомен в другом базовом домене",
			change: func(s *store.InitialSetup) {
				s.Panel.Mode = store.PanelModeSubdomain
				s.Panel.Domain = "panel.example.net"
			},
			want: "базовом домене",
		},
		{
			name: "поддомен совпал с доменом сервера",
			change: func(s *store.InitialSetup) {
				s.Panel.Mode = store.PanelModeSubdomain
				s.Panel.Domain = "example.com"
			},
			want: "должен отличаться",
		},
		{
			name: "неизвестный режим панели",
			change: func(s *store.InitialSetup) {
				s.Panel.Mode = "wildcard"
			},
			want: "режим домена панели",
		},
		{
			name:   "нет имени администратора",
			change: func(s *store.InitialSetup) { s.Admin.Name = "" },
			want:   "имя администратора",
		},
		{
			name:   "нет пароля администратора",
			change: func(s *store.InitialSetup) { s.Admin.Hash = "" },
			want:   "пароль администратора",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setup := initialSetup(t)
			test.change(&setup)

			if _, err := store.ValidateInitialSetup(setup); err == nil {
				t.Fatalf("ValidateInitialSetup() = nil, ожидалась ошибка %q", test.want)
			} else if !strings.Contains(err.Error(), test.want) {
				t.Errorf("ошибка = %q, ожидалась причина %q", err, test.want)
			}
		})
	}
}

// TestValidateInitialSetupPanelModes — начальная настройка принимает оба режима
// домена панели и отвергает недопустимую пару с доменом сервера.
func TestValidateInitialSetupPanelModes(t *testing.T) {
	tests := []struct {
		name     string
		access   store.Access
		mode     store.PanelMode
		domain   string
		wantHost string
		wantErr  bool
	}{
		{
			name:     "server для туннеля",
			access:   store.AccessTunnel,
			mode:     store.PanelModeServer,
			wantHost: "example.com",
		},
		{
			name:     "subdomain по умолчанию",
			access:   store.AccessTunnel,
			mode:     store.PanelModeSubdomain,
			wantHost: "panel.example.com",
		},
		{
			name:     "свой поддомен нормализуется",
			access:   store.AccessTunnel,
			mode:     store.PanelModeSubdomain,
			domain:   "Admin.Example.COM",
			wantHost: "admin.example.com",
		},
		{
			name:     "старая запись без режима",
			access:   store.AccessPublic,
			mode:     "",
			wantHost: "panel.example.com",
		},
		{
			name:    "публичная панель на домене сервера",
			access:  store.AccessPublic,
			mode:    store.PanelModeServer,
			wantErr: true,
		},
		{
			name:    "поддомен вне базового домена",
			access:  store.AccessTunnel,
			mode:    store.PanelModeSubdomain,
			domain:  "panel.example.net",
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setup := initialSetup(t)
			setup.Panel.Access = test.access
			setup.Panel.Mode = test.mode
			setup.Panel.Domain = test.domain

			normalized, err := store.ValidateInitialSetup(setup)
			if test.wantErr {
				if err == nil {
					t.Fatal("ValidateInitialSetup() = nil, ожидалась ошибка")
				}

				return
			}
			if err != nil {
				t.Fatalf("ValidateInitialSetup() = %v", err)
			}
			if host := normalized.Panel.Host(normalized.Domain); host != test.wantHost {
				t.Errorf("Host = %q, ожидался %q", host, test.wantHost)
			}
		})
	}
}

// TestValidateInitialSetupNormalizes — проверка возвращает ответы в том виде, в
// котором они будут сохранены: домен без заглавных букв, почта без пробелов.
func TestValidateInitialSetupNormalizes(t *testing.T) {
	setup := initialSetup(t)

	normalized, err := store.ValidateInitialSetup(setup)
	if err != nil {
		t.Fatalf("ValidateInitialSetup() = %v", err)
	}
	if normalized.Domain != "example.com" {
		t.Errorf("домен = %q, ожидался example.com", normalized.Domain)
	}
	if normalized.ACMEEmail != "admin@example.com" {
		t.Errorf("e-mail = %q, ожидался admin@example.com", normalized.ACMEEmail)
	}
}
