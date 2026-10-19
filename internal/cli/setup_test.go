package cli_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/authn"
	"github.com/vibecodinguser/hysteria-ward/internal/cli"
	"github.com/vibecodinguser/hysteria-ward/internal/config"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// TestRunSetupSavesAnswers — ответы конфигуратора попадают в базу теми же
// методами, которыми их читает панель: домен, почта, сервер, доступ и пароль.
func TestRunSetupSavesAnswers(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "hysteria-ward.db")

	code, stdout, stderr := runSetup(t, "пароль администратора\n",
		"--db", dbPath,
		"--domain", "Example.COM",
		"--email", "admin@example.com",
		"--access", "public",
		"--masquerade", "off",
		"--password-stdin",
	)
	if code != 0 {
		t.Fatalf("Run(setup) = %d, ожидалось 0; stderr: %q", code, stderr)
	}
	if !strings.Contains(stdout, "домен: example.com") {
		t.Errorf("в stdout нет нормализованного домена: %q", stdout)
	}
	if strings.Contains(stdout, "пароль администратора:") {
		t.Errorf("пароль попал в вывод: %q", stdout)
	}

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer s.Close()

	domain, err := s.Domain()
	if err != nil {
		t.Fatalf("Domain() = %v", err)
	}
	if domain != "example.com" {
		t.Errorf("домен = %q, ожидался example.com", domain)
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
	if cfg.TLS.Cert != "/etc/hysteria-ward/certs/fullchain.pem" ||
		cfg.TLS.Key != "/etc/hysteria-ward/certs/privkey.pem" {
		t.Errorf("пути сертификата = %+v, ожидались стандартные", cfg.TLS)
	}
	if cfg.Masquerade.Mode != config.MasqueradeOff {
		t.Errorf("masquerade = %q, ожидался off", cfg.Masquerade.Mode)
	}

	panel, err := s.PanelSettings()
	if err != nil {
		t.Fatalf("PanelSettings() = %v", err)
	}
	if panel.Access != store.AccessPublic || panel.Port != 8443 {
		t.Errorf("настройки панели = %+v, ожидался публичный доступ на 8443", panel)
	}

	stored, ok, err := s.Credential()
	if err != nil {
		t.Fatalf("Credential() = %v", err)
	}
	if !ok {
		t.Fatal("пароль администратора не сохранён")
	}
	if stored.Name != authn.DefaultName {
		t.Errorf("имя администратора = %q, ожидалось %q", stored.Name, authn.DefaultName)
	}
	matched, err := authn.Verify(stored.Hash, "пароль администратора")
	if err != nil {
		t.Fatalf("Verify() = %v", err)
	}
	if !matched {
		t.Error("заданный пароль не подошёл")
	}
}

// TestRunSetupGeneratesPassword — сгенерированный пароль печатается один раз и
// подходит к сохранённому хешу. Второй раз его взять неоткуда.
func TestRunSetupGeneratesPassword(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "hysteria-ward.db")

	code, stdout, stderr := runSetup(t, "",
		"--db", dbPath,
		"--domain", "example.com",
		"--email", "admin@example.com",
		"--access", "tunnel",
		"--masquerade", "off",
		"--generate-password",
	)
	if code != 0 {
		t.Fatalf("Run(setup) = %d, ожидалось 0; stderr: %q", code, stderr)
	}
	if count := strings.Count(stdout, "пароль администратора:"); count != 1 {
		t.Fatalf("пароль напечатан %d раз, ожидался один: %q", count, stdout)
	}

	var password string
	for _, line := range strings.Split(stdout, "\n") {
		if after, ok := strings.CutPrefix(line, "пароль администратора: "); ok {
			password = after
		}
	}
	if len(password) != 24 {
		t.Fatalf("длина сгенерированного пароля = %d, ожидалось 24: %q", len(password), password)
	}

	_, hash := credential(t, dbPath)
	matched, err := authn.Verify(hash, password)
	if err != nil {
		t.Fatalf("Verify() = %v", err)
	}
	if !matched {
		t.Error("напечатанный пароль не подошёл к сохранённому хешу")
	}
}

// TestRunSetupRefusesSecondTime — установщик не должен переписывать настройки
// работающей панели. Повторный setup говорит об этом и оставляет базу как была.
func TestRunSetupRefusesSecondTime(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hysteria-ward.db")

	code, _, stderr := runSetup(t, "первый пароль\n",
		"--db", dbPath,
		"--domain", "example.com",
		"--email", "admin@example.com",
		"--access", "tunnel",
		"--masquerade", "off",
		"--password-stdin",
	)
	if code != 0 {
		t.Fatalf("первый Run(setup) = %d, ожидалось 0; stderr: %q", code, stderr)
	}

	code, _, stderr = runSetup(t, "второй пароль\n",
		"--db", dbPath,
		"--domain", "other.example",
		"--email", "other@example.com",
		"--access", "public",
		"--masquerade", "off",
		"--password-stdin",
	)
	if code == 0 {
		t.Fatal("повторный Run(setup) = 0, ожидался отказ")
	}
	if !strings.Contains(stderr, "уже настроена") {
		t.Errorf("в stderr нет причины: %q", stderr)
	}

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer s.Close()

	domain, err := s.Domain()
	if err != nil {
		t.Fatalf("Domain() = %v", err)
	}
	if domain != "example.com" {
		t.Errorf("домен после отказа = %q, ожидался прежний example.com", domain)
	}
}

// TestRunSetupCheckLeavesNoDatabase — проверка ответов не создаёт базу: до
// подтверждения установка не должна оставлять следов.
func TestRunSetupCheckLeavesNoDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "hysteria-ward.db")

	code, stdout, stderr := runSetup(t, "",
		"--db", dbPath,
		"--domain", "example.com",
		"--email", "admin@example.com",
		"--access", "tunnel",
		"--masquerade", "off",
		"--check",
	)
	if code != 0 {
		t.Fatalf("Run(setup --check) = %d, ожидалось 0; stderr: %q", code, stderr)
	}
	if !strings.Contains(stdout, "проверка пройдена") {
		t.Errorf("в stdout нет подтверждения: %q", stdout)
	}

	if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("проверка создала базу: %v", err)
	}
}

// TestRunSetupCheckRejectsBadAnswers — проверка ловит опечатку до установки:
// почта без домена и каталог сайта, которого нет.
func TestRunSetupCheckRejectsBadAnswers(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "почта без домена",
			args: []string{"--email", "admin@localhost"},
		},
		{
			name: "нет каталога сайта",
			args: []string{"--masquerade", "file", "--masquerade-file", "/нет/такого/каталога"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := []string{
				"--db", filepath.Join(t.TempDir(), "hysteria-ward.db"),
				"--domain", "example.com",
				"--email", "admin@example.com",
				"--access", "tunnel",
				"--masquerade", "off",
				"--check",
			}
			args = append(args, test.args...)

			code, _, stderr := runSetup(t, "", args...)
			if code == 0 {
				t.Fatal("Run(setup --check) = 0, ожидался отказ")
			}
			if stderr == "" {
				t.Error("причина отказа не объяснена")
			}
		})
	}
}

// TestRunSetupStoresACME — способ проверки ACME сохраняется вместе с остальными
// ответами: панель выпускает сертификат по нему, а не по своему умолчанию.
func TestRunSetupStoresACME(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hysteria-ward.db")
	hook := writeHook(t, t.TempDir())

	code, stdout, stderr := runSetup(t, "",
		"--db", dbPath,
		"--domain", "example.com",
		"--email", "admin@example.com",
		"--access", "tunnel",
		"--masquerade", "off",
		"--acme-mode", "dns",
		"--acme-dns-hook", hook,
		"--acme-directory", "https://acme.example/dir",
		"--generate-password",
	)
	if code != 0 {
		t.Fatalf("Run(setup) = %d, ожидалось 0; stderr: %q", code, stderr)
	}
	if !strings.Contains(stdout, "DNS-01") {
		t.Errorf("в итоговом отчёте нет способа проверки: %q", stdout)
	}

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer s.Close()

	settings, err := s.ACMESettings()
	if err != nil {
		t.Fatalf("ACMESettings() = %v", err)
	}
	if settings.Mode != "dns" || settings.DNSHook != hook || settings.Directory != "https://acme.example/dir" {
		t.Errorf("настройки ACME = %+v, ожидались dns с хуком %q", settings, hook)
	}
}

// TestRunSetupRejectsBadACME — DNS без хука и неисполняемый хук отклоняются до
// записи: иначе панель осталась бы без сертификата уже после установки.
func TestRunSetupRejectsBadACME(t *testing.T) {
	notExecutable := filepath.Join(t.TempDir(), "hook.sh")
	if err := os.WriteFile(notExecutable, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}

	tests := []struct {
		name string
		args []string
	}{
		{"dns без хука", []string{"--acme-mode", "dns"}},
		{"хук не исполняется", []string{"--acme-mode", "dns", "--acme-dns-hook", notExecutable}},
		{"хук для http", []string{"--acme-dns-hook", writeHook(t, t.TempDir())}},
		{"неизвестный режим", []string{"--acme-mode", "tls-alpn"}},
		{"плохой каталог", []string{"--acme-directory", "acme.example/dir"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := []string{
				"--db", filepath.Join(t.TempDir(), "hysteria-ward.db"),
				"--domain", "example.com",
				"--email", "admin@example.com",
				"--access", "tunnel",
				"--masquerade", "off",
				"--check",
			}
			args = append(args, test.args...)

			code, _, stderr := runSetup(t, "", args...)
			if code == 0 {
				t.Fatal("Run(setup --check) = 0, ожидался отказ")
			}
			if stderr == "" {
				t.Error("причина отказа не объяснена")
			}
		})
	}
}

// writeHook заводит исполняемую программу-хук для проверок DNS-01.
func writeHook(t *testing.T, dir string) string {
	t.Helper()

	path := filepath.Join(dir, "dns-hook.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}

	return path
}

// TestRunSetupProxy — режим зеркала сохраняется вместе с адресом, а не только
// проверяется: иначе ядро получило бы proxy без цели.
func TestRunSetupProxy(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hysteria-ward.db")

	code, _, stderr := runSetup(t, "",
		"--db", dbPath,
		"--domain", "example.com",
		"--email", "admin@example.com",
		"--access", "tunnel",
		"--masquerade", "proxy",
		"--masquerade-proxy", "https://example.org",
		"--generate-password",
	)
	if code != 0 {
		t.Fatalf("Run(setup) = %d, ожидалось 0; stderr: %q", code, stderr)
	}

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer s.Close()

	cfg, err := s.Config()
	if err != nil {
		t.Fatalf("Config() = %v", err)
	}
	if cfg.Masquerade.Mode != config.MasqueradeProxy || cfg.Masquerade.Proxy != "https://example.org" {
		t.Errorf("masquerade = %+v, ожидалось proxy на https://example.org", cfg.Masquerade)
	}
}

// TestRunSetupRejectsMixedMasquerade — для off не нужен каталог, для file не нужен
// адрес зеркала: лишний флаг — это опечатка, а не запасной вариант.
func TestRunSetupRejectsMixedMasquerade(t *testing.T) {
	code, _, stderr := runSetup(t, "",
		"--db", filepath.Join(t.TempDir(), "hysteria-ward.db"),
		"--domain", "example.com",
		"--email", "admin@example.com",
		"--access", "tunnel",
		"--masquerade", "off",
		"--masquerade-file", t.TempDir(),
		"--generate-password",
	)
	if code == 0 {
		t.Fatal("Run(setup) с лишним --masquerade-file = 0, ожидался отказ")
	}
	if !strings.Contains(stderr, "off") {
		t.Errorf("в stderr нет причины: %q", stderr)
	}
}

// TestRunSetupRejectsDefaultPassword — начальный пароль admin не принимается:
// его знает кто угодно, а панель после установки уже слушает выбранный адрес.
func TestRunSetupRejectsDefaultPassword(t *testing.T) {
	code, _, stderr := runSetup(t, "admin\n",
		"--db", filepath.Join(t.TempDir(), "hysteria-ward.db"),
		"--domain", "example.com",
		"--email", "admin@example.com",
		"--access", "tunnel",
		"--masquerade", "off",
		"--password-stdin",
	)
	if code == 0 {
		t.Fatal("Run(setup) с паролем admin = 0, ожидался отказ")
	}
	if !strings.Contains(stderr, "паролем по умолчанию") {
		t.Errorf("в stderr нет причины: %q", stderr)
	}
}

// TestRunSetupNeedsPassword — без источника пароля начальная настройка не
// выполняется: панель не должна остаться с паролем, которого никто не знает.
func TestRunSetupNeedsPassword(t *testing.T) {
	code, _, stderr := runSetup(t, "",
		"--db", filepath.Join(t.TempDir(), "hysteria-ward.db"),
		"--domain", "example.com",
		"--email", "admin@example.com",
		"--access", "tunnel",
		"--masquerade", "off",
	)
	if code == 0 {
		t.Fatal("Run(setup) без пароля = 0, ожидался отказ")
	}
	if !strings.Contains(stderr, "пароль") {
		t.Errorf("в stderr нет причины: %q", stderr)
	}
}

// TestRunSetupNeedsAnswers — незаданные обязательные ответы перечисляются все
// сразу, чтобы установщик не гонял администратора по одному вопросу.
func TestRunSetupNeedsAnswers(t *testing.T) {
	code, _, stderr := runSetup(t, "", "--password-stdin")
	if code == 0 {
		t.Fatal("Run(setup) без ответов = 0, ожидался отказ")
	}

	for _, flag := range []string{"--domain", "--email", "--access", "--masquerade"} {
		if !strings.Contains(stderr, flag) {
			t.Errorf("в stderr нет %s: %q", flag, stderr)
		}
	}
}

// runSetup запускает setup и возвращает код, stdout и stderr.
func runSetup(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	code := cli.Run(context.Background(), append([]string{"setup"}, args...),
		strings.NewReader(stdin), &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}
