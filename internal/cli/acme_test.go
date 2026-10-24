package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/cli"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// TestRunACMEUpdatesCloudflare — существующая база переходит на встроенный
// провайдер, токен читается из файла и не попадает в вывод.
func TestRunACMEUpdatesCloudflare(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hysteria-ward.db")
	seedSettings(t, dbPath)

	token := "cloudflare-secret-token"
	tokenFile := filepath.Join(t.TempDir(), "cloudflare-token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}

	code, stdout, stderr := runACMECLI(t,
		"--db", dbPath,
		"--mode", "dns",
		"--provider", "cloudflare",
		"--token-file", tokenFile,
		"--zone-id", "zone-1",
	)
	if code != 0 {
		t.Fatalf("Run(acme) = %d, ожидалось 0; stderr: %q", code, stderr)
	}
	if strings.Contains(stdout, token) || strings.Contains(stderr, token) {
		t.Errorf("токен попал в вывод: stdout %q, stderr %q", stdout, stderr)
	}
	if !strings.Contains(stdout, "DNS-01, Cloudflare") {
		t.Errorf("в выводе нет провайдера: %q", stdout)
	}

	settings := readACME(t, dbPath)
	if settings.Mode != "dns" || settings.DNSProvider != "cloudflare" ||
		settings.CloudflareAPIToken != token || settings.CloudflareZoneID != "zone-1" {
		t.Errorf("настройки ACME = %+v, ожидался Cloudflare", settings)
	}
}

// TestRunACMEShowsSettings — без флагов команда только показывает настройки и
// говорит, задан ли токен, не раскрывая его.
func TestRunACMEShowsSettings(t *testing.T) {
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

	code, stdout, stderr := runACMECLI(t, "--db", dbPath)
	if code != 0 {
		t.Fatalf("Run(acme) = %d, ожидалось 0; stderr: %q", code, stderr)
	}
	for _, want := range []string{"Cloudflare", "zone-1", "токен Cloudflare: задан"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("в выводе нет %q: %q", want, stdout)
		}
	}
	if strings.Contains(stdout, "secret-token") {
		t.Errorf("токен попал в вывод: %q", stdout)
	}
}

// TestRunACMEBackToHTTP — возврат на HTTP-01 очищает DNS-поля: иначе проверка
// настроек не пропустила бы такой набор.
func TestRunACMEBackToHTTP(t *testing.T) {
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

	code, stdout, stderr := runACMECLI(t, "--db", dbPath, "--mode", "http")
	if code != 0 {
		t.Fatalf("Run(acme) = %d, ожидалось 0; stderr: %q", code, stderr)
	}
	if !strings.Contains(stdout, "HTTP-01") {
		t.Errorf("в выводе нет HTTP-01: %q", stdout)
	}

	settings := readACME(t, dbPath)
	if settings.Mode != "http" || settings.DNSProvider != "" || settings.DNSHook != "" ||
		settings.CloudflareAPIToken != "" || settings.CloudflareZoneID != "" {
		t.Errorf("настройки ACME = %+v, ожидался чистый HTTP-01", settings)
	}
}

// TestRunACMESwitchToHook — переход с Cloudflare на внешний хук убирает секрет
// Cloudflare из базы.
func TestRunACMESwitchToHook(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hysteria-ward.db")
	seedSettings(t, dbPath)
	hook := writeHook(t, t.TempDir())

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

	code, _, stderr := runACMECLI(t,
		"--db", dbPath,
		"--mode", "dns",
		"--provider", "hook",
		"--hook", hook,
	)
	if code != 0 {
		t.Fatalf("Run(acme) = %d, ожидалось 0; stderr: %q", code, stderr)
	}

	settings := readACME(t, dbPath)
	if settings.DNSProvider != "hook" || settings.DNSHook != hook ||
		settings.CloudflareAPIToken != "" || settings.CloudflareZoneID != "" {
		t.Errorf("настройки ACME = %+v, ожидался hook без Cloudflare", settings)
	}
}

// TestRunACMEInfersDNS — провайдер без --mode включает DNS-01: иначе флаг
// молча отвергся бы на базе с HTTP-01.
func TestRunACMEInfersDNS(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hysteria-ward.db")
	seedSettings(t, dbPath)

	tokenFile := filepath.Join(t.TempDir(), "cloudflare-token")
	if err := os.WriteFile(tokenFile, []byte("secret-token\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}

	code, _, stderr := runACMECLI(t,
		"--db", dbPath,
		"--provider", "cloudflare",
		"--token-file", tokenFile,
	)
	if code != 0 {
		t.Fatalf("Run(acme) = %d, ожидалось 0; stderr: %q", code, stderr)
	}

	settings := readACME(t, dbPath)
	if settings.Mode != "dns" || settings.DNSProvider != "cloudflare" {
		t.Errorf("настройки ACME = %+v, ожидался DNS-01 с Cloudflare", settings)
	}
}

// TestRunACMERejectsBadSettings — неполная или противоречивая конфигурация
// отклоняется до записи.
func TestRunACMERejectsBadSettings(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("token\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}
	notExecutable := filepath.Join(t.TempDir(), "hook.sh")
	if err := os.WriteFile(notExecutable, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}

	tests := []struct {
		name string
		args []string
	}{
		{"cloudflare без токена", []string{"--mode", "dns", "--provider", "cloudflare"}},
		{"неизвестный провайдер", []string{"--mode", "dns", "--provider", "route53"}},
		{"хук не исполняется", []string{"--mode", "dns", "--provider", "hook", "--hook", notExecutable}},
		{"http с токеном", []string{"--mode", "http", "--token-file", tokenFile}},
		{"cloudflare с хуком", []string{
			"--mode", "dns", "--provider", "cloudflare", "--token-file", tokenFile,
			"--hook", writeHook(t, t.TempDir()),
		}},
		{"хук с zone id", []string{
			"--mode", "dns", "--provider", "hook", "--hook", writeHook(t, t.TempDir()),
			"--zone-id", "zone-1",
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "hysteria-ward.db")
			seedSettings(t, dbPath)

			args := append([]string{"--db", dbPath}, test.args...)
			code, stdout, stderr := runACMECLI(t, args...)
			if code == 0 {
				t.Fatalf("Run(acme) = 0, ожидался отказ; stdout: %q", stdout)
			}
			if stderr == "" {
				t.Error("причина отказа не объяснена")
			}
		})
	}
}

// readACME читает настройки после команды и закрывает базу.
func readACME(t *testing.T, dbPath string) store.ACMESettings {
	t.Helper()

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer func() { _ = db.Close() }()

	settings, err := db.ACMESettings()
	if err != nil {
		t.Fatalf("ACMESettings() = %v", err)
	}

	return settings
}

// runACMECLI запускает команду acme и возвращает код, stdout и stderr.
func runACMECLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	code := cli.Run(context.Background(), append([]string{"acme"}, args...),
		strings.NewReader(""), &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}
