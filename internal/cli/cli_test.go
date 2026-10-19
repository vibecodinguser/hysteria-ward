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
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/authn"
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

	code := cli.Run(context.Background(), []string{"--version"}, strings.NewReader(""), &stdout, &stderr)

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

	code := cli.Run(context.Background(), []string{"--help"}, strings.NewReader(""), &stdout, &stderr)

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

	code := cli.Run(context.Background(), []string{"--такого-нет"}, strings.NewReader(""), &stdout, &stderr)

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

	code := cli.Run(context.Background(), nil, strings.NewReader(""), &stdout, &stderr)

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
	panelAddr := seedPanel(t, dbPath, config.Config{
		Listen:     "127.0.0.1:0",
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

	seedPanel(t, dbPath, config.Config{
		Listen:     "127.0.0.1:0",
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
	panelAddr := seedPanel(t, dbPath, config.Config{
		Listen:     "127.0.0.1:0",
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

// TestRunServeStartsWithPendingCertificate — домен и почта заданы, а сертификата
// ещё нет: панель открывается с самоподписанным даже без --debug. Иначе провал
// ACME оставил бы администратора без входа и без способа починить настройку.
func TestRunServeStartsWithPendingCertificate(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "hysteria-ward.db")
	logPath := filepath.Join(dir, "panel.log")
	certPath := filepath.Join(dir, "certs", "fullchain.pem")
	keyPath := filepath.Join(dir, "certs", "privkey.pem")

	panelAddr := seedPanel(t, dbPath, config.Config{
		Listen:     "127.0.0.1:0",
		TLS:        config.TLS{Cert: certPath, Key: keyPath},
		Masquerade: config.Masquerade{Mode: config.MasqueradeOff},
	})

	// Выпуск настраивается на DNS с заведомо падающим хуком: тест не должен
	// занимать порт 80 и ходить в настоящий ACME.
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	if err := s.SaveDomain("example.com"); err != nil {
		t.Fatalf("SaveDomain() = %v", err)
	}
	if err := s.SaveACMEEmail("admin@example.com"); err != nil {
		t.Fatalf("SaveACMEEmail() = %v", err)
	}
	if err := s.SaveACMESettings(store.ACMESettings{Mode: "dns", DNSHook: "/bin/false"}); err != nil {
		t.Fatalf("SaveACMESettings() = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	panel := serve(t, "--db", dbPath, "--log", logPath)

	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}

	waitForInterface(t, client, panelAddr)

	if log := readLog(t, logPath); !strings.Contains(log, "самоподписанный") {
		t.Errorf("в журнале нет предупреждения о самоподписанном сертификате: %q", log)
	}

	panel.cancel()

	if code := panel.wait(t); code != 0 {
		t.Fatalf("Run(serve) = %d, ожидалось 0; stderr: %q", code, panel.stderr.String())
	}
}

// TestRunServeLoginCycle — панель целиком: вход по HTTPS, работа под сессией,
// выход и отказ после выхода.
//
// Здесь проверяется вся проводка: настройки из базы, сертификат, база, вход и
// интерфейс. Тот же круг по частям проверяют тесты authn, store и api; тут важно,
// что он сходится вместе — в том виде, в каком его соберёт serve.
func TestRunServeLoginCycle(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeCertificate(t, dir)
	dbPath := filepath.Join(dir, "hysteria-ward.db")
	logPath := filepath.Join(dir, "panel.log")
	panelAddr := seedPanel(t, dbPath, config.Config{
		Listen:     "127.0.0.1:0",
		TLS:        config.TLS{Cert: certPath, Key: keyPath},
		Masquerade: config.Masquerade{Mode: config.MasqueradeOff},
	})

	panel := serve(t, "--db", dbPath, "--log", logPath)

	client := trusting(t, certPath)
	waitForInterface(t, client, panelAddr)

	base := "https://" + panelAddr

	// До входа панель данных не отдаёт.
	if status, _, _ := get(t, client, base+"/api/status"); status != http.StatusUnauthorized {
		t.Errorf("состояние без входа = %d, ожидался 401", status)
	}

	// Интерфейс при этом отдаётся: иначе войти было бы негде.
	if status, _, _ := get(t, client, base+"/"); status != http.StatusOK {
		t.Errorf("интерфейс без входа = %d, ожидался 200", status)
	}

	status, resp, body := do(t, client, http.MethodPost, base+"/api/login",
		`{"name":"admin","password":"admin"}`, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("вход = %d (%s), ожидался 200", status, body)
	}

	var entered struct {
		Name string `json:"name"`
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal([]byte(body), &entered); err != nil {
		t.Fatalf("ответ о входе не разобран: %v", err)
	}
	if entered.Name != "admin" || entered.CSRF == "" {
		t.Fatalf("ответ о входе = %+v", entered)
	}

	var session *http.Cookie
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "hysteria_ward_session" {
			session = cookie
		}
	}
	if session == nil {
		t.Fatal("панель не выдала cookie сессии")
	}

	if status, _, _ := do(t, client, http.MethodGet, base+"/api/status", "", session, nil); status != http.StatusOK {
		t.Errorf("состояние под сессией = %d, ожидался 200", status)
	}

	// Выход без csrf-токена не проходит: cookie чужой сайт пришлёт, а заголовок
	// подставить не сможет.
	if status, _, _ := do(t, client, http.MethodPost, base+"/api/logout", "", session, nil); status != http.StatusForbidden {
		t.Errorf("выход без csrf-токена = %d, ожидался 403", status)
	}

	status, _, _ = do(t, client, http.MethodPost, base+"/api/logout", "", session,
		map[string]string{"X-CSRF-Token": entered.CSRF})
	if status != http.StatusNoContent {
		t.Fatalf("выход = %d, ожидался 204", status)
	}

	if status, _, _ := do(t, client, http.MethodGet, base+"/api/status", "", session, nil); status != http.StatusUnauthorized {
		t.Errorf("состояние после выхода = %d, ожидался 401", status)
	}

	panel.cancel()

	if code := panel.wait(t); code != 0 {
		t.Fatalf("Run(serve) = %d, ожидалось 0; stderr: %q", code, panel.stderr.String())
	}
}

// TestRunServeKeepsSessionAcrossRestart — сессия переживает перезапуск панели:
// обновление панели не должно выбрасывать администратора наружу, тем более что
// обновляется он ради неё.
//
// Проверка сквозная: сессия проходит через остановку процесса, перезапуск, базу и
// ту же cookie. По частям то же самое проверяют authn и store, здесь важно, что
// сходится вместе — в том виде, в каком его соберёт serve.
func TestRunServeKeepsSessionAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeCertificate(t, dir)
	dbPath := filepath.Join(dir, "hysteria-ward.db")
	logPath := filepath.Join(dir, "panel.log")
	panelAddr := seedPanel(t, dbPath, config.Config{
		Listen:     "127.0.0.1:0",
		TLS:        config.TLS{Cert: certPath, Key: keyPath},
		Masquerade: config.Masquerade{Mode: config.MasqueradeOff},
	})

	client := trusting(t, certPath)
	base := "https://" + panelAddr

	panel := serve(t, "--db", dbPath, "--log", logPath)
	waitForInterface(t, client, panelAddr)

	status, resp, body := do(t, client, http.MethodPost, base+"/api/login",
		`{"name":"admin","password":"admin"}`, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("вход = %d (%s), ожидался 200", status, body)
	}

	var session *http.Cookie
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "hysteria_ward_session" {
			session = cookie
		}
	}
	if session == nil {
		t.Fatal("панель не выдала cookie сессии")
	}

	panel.cancel()
	if code := panel.wait(t); code != 0 {
		t.Fatalf("Run(serve) = %d, ожидалось 0; stderr: %q", code, panel.stderr.String())
	}

	panel = serve(t, "--db", dbPath, "--log", logPath)
	waitForInterface(t, client, panelAddr)

	if status, _, _ := do(t, client, http.MethodGet, base+"/api/status", "", session, nil); status != http.StatusOK {
		t.Errorf("состояние после перезапуска = %d, ожидался 200: сессия не пережила перезапуск", status)
	}

	panel.cancel()
	if code := panel.wait(t); code != 0 {
		t.Fatalf("Run(serve) = %d, ожидалось 0; stderr: %q", code, panel.stderr.String())
	}
}

// TestRunServeSwitchesAccess — публичный доступ включается из интерфейса, не
// перезапуская панель, и переживает перезапуск.
//
// Круг проверяется целиком, потому что решает его не одна функция: панель не
// открывается с паролем по умолчанию, перевешивает слушатель на ходу, запоминает
// выбор в базе и встаёт по нему при следующем запуске. По частям то же самое
// проверяют api и store; здесь важно, что сходится вместе — и что переключение не
// стоит перезапуска.
func TestRunServeSwitchesAccess(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeCertificate(t, dir)
	dbPath := filepath.Join(dir, "hysteria-ward.db")
	logPath := filepath.Join(dir, "panel.log")

	panelAddr := seedPanel(t, dbPath, config.Config{
		Listen:     "127.0.0.1:0",
		TLS:        config.TLS{Cert: certPath, Key: keyPath},
		Masquerade: config.Masquerade{Mode: config.MasqueradeOff},
	})
	port := portOf(t, panelAddr)

	client := trusting(t, certPath)
	base := "https://" + panelAddr

	// С паролем по умолчанию панель наружу не открывается: этот пароль знает кто
	// угодно.
	panel := serve(t, "--db", dbPath, "--log", logPath)
	waitForInterface(t, client, panelAddr)

	cookie, csrf := signIn(t, client, base, authn.DefaultName, authn.DefaultPassword)

	status, _, body := do(t, client, http.MethodPost, base+"/api/panel/access", `{"access":"public"}`, cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if status != http.StatusConflict {
		t.Fatalf("публичный доступ с паролем по умолчанию = %d (%s), ожидался 409", status, body)
	}

	panel.cancel()
	if code := panel.wait(t); code != 0 {
		t.Fatalf("Run(serve) = %d, ожидалось 0; stderr: %q", code, panel.stderr.String())
	}

	// База освободилась — пароль можно сменить.
	var stdout, stderr bytes.Buffer
	if code := cli.Run(context.Background(), []string{"passwd", "--db", dbPath},
		strings.NewReader("новый пароль\n"), &stdout, &stderr); code != 0 {
		t.Fatalf("Run(passwd) = %d; stderr: %q", code, stderr.String())
	}

	panel = serve(t, "--db", dbPath, "--log", logPath)
	waitForInterface(t, client, panelAddr)

	cookie, csrf = signIn(t, client, base, authn.DefaultName, "новый пароль")

	status, _, body = do(t, client, http.MethodPost, base+"/api/panel/access", `{"access":"public"}`, cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if status != http.StatusOK {
		t.Fatalf("публичный доступ = %d (%s), ожидался 200", status, body)
	}

	if !strings.Contains(body, `"access":"public"`) || !strings.Contains(body, `"addr":"0.0.0.0:`+strconv.Itoa(port)) {
		t.Errorf("ответ на переключение = %s", body)
	}

	waitForLog(t, logPath, "панель открыта наружу")

	panel.cancel()
	if code := panel.wait(t); code != 0 {
		t.Fatalf("Run(serve) = %d, ожидалось 0; stderr: %q", code, panel.stderr.String())
	}

	// Выбор пережил перезапуск: панель встала на все интерфейсы, а не на петлю.
	panel = serve(t, "--db", dbPath, "--log", logPath)
	waitForLog(t, logPath, "access=public")
	waitForInterface(t, client, panelAddr)

	cookie, _ = signIn(t, client, base, authn.DefaultName, "новый пароль")

	status, _, body = do(t, client, http.MethodGet, base+"/api/panel", "", cookie, nil)
	if status != http.StatusOK {
		t.Fatalf("настройки панели после перезапуска = %d, ожидался 200", status)
	}
	if !strings.Contains(body, `"access":"public"`) {
		t.Errorf("после перезапуска панель снова спрятана: %s", body)
	}

	panel.cancel()
	if code := panel.wait(t); code != 0 {
		t.Fatalf("Run(serve) = %d, ожидалось 0; stderr: %q", code, panel.stderr.String())
	}
}

// TestRunPasswdSetsPassword — passwd меняет пароль администратора.
//
// Пароль приходит из трубы: так установщик передаст сгенерированный пароль, не
// светя его в списке процессов. С терминала тот же путь спрашивает дважды и не
// показывая набранное.
func TestRunPasswdSetsPassword(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hysteria-ward.db")

	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), []string{"passwd", "--db", dbPath, "--name", "хозяин"},
		strings.NewReader("новый пароль\n"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run(passwd) = %d, ожидалось 0; stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "изменён") {
		t.Errorf("в stdout нет подтверждения: %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "новый пароль") {
		t.Errorf("пароль попал в вывод: %q", stdout.String())
	}

	// Читаем то же, что прочитает панель при входе.
	name, hash := credential(t, dbPath)
	if name != "хозяин" {
		t.Errorf("имя = %q, ожидалось %q", name, "хозяин")
	}

	matched, err := authn.Verify(hash, "новый пароль")
	if err != nil {
		t.Fatalf("Verify() = %v", err)
	}
	if !matched {
		t.Error("заданный пароль не подошёл")
	}
}

// TestRunPasswdKeepsName — без --name имя остаётся прежним: панель знает, кто её
// администратор, и подменять его при смене пароля незачем.
func TestRunPasswdKeepsName(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hysteria-ward.db")

	// На пустой базе имя взять неоткуда, кроме значения по умолчанию.
	if code := passwd(t, dbPath, "", "первый"); code != 0 {
		t.Fatalf("Run(passwd) = %d, ожидалось 0", code)
	}
	if name, _ := credential(t, dbPath); name != authn.DefaultName {
		t.Errorf("имя на пустой базе = %q, ожидалось %q", name, authn.DefaultName)
	}

	if code := passwd(t, dbPath, "хозяин", "второй"); code != 0 {
		t.Fatalf("Run(passwd) = %d, ожидалось 0", code)
	}
	if code := passwd(t, dbPath, "", "третий"); code != 0 {
		t.Fatalf("Run(passwd) = %d, ожидалось 0", code)
	}

	name, hash := credential(t, dbPath)
	if name != "хозяин" {
		t.Errorf("имя = %q, ожидалось %q", name, "хозяин")
	}

	matched, err := authn.Verify(hash, "третий")
	if err != nil {
		t.Fatalf("Verify() = %v", err)
	}
	if !matched {
		t.Error("пароль не сменился")
	}
}

// TestRunPasswdRejectsEmptyPassword — пустой пароль не задаётся: панель, открытая
// по одному щелчку, — это панель без пароля.
func TestRunPasswdRejectsEmptyPassword(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := cli.Run(context.Background(), []string{"passwd", "--db", filepath.Join(t.TempDir(), "hysteria-ward.db")},
		strings.NewReader("\n"), &stdout, &stderr)
	if code == 0 {
		t.Fatalf("Run(passwd) с пустым паролем = 0, ожидался отказ")
	}
	if !strings.Contains(stderr.String(), "пустой пароль") {
		t.Errorf("в stderr нет причины: %q", stderr.String())
	}
}

// TestRunPasswdNeedsFreeDatabase — пока панель работает, база занята, и passwd
// говорит об этом, вместо того чтобы ждать вечно.
func TestRunPasswdNeedsFreeDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "hysteria-ward.db")
	logPath := filepath.Join(dir, "panel.log")
	panelAddr := seedPanel(t, dbPath, config.Config{
		Listen:     "127.0.0.1:0",
		Masquerade: config.Masquerade{Mode: config.MasqueradeOff},
	})

	panel := serve(t, "--db", dbPath, "--log", logPath, "--debug")

	// Панель должна успеть открыть базу: иначе passwd обгонит её и проверит
	// пустую базу вместо занятой.
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	waitForInterface(t, client, panelAddr)

	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), []string{"passwd", "--db", dbPath},
		strings.NewReader("новый пароль\n"), &stdout, &stderr)
	if code == 0 {
		t.Fatalf("Run(passwd) при работающей панели = 0, ожидался отказ")
	}
	if !strings.Contains(stderr.String(), "занята") {
		t.Errorf("в stderr нет объяснения: %q", stderr.String())
	}

	panel.cancel()

	if code := panel.wait(t); code != 0 {
		t.Fatalf("Run(serve) = %d, ожидалось 0; stderr: %q", code, panel.stderr.String())
	}
}

// passwd запускает passwd с паролем из трубы.
func passwd(t *testing.T, dbPath, name, password string) int {
	t.Helper()

	args := []string{"passwd", "--db", dbPath}
	if name != "" {
		args = append(args, "--name", name)
	}

	var stdout, stderr bytes.Buffer

	return cli.Run(context.Background(), args, strings.NewReader(password+"\n"), &stdout, &stderr)
}

// credential читает учётные данные из базы.
func credential(t *testing.T, dbPath string) (string, string) {
	t.Helper()

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer s.Close()

	credential, ok, err := s.Credential()
	if err != nil {
		t.Fatalf("Credential() = %v", err)
	}
	if !ok {
		t.Fatal("учётные данные не заданы")
	}

	return credential.Name, credential.Hash
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
		done <- cli.Run(ctx, append([]string{"serve"}, args...), strings.NewReader(""), &bytes.Buffer{}, stderr)
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

// do выполняет запрос с телом, cookie и заголовками и возвращает код ответа,
// заголовки с cookie и тело.
//
// Ответ возвращается уже прочитанным: телу запроса и ответа в тесте незачем жить
// дольше проверки.
func do(t *testing.T, client *http.Client, method, url, body string, cookie *http.Cookie, headers map[string]string) (int, *http.Response, string) {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, url, reader)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}

	if cookie != nil {
		req.AddCookie(cookie)
	}

	for name, value := range headers {
		req.Header.Set(name, value)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("тело ответа на %s %s: %v", method, url, err)
	}

	return resp.StatusCode, resp, string(content)
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

// signIn входит в панель и возвращает cookie сессии и csrf-токен: без второго
// изменяющий запрос не пройдёт.
func signIn(t *testing.T, client *http.Client, base, name, password string) (*http.Cookie, string) {
	t.Helper()

	status, resp, body := do(t, client, http.MethodPost, base+"/api/login",
		`{"name":"`+name+`","password":"`+password+`"}`, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("вход = %d (%s), ожидался 200", status, body)
	}

	var entered struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal([]byte(body), &entered); err != nil {
		t.Fatalf("ответ о входе не разобран: %v", err)
	}

	for _, cookie := range resp.Cookies() {
		if cookie.Name == "hysteria_ward_session" {
			return cookie, entered.CSRF
		}
	}

	t.Fatal("панель не выдала cookie сессии")

	return nil, ""
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

// seedPanel кладёт в базу конфигурацию сервера и настройки панели на свободном
// порту — так же, как это сделает установщик на чистом сервере, — и возвращает
// адрес, по которому панель будет доступна.
//
// Наружу панель при этом не выставлена: в настройках режим туннеля, то есть
// петля.
func seedPanel(t *testing.T, dbPath string, cfg config.Config) string {
	t.Helper()

	port := portOf(t, freeAddr(t))

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer s.Close()

	if err := s.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig() = %v", err)
	}

	settings := store.PanelSettings{Access: store.AccessTunnel, Port: port}
	if err := s.SavePanelSettings(settings); err != nil {
		t.Fatalf("SavePanelSettings() = %v", err)
	}

	return net.JoinHostPort(settings.Access.Host(), strconv.Itoa(port))
}

// portOf достаёт номер порта из адреса.
func portOf(t *testing.T, addr string) int {
	t.Helper()

	_, raw, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("адрес %q не разобран: %v", addr, err)
	}

	number, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("порт %q не разобран: %v", raw, err)
	}

	return number
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
