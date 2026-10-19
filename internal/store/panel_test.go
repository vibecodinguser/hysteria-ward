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

	want := store.PanelSettings{Access: store.AccessPublic, Port: 9443}
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
		{"режим не задан", store.PanelSettings{Port: 8443}},
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
