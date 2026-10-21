package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/authn"
	"github.com/vibecodinguser/hysteria-ward/internal/cli"
	"github.com/vibecodinguser/hysteria-ward/internal/config"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// TestRunConfigCheck — проверка показывает настройки из базы и ничего не меняет.
func TestRunConfigCheck(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hysteria-ward.db")
	seedSettings(t, dbPath)

	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("ReadFile() = %v", err)
	}

	code, stdout, stderr := runConfig(t, "--db", dbPath)
	if code != 0 {
		t.Fatalf("Run(config) = %d, ожидалось 0; stderr: %q", code, stderr)
	}

	for _, want := range []string{"домен: example.com", "сервер: :443", "панель: tunnel, 8443", "ACME: http"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("в выводе нет %q: %q", want, stdout)
		}
	}

	after, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("ReadFile() = %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("проверка изменила базу")
	}
}

// TestRunConfigHidesCloudflareToken — проверка показывает провайдера, но не
// секрет: вывод команды может попасть в журнал или переписку.
func TestRunConfigHidesCloudflareToken(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hysteria-ward.db")
	seedSettings(t, dbPath)

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	if err := db.SaveACMESettings(store.ACMESettings{
		Mode:               "dns",
		DNSProvider:        "cloudflare",
		CloudflareAPIToken: "secret-token",
		CloudflareZoneID:   "zone-1",
	}); err != nil {
		t.Fatalf("SaveACMESettings() = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	code, stdout, stderr := runConfig(t, "--db", dbPath)
	if code != 0 {
		t.Fatalf("Run(config) = %d, ожидалось 0; stderr: %q", code, stderr)
	}
	if strings.Contains(stdout, "secret-token") || strings.Contains(stderr, "secret-token") {
		t.Errorf("токен попал в вывод: stdout %q, stderr %q", stdout, stderr)
	}
	if !strings.Contains(stdout, "Cloudflare") {
		t.Errorf("в выводе нет провайдера: %q", stdout)
	}
}

// TestRunConfigCheckReportsBadSettings — негодная конфигурация видна как ошибка,
// а не как «всё хорошо».
func TestRunConfigCheckReportsBadSettings(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hysteria-ward.db")
	seedSettings(t, dbPath)

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	bad := config.Default()
	bad.TLS = config.TLS{Cert: "/etc/cert.pem", Key: "/etc/key.pem"}
	bad.Listen = ""
	bad.Masquerade = config.Masquerade{Mode: "неизвестно"}
	if err := db.SaveConfig(bad); err != nil {
		t.Fatalf("SaveConfig() = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	code, _, stderr := runConfig(t, "--db", dbPath, "--check")
	if code == 0 {
		t.Fatal("Run(config --check) = 0, ожидался отказ")
	}
	if !strings.Contains(stderr, "masquerade") {
		t.Errorf("в stderr нет причины: %q", stderr)
	}
}

// TestRunConfigRepairFillsDefaults — починка дополняет недостающие поля
// умолчаниями и сохраняет их: следующая проверка видит уже полные настройки.
func TestRunConfigRepairFillsDefaults(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hysteria-ward.db")
	seedSettings(t, dbPath)

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	// База прежней версии: пути к сертификату ещё не записаны.
	if err := db.SaveConfig(config.Config{Listen: ":8443", Masquerade: config.Masquerade{Mode: config.MasqueradeOff}}); err != nil {
		t.Fatalf("SaveConfig() = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	code, stdout, stderr := runConfig(t, "--db", dbPath, "--repair")
	if code != 0 {
		t.Fatalf("Run(config --repair) = %d, ожидалось 0; stderr: %q", code, stderr)
	}
	if !strings.Contains(stdout, "настройки приведены в порядок") {
		t.Errorf("в выводе нет подтверждения: %q", stdout)
	}

	db, err = store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer db.Close()

	cfg, err := db.Config()
	if err != nil {
		t.Fatalf("Config() = %v", err)
	}
	if cfg.TLS.Cert != "/etc/hysteria-ward/certs/fullchain.pem" ||
		cfg.TLS.Key != "/etc/hysteria-ward/certs/privkey.pem" {
		t.Errorf("пути TLS = %+v, ожидались стандартные", cfg.TLS)
	}
	if cfg.Listen != ":8443" {
		t.Errorf("адрес = %q, ожидался прежний :8443", cfg.Listen)
	}
}

// seedSettings кладёт в базу годные настройки так, как это сделал бы установщик.
func seedSettings(t *testing.T, dbPath string) {
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

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer func() { _ = db.Close() }()

	if err := db.ApplyInitialSetup(store.InitialSetup{
		Domain:    "example.com",
		ACMEEmail: "admin@example.com",
		Config:    cfg,
		Panel:     store.DefaultPanelSettings(),
		Admin:     authn.Credential{Name: authn.DefaultName, Hash: hash},
	}); err != nil {
		t.Fatalf("ApplyInitialSetup() = %v", err)
	}
}

// runConfig запускает config и возвращает код, stdout и stderr.
func runConfig(t *testing.T, args ...string) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	code := cli.Run(context.Background(), append([]string{"config"}, args...),
		strings.NewReader(""), &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}
