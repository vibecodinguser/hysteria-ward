package cli_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
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

// TestRunServeServesInterface — полный путь: serve берёт сертификат из настроек,
// открывает интерфейс по HTTPS, а отмена контекста (в бою — SIGTERM) завершает
// панель с кодом 0.
func TestRunServeServesInterface(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeCertificate(t, dir)
	dbPath := filepath.Join(dir, "hysteria-ward.db")
	logPath := filepath.Join(dir, "panel.log")
	panelAddr := freeAddr(t)

	seedConfig(t, dbPath, config.Config{
		Listen:     "127.0.0.1:0",
		Panel:      config.Panel{Listen: panelAddr},
		TLS:        config.TLS{Cert: certPath, Key: keyPath},
		Masquerade: config.Masquerade{Mode: config.MasqueradeOff},
	})

	panel := serve(t, "--db", dbPath, "--log", logPath)

	// Клиент доверяет ровно тому сертификату, который лежит в настройках: так
	// проверяется, что панель отдаёт именно его, а не что-нибудь своё.
	client := trusting(t, certPath)
	waitForInterface(t, client, panelAddr)

	status, header, body := get(t, client, "https://"+panelAddr+"/")
	if status != http.StatusOK {
		t.Fatalf("GET / = %d, ожидалось 200", status)
	}
	if contentType := header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Errorf("Content-Type = %q, ожидался HTML", contentType)
	}
	if !strings.Contains(body, `id="app"`) {
		t.Errorf("точка входа интерфейса не отдана: %q", body)
	}

	// Прямая ссылка на раздел тоже открывается: маршруты знает клиент.
	if status, _, deepLink := get(t, client, "https://"+panelAddr+"/users"); status != http.StatusOK || deepLink != body {
		t.Errorf("GET /users = %d, ожидалась та же точка входа", status)
	}

	// Тот же адрес по открытому HTTP не обслуживается: панель отвечает только по
	// шифрованному каналу.
	if status, _, _ := get(t, &http.Client{Timeout: 5 * time.Second}, "http://"+panelAddr+"/"); status != http.StatusBadRequest {
		t.Errorf("GET по открытому HTTP = %d, ожидался отказ", status)
	}

	panel.cancel()

	if code := panel.wait(t); code != 0 {
		t.Fatalf("Run(serve) = %d, ожидалось 0; stderr: %q", code, panel.stderr.String())
	}

	waitForLog(t, logPath, "панель остановлена")
}

// TestRunServeRefusesWithoutCertificate — без сертификата панель не поднимается:
// самоподписанный годится только для отладки и сам собой не берётся.
func TestRunServeRefusesWithoutCertificate(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "hysteria-ward.db")
	logPath := filepath.Join(dir, "panel.log")

	seedConfig(t, dbPath, config.Config{
		Listen:     "127.0.0.1:0",
		Panel:      config.Panel{Listen: freeAddr(t)},
		TLS:        config.TLS{Cert: filepath.Join(dir, "нет.pem"), Key: filepath.Join(dir, "нет.key")},
		Masquerade: config.Masquerade{Mode: config.MasqueradeOff},
	})

	panel := serve(t, "--db", dbPath, "--log", logPath)

	if code := panel.wait(t); code == 0 {
		t.Fatalf("Run(serve) = 0, ожидался отказ без сертификата")
	}
	if !strings.Contains(panel.stderr.String(), "сертификат") {
		t.Errorf("в stderr нет причины: %q", panel.stderr.String())
	}
}

// TestRunServeDebugWithoutCertificate — отладочный запуск поднимает интерфейс с
// самоподписанным сертификатом: посмотреть панель можно до появления домена.
func TestRunServeDebugWithoutCertificate(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "hysteria-ward.db")
	logPath := filepath.Join(dir, "panel.log")
	panelAddr := freeAddr(t)

	seedConfig(t, dbPath, config.Config{
		Listen:     "127.0.0.1:0",
		Panel:      config.Panel{Listen: panelAddr},
		TLS:        config.TLS{Cert: filepath.Join(dir, "нет.pem"), Key: filepath.Join(dir, "нет.key")},
		Masquerade: config.Masquerade{Mode: config.MasqueradeOff},
	})

	panel := serve(t, "--db", dbPath, "--log", logPath, "--debug")

	// Сертификат самоподписанный, поэтому доверять ему нечем: проверяем сам факт
	// работы интерфейса.
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}

	waitForInterface(t, client, panelAddr)

	if log := readLog(t, logPath); !strings.Contains(log, "самоподписанный") {
		t.Errorf("в журнале нет предупреждения о самоподписанном сертификате: %q", log)
	}

	status, _, body := get(t, client, "https://"+panelAddr+"/")
	if status != http.StatusOK {
		t.Fatalf("GET / = %d, ожидалось 200", status)
	}
	if !strings.Contains(body, `id="app"`) {
		t.Errorf("точка входа интерфейса не отдана: %q", body)
	}

	// Ядро при этом не поднялось: отладка интерфейса не повод пускать трафик.
	if log := readLog(t, logPath); !strings.Contains(log, "конфигурация не применена") {
		t.Errorf("в журнале нет записи о несработавшем ядре: %q", log)
	}

	panel.cancel()

	if code := panel.wait(t); code != 0 {
		t.Fatalf("Run(serve) = %d, ожидалось 0; stderr: %q", code, panel.stderr.String())
	}
}

// running — запущенный serve.
type running struct {
	done   <-chan int
	stderr *bytes.Buffer
	cancel context.CancelFunc
}

// serve запускает serve с переданными аргументами.
func serve(t *testing.T, args ...string) *running {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan int, 1)
	stderr := &bytes.Buffer{}

	go func() {
		done <- cli.Run(ctx, append([]string{"serve"}, args...), &bytes.Buffer{}, stderr)
	}()

	return &running{done: done, stderr: stderr, cancel: cancel}
}

// wait возвращает код возврата панели или роняет тест по таймауту.
func (r *running) wait(t *testing.T) int {
	t.Helper()

	select {
	case code := <-r.done:
		return code
	case <-time.After(10 * time.Second):
		t.Fatal("панель не завершилась")
		return -1
	}
}

// get выполняет запрос и возвращает код ответа, заголовки и тело.
func get(t *testing.T, client *http.Client, url string) (int, http.Header, string) {
	t.Helper()

	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("тело ответа на GET %s: %v", url, err)
	}

	return resp.StatusCode, resp.Header, string(content)
}

// trusting собирает клиента, который доверяет ровно этому сертификату.
func trusting(t *testing.T, certPath string) *http.Client {
	t.Helper()

	content, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("сертификат не прочитан: %v", err)
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(content) {
		t.Fatalf("сертификат не разобран: %s", certPath)
	}

	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}},
	}
}

// freeAddr занимает и сразу освобождает порт: тесту нужен адрес, который панель
// сможет занять сама.
func freeAddr(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("свободный порт не найден: %v", err)
	}
	defer listener.Close()

	return listener.Addr().String()
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

// readLog читает журнал панели.
func readLog(t *testing.T, path string) string {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("журнал не прочитан: %v", err)
	}

	return string(content)
}

// waitForLog ждёт, пока в журнале появится подстрока.
func waitForLog(t *testing.T, path, want string) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(content), want) {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("в журнале так и не появилось %q", want)
}

// waitForInterface ждёт, пока интерфейс начнёт отвечать.
//
// Ждём ответа, а не записи в журнале: панель в тесте должна быть готова к
// запросам, а не «судя по журналу, готова», и формулировки другого пакета тесту
// знать не обязательно.
func waitForInterface(t *testing.T, client *http.Client, addr string) {
	t.Helper()

	var lastErr error

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get("https://" + addr + "/")
		if err == nil {
			resp.Body.Close()
			return
		}

		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("интерфейс так и не начал отвечать: %v", lastErr)
}

// writeCertificate готовит самоподписанный сертификат для запуска панели в тесте.
// Сертификат покрывает петлю: панель в тесте слушает 127.0.0.1.
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
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
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
