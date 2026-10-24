package store_test

import (
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// TestPanelSettingsDefault — новая панель наружу не выставлена: снаружи её не
// видно, пока доступ не включат явным действием.
func TestPanelSettingsDefault(t *testing.T) {
	s := openStore(t)

	settings, err := s.PanelSettings()
	if err != nil {
		t.Fatalf("PanelSettings() = %v", err)
	}

	if settings.Access != store.AccessTunnel {
		t.Errorf("режим = %q, ожидался %q", settings.Access, store.AccessTunnel)
	}
	if settings.Port == 0 {
		t.Error("порт панели не задан")
	}
	if settings.Mode != store.PanelModeSubdomain {
		t.Errorf("режим домена = %q, ожидался %q", settings.Mode, store.PanelModeSubdomain)
	}
	if host := settings.Access.Host(); host != "127.0.0.1" {
		t.Errorf("петля слушает %q", host)
	}
}

// TestPanelSettingsRoundTrip — выбранный режим — это данные, а не состояние
// процесса: он лежит в базе.
func TestPanelSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hysteria-ward.db")

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	want := store.PanelSettings{
		Access: store.AccessPublic,
		Port:   9443,
		Mode:   store.PanelModeSubdomain,
	}
	if err := s.SavePanelSettings(want); err != nil {
		t.Fatalf("SavePanelSettings() = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer reopened.Close()

	got, err := reopened.PanelSettings()
	if err != nil {
		t.Fatalf("PanelSettings() = %v", err)
	}
	if got != want {
		t.Errorf("настройки = %+v, ожидались %+v", got, want)
	}
	if addr := got.Addr(); addr != "0.0.0.0:9443" {
		t.Errorf("адрес = %q, ожидался %q", addr, "0.0.0.0:9443")
	}
}

// TestPanelSettingsRejectsNonsense — испорченные настройки не превращаются молча
// в умолчание: панель, выставленная наружу недосмотром, — это дыра.
func TestPanelSettingsRejectsNonsense(t *testing.T) {
	for _, c := range []struct {
		name     string
		settings store.PanelSettings
	}{
		{"неизвестный режим", store.PanelSettings{Access: "снаружи", Port: 8443}},
		{"нулевой порт", store.PanelSettings{Access: store.AccessTunnel, Port: 0}},
		{"порт вне границ", store.PanelSettings{Access: store.AccessTunnel, Port: 70000}},
		{"режим доступа не задан", store.PanelSettings{Port: 8443}},
		{"неизвестный режим панели", store.PanelSettings{Access: store.AccessTunnel, Port: 8443, Mode: "wildcard"}},
		{"server с доменом", store.PanelSettings{
			Access: store.AccessTunnel,
			Port:   8443,
			Mode:   store.PanelModeServer,
			Domain: "panel.example.com",
		}},
		{"домен панели — IP", store.PanelSettings{
			Access: store.AccessTunnel,
			Port:   8443,
			Mode:   store.PanelModeSubdomain,
			Domain: "203.0.113.5",
		}},
		{"домен панели без точки", store.PanelSettings{
			Access: store.AccessTunnel,
			Port:   8443,
			Mode:   store.PanelModeSubdomain,
			Domain: "panel",
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := openStore(t).SavePanelSettings(c.settings); err == nil {
				t.Errorf("SavePanelSettings(%+v) прошли, ожидался отказ", c.settings)
			}
		})
	}
}

// TestPanelSettingsFillsMissing — незаданное поле означает базу, записанную
// прежней панелью, а не решение администратора.
func TestPanelSettingsFillsMissing(t *testing.T) {
	for _, c := range []struct {
		name string
		raw  string
		want store.PanelSettings
	}{
		{"только порт", `{"port":9443}`, store.PanelSettings{Access: store.AccessTunnel, Port: 9443}},
		{"только режим", `{"access":"public"}`, store.PanelSettings{Access: store.AccessPublic, Port: 8443}},
		{"пустой порт", `{"access":"public","port":0}`, store.PanelSettings{Access: store.AccessPublic, Port: 8443}},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hysteria-ward.db")

			s, err := store.Open(path)
			if err != nil {
				t.Fatalf("Open() = %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatalf("Close() = %v", err)
			}

			writePanelSettings(t, path, c.raw)

			reopened, err := store.Open(path)
			if err != nil {
				t.Fatalf("Open() = %v", err)
			}
			defer reopened.Close()

			got, err := reopened.PanelSettings()
			if err != nil {
				t.Fatalf("PanelSettings() = %v", err)
			}
			if got != c.want {
				t.Errorf("настройки = %+v, ожидались %+v", got, c.want)
			}
		})
	}
}

// TestPanelSettingsHost — имя панели зависит от режима: домен сервера, отдельный
// поддомен или прежнее panel.<домен>.
func TestPanelSettingsHost(t *testing.T) {
	tests := []struct {
		name      string
		settings  store.PanelSettings
		server    string
		wantHost  string
		wantHosts []string
	}{
		{
			name:      "subdomain по умолчанию",
			settings:  store.PanelSettings{Access: store.AccessTunnel, Port: 8443, Mode: store.PanelModeSubdomain},
			server:    "example.com",
			wantHost:  "panel.example.com",
			wantHosts: []string{"example.com", "panel.example.com"},
		},
		{
			name:      "server",
			settings:  store.PanelSettings{Access: store.AccessTunnel, Port: 8443, Mode: store.PanelModeServer},
			server:    "example.com",
			wantHost:  "example.com",
			wantHosts: []string{"example.com"},
		},
		{
			name:      "свой поддомен",
			settings:  store.PanelSettings{Access: store.AccessTunnel, Port: 8443, Mode: store.PanelModeSubdomain, Domain: "admin.example.com"},
			server:    "example.com",
			wantHost:  "admin.example.com",
			wantHosts: []string{"example.com", "admin.example.com"},
		},
		{
			name:      "старая запись",
			settings:  store.PanelSettings{Access: store.AccessTunnel, Port: 8443},
			server:    "example.com",
			wantHost:  "panel.example.com",
			wantHosts: []string{"example.com", "panel.example.com"},
		},
		{
			name:      "старая запись на panel.*",
			settings:  store.PanelSettings{Access: store.AccessTunnel, Port: 8443},
			server:    "panel.example.com",
			wantHost:  "panel.example.com",
			wantHosts: []string{"panel.example.com"},
		},
		{
			name:      "домен сервера не задан",
			settings:  store.PanelSettings{Access: store.AccessTunnel, Port: 8443, Mode: store.PanelModeSubdomain},
			server:    "",
			wantHost:  "",
			wantHosts: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.settings.Host(test.server); got != test.wantHost {
				t.Errorf("Host(%q) = %q, ожидался %q", test.server, got, test.wantHost)
			}
			got := test.settings.Hosts(test.server)
			if len(got) != len(test.wantHosts) {
				t.Fatalf("Hosts(%q) = %v, ожидалось %v", test.server, got, test.wantHosts)
			}
			for i := range got {
				if got[i] != test.wantHosts[i] {
					t.Errorf("Hosts(%q) = %v, ожидалось %v", test.server, got, test.wantHosts)
					break
				}
			}
		})
	}
}

// TestPanelSettingsValidateFor — режим проверяется вместе с доменом сервера:
// публичная панель не может делить с ним имя, а поддомен должен оставаться в том
// же базовом домене.
func TestPanelSettingsValidateFor(t *testing.T) {
	tests := []struct {
		name     string
		settings store.PanelSettings
		server   string
		wantErr  bool
	}{
		{
			name:     "server для туннеля",
			settings: store.PanelSettings{Access: store.AccessTunnel, Port: 8443, Mode: store.PanelModeServer},
			server:   "example.com",
		},
		{
			name:     "server для публичной панели",
			settings: store.PanelSettings{Access: store.AccessPublic, Port: 8443, Mode: store.PanelModeServer},
			server:   "example.com",
			wantErr:  true,
		},
		{
			name:     "subdomain для публичной панели",
			settings: store.PanelSettings{Access: store.AccessPublic, Port: 8443, Mode: store.PanelModeSubdomain},
			server:   "example.com",
		},
		{
			name:     "свой поддомен в том же базовом домене",
			settings: store.PanelSettings{Access: store.AccessTunnel, Port: 8443, Mode: store.PanelModeSubdomain, Domain: "admin.example.com"},
			server:   "example.com",
		},
		{
			name:     "поддомен в другом базовом домене",
			settings: store.PanelSettings{Access: store.AccessTunnel, Port: 8443, Mode: store.PanelModeSubdomain, Domain: "admin.example.net"},
			server:   "example.com",
			wantErr:  true,
		},
		{
			name:     "поддомен совпал с доменом сервера",
			settings: store.PanelSettings{Access: store.AccessTunnel, Port: 8443, Mode: store.PanelModeSubdomain, Domain: "example.com"},
			server:   "example.com",
			wantErr:  true,
		},
		{
			name:     "сложный публичный суффикс",
			settings: store.PanelSettings{Access: store.AccessTunnel, Port: 8443, Mode: store.PanelModeSubdomain, Domain: "panel.example.co.uk"},
			server:   "example.co.uk",
		},
		{
			name:     "разные сложные публичные суффиксы",
			settings: store.PanelSettings{Access: store.AccessTunnel, Port: 8443, Mode: store.PanelModeSubdomain, Domain: "panel.example.com"},
			server:   "example.co.uk",
			wantErr:  true,
		},
		{
			name:     "старая запись для публичной панели",
			settings: store.PanelSettings{Access: store.AccessPublic, Port: 8443},
			server:   "example.com",
		},
		{
			name:     "старая запись на panel.*",
			settings: store.PanelSettings{Access: store.AccessPublic, Port: 8443},
			server:   "panel.example.com",
		},
		{
			name:     "старая запись на IP",
			settings: store.PanelSettings{Access: store.AccessPublic, Port: 8443},
			server:   "203.0.113.5",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.settings.ValidateFor(test.server)
			if test.wantErr && err == nil {
				t.Fatalf("ValidateFor(%q) = nil, ожидалась ошибка", test.server)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("ValidateFor(%q) = %v", test.server, err)
			}
		})
	}
}

// TestPanelSettingsServerDomainChange — при смене домена сервера режим server
// следует за ним, автоматический поддомен пересчитывается, а явный чужой
// поддомен отвергается.
func TestPanelSettingsServerDomainChange(t *testing.T) {
	serverMode := store.PanelSettings{Access: store.AccessTunnel, Port: 8443, Mode: store.PanelModeServer}
	if err := serverMode.ValidateFor("new.example.net"); err != nil {
		t.Fatalf("server для нового домена = %v", err)
	}
	if host := serverMode.Host("new.example.net"); host != "new.example.net" {
		t.Errorf("server следует за доменом: Host = %q", host)
	}

	auto := store.PanelSettings{Access: store.AccessTunnel, Port: 8443, Mode: store.PanelModeSubdomain}
	if err := auto.ValidateFor("new.example.net"); err != nil {
		t.Fatalf("автоматический поддомен для нового домена = %v", err)
	}
	if host := auto.Host("new.example.net"); host != "panel.new.example.net" {
		t.Errorf("автоматический поддомен пересчитан: Host = %q", host)
	}

	// Явный поддомен остаётся прежним, пока новый домен сервера делит с ним
	// базовый домен. Смена базового домена делает пару недопустимой.
	explicit := store.PanelSettings{
		Access: store.AccessTunnel,
		Port:   8443,
		Mode:   store.PanelModeSubdomain,
		Domain: "panel.old.example.com",
	}
	if err := explicit.ValidateFor("new.example.com"); err != nil {
		t.Fatalf("явный поддомен в том же базовом домене = %v", err)
	}
	if err := explicit.ValidateFor("new.example.net"); err == nil {
		t.Fatal("явный поддомен старого базового домена принят, ожидалась ошибка")
	}
}

// TestPanelSettingsLegacyPublicOnPanelDomain — старая база с доменом panel.* и
// публичной панелью продолжает открываться: до появления режимов это было
// штатное поведение, и обновление не должно его ломать.
func TestPanelSettingsLegacyPublicOnPanelDomain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hysteria-ward.db")

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	if err := s.SaveDomain("panel.example.com"); err != nil {
		t.Fatalf("SaveDomain() = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	writePanelSettings(t, path, `{"access":"public","port":8443}`)

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer reopened.Close()

	settings, err := reopened.PanelSettings()
	if err != nil {
		t.Fatalf("PanelSettings() = %v", err)
	}
	if settings.Access != store.AccessPublic {
		t.Errorf("доступ = %q, ожидался public", settings.Access)
	}
	if settings.Mode != "" {
		t.Errorf("режим = %q, ожидалась старая запись без режима", settings.Mode)
	}
	if host := settings.Host("panel.example.com"); host != "panel.example.com" {
		t.Errorf("Host = %q, ожидался panel.example.com", host)
	}
}

// TestPanelSettingsRejectsExplicitPublicServer — явный режим server с публичной
// панелью не проходит и при чтении: такую пару мог оставить только ручной
// редактор базы, и запускать её нельзя.
func TestPanelSettingsRejectsExplicitPublicServer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hysteria-ward.db")

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	if err := s.SaveDomain("example.com"); err != nil {
		t.Fatalf("SaveDomain() = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	writePanelSettings(t, path, `{"access":"public","port":8443,"mode":"server"}`)

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer reopened.Close()

	if _, err := reopened.PanelSettings(); err == nil {
		t.Fatal("PanelSettings() = nil, ожидалась ошибка для public + server")
	}
}

// writePanelSettings кладёт настройки панели в базу сырыми: так выглядит запись,
// оставленная прежней панелью, и другим способом её не подделать — свои настройки
// store принимает только годными.
func writePanelSettings(t *testing.T, path, raw string) {
	t.Helper()

	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatalf("открытие базы напрямую: %v", err)
	}
	defer db.Close()

	err = db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("appstate")).Put([]byte("panel"), []byte(raw))
	})
	if err != nil {
		t.Fatalf("запись настроек: %v", err)
	}
}
