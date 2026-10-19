package store_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/config"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// TestConfigIsDefaultWhenNotSaved — новая панель не требует настройки, чтобы
// просто открыться: пока конфигурацию не сохраняли, отдаём значения по умолчанию.
func TestConfigIsDefaultWhenNotSaved(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "hysteria-ward.db"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer s.Close()

	got, err := s.Config()
	if err != nil {
		t.Fatalf("Config() = %v", err)
	}

	if !reflect.DeepEqual(got, config.Default()) {
		t.Errorf("Config() = %+v, ожидалась конфигурация по умолчанию", got)
	}
}

// TestSaveAndReadConfig — конфигурация переживает перезапуск панели и
// возвращается такой же: хранилище не должно ничего терять по дороге.
func TestSaveAndReadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hysteria-ward.db")

	want := config.Config{
		Listen: ":8443",
		TLS: config.TLS{
			Cert: "/etc/hysteria-ward/certs/panel.crt",
			Key:  "/etc/hysteria-ward/certs/panel.key",
		},
		Obfs:       config.Obfs{Password: "salamander"},
		Masquerade: config.Masquerade{Mode: config.MasqueradeProxy, Proxy: "https://example.com"},
	}

	first, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	if err := first.SaveConfig(want); err != nil {
		t.Fatalf("SaveConfig() = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	second, err := store.Open(path)
	if err != nil {
		t.Fatalf("повторный Open() = %v", err)
	}
	defer second.Close()

	got, err := second.Config()
	if err != nil {
		t.Fatalf("Config() = %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("Config() = %+v, ожидалось %+v", got, want)
	}
}

// TestConfigFillsMissingFields — конфигурация без новых полей (так выглядит база,
// записанная прежней версией панели) дополняется значениями по умолчанию.
func TestConfigFillsMissingFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hysteria-ward.db")

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer s.Close()

	old := config.Config{
		TLS: config.TLS{Cert: "/etc/cert.pem", Key: "/etc/key.pem"},
	}
	if err := s.SaveConfig(old); err != nil {
		t.Fatalf("SaveConfig() = %v", err)
	}

	got, err := s.Config()
	if err != nil {
		t.Fatalf("Config() = %v", err)
	}

	if got.Listen != config.Default().Listen {
		t.Errorf("Listen = %q, ожидался %q", got.Listen, config.Default().Listen)
	}
	if got.Masquerade != config.Default().Masquerade {
		t.Errorf("Masquerade = %+v, ожидался %+v", got.Masquerade, config.Default().Masquerade)
	}
}
