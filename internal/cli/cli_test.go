package cli_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/build"
	"github.com/vibecodinguser/hysteria-ward/internal/cli"
	"github.com/vibecodinguser/hysteria-ward/internal/config"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// TestRunVersion проверяет, что --version печатает метаданные сборки
// и завершается успешно.
//
// Сам формат строки зафиксирован литералами в тестах пакета build;
// здесь проверяется проводка: точка входа печатает именно его.
func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := cli.Run(context.Background(), []string{"--version"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("Run(--version) вернул код %d, ожидался 0; stderr: %q", code, stderr.String())
	}

	want := build.Current().String() + "\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout = %q, ожидалось %q", got, want)
	}

	if strings.Count(stdout.String(), "\n") != 1 {
		t.Errorf("ожидалась ровно одна строка, получено %q", stdout.String())
	}
}

// TestRunHelp проверяет, что --help печатает справку в stdout и завершается успешно.
func TestRunHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := cli.Run(context.Background(), []string{"--help"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("Run(--help) вернул код %d, ожидался 0; stderr: %q", code, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "Использование:") || !strings.Contains(out, build.Name) {
		t.Errorf("справка не похожа на справку: %q", out)
	}
	if stderr.Len() != 0 {
		t.Errorf("справка ушла в stderr: %q", stderr.String())
	}
}

// TestRunUnknownArgument проверяет, что неизвестный аргумент — это ошибка,
// а не молчаливое согласие: сообщение уходит в stderr и указывает на --help.
func TestRunUnknownArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := cli.Run(context.Background(), []string{"--такого-нет"}, &stdout, &stderr)

	if code == 0 {
		t.Errorf("Run(неизвестный аргумент) вернул 0, ожидался ненулевой код")
	}
	if !strings.Contains(stderr.String(), "--такого-нет") {
		t.Errorf("в stderr нет имени неизвестного аргумента: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "--help") {
		t.Errorf("в stderr нет подсказки про --help: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("в stdout что-то попало: %q", stdout.String())
	}
}

// TestRunWithoutArguments проверяет поведение без аргументов: запускать нечего,
// поэтому это справка и ненулевой код.
func TestRunWithoutArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := cli.Run(context.Background(), nil, &stdout, &stderr)

	if code == 0 {
		t.Errorf("Run() без аргументов вернул 0, ожидался ненулевой код")
	}
	if !strings.Contains(stderr.String(), "Использование:") {
		t.Errorf("в stderr нет справки: %q", stderr.String())
	}
}

// TestRunServeStopsOnContextCancel — полный путь: serve поднимает ядро по
// конфигурации из базы, отмена контекста (в бою — SIGTERM) завершает панель с
// кодом 0.
func TestRunServeStopsOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeCertificate(t, dir)
	dbPath := filepath.Join(dir, "hysteria-ward.db")
	logPath := filepath.Join(dir, "panel.log")

	seedConfig(t, dbPath, config.Config{
		Listen:     "127.0.0.1:0",
		TLS:        config.TLS{Cert: certPath, Key: keyPath},
		Masquerade: config.Masquerade{Mode: config.MasqueradeOff},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- cli.Run(ctx, []string{"serve", "--db", dbPath, "--log", logPath}, &stdout, &stderr)
	}()

	waitForLog(t, logPath, "ядро запущено")
	cancel()

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("Run(serve) = %d, ожидалось 0; stderr: %q", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("панель не завершилась после отмены контекста")
	}

	waitForLog(t, logPath, "панель остановлена")
}

// TestRunServeRunsWithoutCore — ядро без читаемого сертификата не поднимется, но
// панель обязана работать: иначе администратор не откроет интерфейс и не починит
// конфигурацию снаружи.
func TestRunServeRunsWithoutCore(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "hysteria-ward.db")
	logPath := filepath.Join(dir, "panel.log")

	seedConfig(t, dbPath, config.Config{
		Listen:     "127.0.0.1:0",
		TLS:        config.TLS{Cert: filepath.Join(dir, "нет.pem"), Key: filepath.Join(dir, "нет.key")},
		Masquerade: config.Masquerade{Mode: config.MasqueradeOff},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- cli.Run(ctx, []string{"serve", "--db", dbPath, "--log", logPath}, &stdout, &stderr)
	}()

	waitForLog(t, logPath, "конфигурация не применена")

	select {
	case code := <-done:
		t.Fatalf("панель завершилась без ядра, код %d; stderr: %q", code, stderr.String())
	default:
	}

	cancel()

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("Run(serve) = %d, ожидалось 0; stderr: %q", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("панель не завершилась после отмены контекста")
	}
}

// seedConfig кладёт конфигурацию в базу до запуска панели — так же, как это
// сделает установщик на чистом сервере.
func seedConfig(t *testing.T, dbPath string, cfg config.Config) {
	t.Helper()

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer s.Close()

	if err := s.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig() = %v", err)
	}
}

// waitForLog ждёт, пока в журнале появится подстрока.
func waitForLog(t *testing.T, path, want string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(content), want) {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("в журнале так и не появилось %q", want)
}

// writeCertificate готовит самоподписанный сертификат для запуска ядра в тесте.
func writeCertificate(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ключ не сгенерировался: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "hysteria-ward.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"hysteria-ward.test"},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("сертификат не создался: %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("ключ не сериализовался: %v", err)
	}

	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("сертификат не записан: %v", err)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("ключ не записан: %v", err)
	}

	return certPath, keyPath
}
