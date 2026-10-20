// Package e2e поднимает панель целиком и проверяет её по настоящему HTTPS.
//
// Это не тест отдельного пакета: здесь сходится всё, что в бою собирается
// установщиком, — база, сертификат, настройки панели, служба и интерфейс. Такой
// прогон ловит поломки на стыках, которые модульные тесты по определению не
// видят: например, вход, который перестал выдавать cookie, или защищённый
// эндпоинт, открывшийся без сессии.
//
// Каркас намеренно отделён от сценариев. Сценарий — обычная функция, получающая
// *Panel; чтобы добавить новый, не нужно трогать запуск, ожидание готовности и
// остановку. Панель поднимается на свободном порту: фиксированный порт в CI
// рано или поздно занял бы кто-то другой.
package e2e

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/certs"
	"github.com/vibecodinguser/hysteria-ward/internal/cli"
	"github.com/vibecodinguser/hysteria-ward/internal/config"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

const (
	// readyTimeout — сколько ждать, пока панель начнёт отвечать. В CI машина
	// бывает занята, но дольше этого срока ждать уже нечего: значит, панель не
	// поднялась.
	readyTimeout = 15 * time.Second

	// stopTimeout — сколько ждать штатной остановки. Она должна быть быстрой:
	// Run закрывает слушатель и ждёт начатые запросы.
	stopTimeout = 15 * time.Second

	// pollInterval — как часто спрашивать проверку работоспособности.
	pollInterval = 20 * time.Millisecond

	// defaultName и defaultPassword — пара, которую панель заводит на пустой
	// базе. Сценарии входят ею, как это сделал бы администратор после установки.
	defaultName     = "admin"
	defaultPassword = "admin"
)

// Option дополняет запуск панели.
type Option func(*settings)

// settings — то, что можно поменять до старта.
type settings struct {
	setup func(*store.Store)
	args  []string
}

// WithSetup выполняет подготовку базы до запуска панели: заводит пользователя,
// задаёт домен или другие настройки, которые в бою пришли бы из установщика.
//
// База открывается на запись и закрывается до старта: панель должна получить её
// свободной, иначе второй открыватель упрётся в блокировку.
func WithSetup(setup func(*store.Store)) Option {
	return func(o *settings) {
		o.setup = setup
	}
}

// WithArgs добавляет аргументы serve, например --debug.
func WithArgs(args ...string) Option {
	return func(o *settings) {
		o.args = append(o.args, args...)
	}
}

// Panel — запущенная панель и всё, что нужно сценарию.
type Panel struct {
	t       *testing.T
	dir     string
	dbPath  string
	logPath string
	baseURL string

	client *http.Client

	session *http.Cookie
	csrf    string

	cancel context.CancelFunc
	done   chan int
	stderr *bytes.Buffer

	stopOnce sync.Once
}

// Response — прочитанный ответ панели. Тело уже в памяти: сценарию не нужно
// помнить о закрытии.
type Response struct {
	Status  int
	Header  http.Header
	Cookies []*http.Cookie
	Body    []byte
}

// Start поднимает панель на свободном порту и ждёт, пока она начнёт отвечать.
//
// Временный каталог принадлежит тесту: testing удаляет его сам, а панель пишет
// только туда. Остановка зарегистрирована в t.Cleanup, поэтому сценарию не нужно
// помнить о ней даже при падении проверки.
func Start(t *testing.T, options ...Option) *Panel {
	t.Helper()

	var opts settings
	for _, option := range options {
		option(&opts)
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "hysteria-ward.db")
	logPath := filepath.Join(dir, "panel.log")
	certPath := filepath.Join(dir, "certs", "fullchain.pem")
	keyPath := filepath.Join(dir, "certs", "privkey.pem")

	certificate, err := certs.SelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatalf("сертификат для e2e: %v", err)
	}
	if err := certs.Save(certPath, keyPath, certificate); err != nil {
		t.Fatalf("запись сертификата для e2e: %v", err)
	}

	port := freePort(t)

	seed(t, dbPath, config.Config{
		Listen:     "127.0.0.1:0",
		TLS:        config.TLS{Cert: certPath, Key: keyPath},
		Masquerade: config.Masquerade{Mode: config.MasqueradeOff},
	}, store.PanelSettings{Access: store.AccessTunnel, Port: port}, opts.setup)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	stderr := &bytes.Buffer{}

	args := append([]string{"serve", "--db", dbPath, "--log", logPath}, opts.args...)
	go func() {
		done <- cli.Run(ctx, args, strings.NewReader(""), io.Discard, stderr)
	}()

	panel := &Panel{
		t:       t,
		dir:     dir,
		dbPath:  dbPath,
		logPath: logPath,
		baseURL: fmt.Sprintf("https://127.0.0.1:%d", port),
		client: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
		cancel: cancel,
		done:   done,
		stderr: stderr,
	}

	t.Cleanup(func() { panel.Stop() })

	panel.waitReady()

	return panel
}

// seed готовит базу так, как это сделал бы установщик: конфигурация сервера,
// настройки панели на выбранном порту и всё, что добавил сценарий.
func seed(t *testing.T, dbPath string, cfg config.Config, settings store.PanelSettings, setup func(*store.Store)) {
	t.Helper()

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("база для e2e: %v", err)
	}

	if err := db.SaveConfig(cfg); err != nil {
		_ = db.Close()
		t.Fatalf("конфигурация для e2e: %v", err)
	}
	if err := db.SavePanelSettings(settings); err != nil {
		_ = db.Close()
		t.Fatalf("настройки панели для e2e: %v", err)
	}
	if setup != nil {
		setup(db)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("закрытие базы для e2e: %v", err)
	}
}

// waitReady ждёт, пока панель начнёт отвечать на проверку работоспособности.
//
// Ждём ответа, а не записи в журнале: сценарию нужна готовая панель, а не
// «судя по журналу, готовая». Если процесс успел завершиться, причина берётся из
// его вывода — иначе тест ругался бы на таймаут вместо настоящей ошибки.
func (p *Panel) waitReady() {
	p.t.Helper()

	deadline := time.Now().Add(readyTimeout)
	var lastErr error

	for time.Now().Before(deadline) {
		select {
		case code := <-p.done:
			// Код возвращается в канал: остановка в Cleanup должна увидеть его, а
			// не ждать процесс, который уже закончился.
			p.done <- code
			p.t.Fatalf("панель завершилась до готовности (код %d): %s", code, p.stderr.String())
		default:
		}

		resp, err := p.client.Get(p.baseURL + "/api/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}

			lastErr = fmt.Errorf("проверка работоспособности: код %d", resp.StatusCode)
		} else {
			lastErr = err
		}

		time.Sleep(pollInterval)
	}

	p.t.Fatalf("панель не начала отвечать за %s: %v; журнал: %s", readyTimeout, lastErr, p.stderr.String())
}

// Stop останавливает панель и проверяет, что она действительно остановилась.
//
// Повторный вызов безопасен: t.Cleanup зовёт Stop после сценария, а сценарий
// мог остановить панель сам, чтобы проверить поведение после остановки.
func (p *Panel) Stop() {
	p.stopOnce.Do(func() {
		p.cancel()

		select {
		case code := <-p.done:
			if code != 0 {
				p.t.Errorf("панель остановилась с кодом %d: %s", code, p.stderr.String())
			}
		case <-time.After(stopTimeout):
			p.t.Errorf("панель не остановилась за %s", stopTimeout)
			return
		}

		p.client.CloseIdleConnections()

		// Слушатель должен быть отпущен: иначе следующий запуск на этом порту
		// упёрся бы в занятый адрес, а тест оставил бы за собой живой сокет.
		addr := strings.TrimPrefix(p.baseURL, "https://")
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			p.t.Errorf("после остановки порт %s всё ещё занят: %v", addr, err)
			return
		}
		_ = listener.Close()
	})
}

// BaseURL — адрес панели, например https://127.0.0.1:43210.
func (p *Panel) BaseURL() string {
	return p.baseURL
}

// Client — HTTPS-клиент, доверяющий сертификату панели. Он же хранит cookie:
// сценарию не нужно собирать его заново.
func (p *Panel) Client() *http.Client {
	return p.client
}

// Dir — временный каталог панели. В нём лежат база, журнал и сертификаты; сюда
// же сценарий может положить свои файлы, например сайт для masquerade.
func (p *Panel) Dir() string {
	return p.dir
}

// DBPath — файл базы.
func (p *Panel) DBPath() string {
	return p.dbPath
}

// LogPath — файл журнала.
func (p *Panel) LogPath() string {
	return p.logPath
}

// Log — прочитанный журнал панели. Если журнала ещё нет, возвращается пустая
// строка: тест не должен зависеть от момента записи.
func (p *Panel) Log() string {
	content, err := os.ReadFile(p.logPath)
	if err != nil {
		return ""
	}

	return string(content)
}

// Login входит паролем по умолчанию и запоминает сессию и csrf-токен.
func (p *Panel) Login() {
	p.t.Helper()

	p.LoginAs(defaultName, defaultPassword)
}

// LoginAs входит заданной парой и запоминает сессию и csrf-токен.
func (p *Panel) LoginAs(name, password string) {
	p.t.Helper()

	body, err := json.Marshal(map[string]string{"name": name, "password": password})
	if err != nil {
		p.t.Fatalf("тело входа: %v", err)
	}

	resp := p.Do(http.MethodPost, "/api/login", string(body), nil)
	if resp.Status != http.StatusOK {
		p.t.Fatalf("вход %q: код %d, ожидался 200; тело: %s", name, resp.Status, resp.Body)
	}

	var entered struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(resp.Body, &entered); err != nil {
		p.t.Fatalf("ответ о входе не разобран: %v; тело: %s", err, resp.Body)
	}
	if entered.CSRF == "" {
		p.t.Fatalf("панель не выдала csrf-токен: %s", resp.Body)
	}

	var session *http.Cookie
	for _, cookie := range resp.Cookies {
		if cookie.Name == "hysteria_ward_session" {
			session = cookie
		}
	}
	if session == nil {
		p.t.Fatalf("панель не выдала cookie сессии: %s", resp.Body)
	}

	p.session = session
	p.csrf = entered.CSRF
}

// Session — cookie вошедшего. nil означает «вход не выполнен».
func (p *Panel) Session() *http.Cookie {
	return p.session
}

// CSRF — csrf-токен вошедшего.
func (p *Panel) CSRF() string {
	return p.csrf
}

// Do отправляет запрос к панели. Cookie и csrf-токен подставляются сами, если
// сценарий уже вошёл; явные заголовки сильнее.
func (p *Panel) Do(method, path, body string, headers map[string]string) *Response {
	p.t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, p.baseURL+path, reader)
	if err != nil {
		p.t.Fatalf("%s %s: %v", method, path, err)
	}

	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.session != nil {
		req.AddCookie(p.session)
	}
	if p.csrf != "" && method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("X-CSRF-Token", p.csrf)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		p.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		p.t.Fatalf("тело ответа на %s %s: %v", method, path, err)
	}

	return &Response{
		Status:  resp.StatusCode,
		Header:  resp.Header,
		Cookies: resp.Cookies(),
		Body:    content,
	}
}

// Get спрашивает путь.
func (p *Panel) Get(path string) *Response {
	p.t.Helper()

	return p.Do(http.MethodGet, path, "", nil)
}

// Post отправляет JSON на путь.
func (p *Panel) Post(path, body string) *Response {
	p.t.Helper()

	return p.Do(http.MethodPost, path, body, nil)
}

// Decode разбирает тело ответа в value.
func (r *Response) Decode(t *testing.T, value any) {
	t.Helper()

	if err := json.Unmarshal(r.Body, value); err != nil {
		t.Fatalf("ответ не разобран: %v; тело: %s", err, r.Body)
	}
}

// Expect требует код ответа и объясняет отказ телом.
func (r *Response) Expect(t *testing.T, want int) *Response {
	t.Helper()

	if r.Status != want {
		t.Fatalf("код ответа = %d, ожидался %d; тело: %s", r.Status, want, r.Body)
	}

	return r
}

// freePort занимает и сразу освобождает порт: тесту нужен номер, который панель
// сможет занять сама. Фиксированный порт не годится — в CI его может занять
// что угодно, включая соседний тест.
func freePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("свободный порт не найден: %v", err)
	}
	defer func() { _ = listener.Close() }()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("неожиданный адрес слушателя: %T", listener.Addr())
	}

	return addr.Port
}
