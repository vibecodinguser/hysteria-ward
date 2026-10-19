package api_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/api"
	"github.com/vibecodinguser/hysteria-ward/internal/authn"
	"github.com/vibecodinguser/hysteria-ward/internal/certs"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// response — что отдаёт обработчик в тесте.
const response = "панель отвечает"

// options — заведомо годные варианты сервера. Тесты портят в них по одному полю:
// так видно, что сервер отвергает именно то, чего не хватает, а не что-нибудь
// другое.
func options(t *testing.T) api.Options {
	t.Helper()

	certificate, err := certs.SelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatalf("SelfSigned() = %v", err)
	}

	db := database(t)
	if err := db.SavePanelSettings(settings(t)); err != nil {
		t.Fatalf("SavePanelSettings() = %v", err)
	}

	return api.Options{
		Certificate: certificate,
		Handler:     http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, response) }),
		Auth:        authService(t, db),
		Store:       db,
		Logger:      slog.New(slog.DiscardHandler),
	}
}

// database открывает базу во временном каталоге: сервер и вход работают поверх
// одной и той же базы, как и в бою.
func database(t *testing.T) *store.Store {
	t.Helper()

	db, err := store.Open(filepath.Join(t.TempDir(), "hysteria-ward.db"))
	if err != nil {
		t.Fatalf("store.Open() = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
	})

	return db
}

// settings — настройки панели на свободном порту: панель в них слушает петлю, то
// есть снаружи её не видно.
//
// Порт берётся свободный, а не по умолчанию: тесты идут параллельно с рабочей
// панелью, и порт по умолчанию у них бы отобрали.
func settings(t *testing.T) store.PanelSettings {
	t.Helper()

	return store.PanelSettings{Access: store.AccessTunnel, Port: freePort(t)}
}

// freePort занимает и сразу освобождает порт: тесту нужен номер, который панель
// сможет занять сама.
func freePort(t *testing.T) int {
	t.Helper()

	return portOf(t, freeAddr(t))
}

// freeAddr занимает и сразу освобождает адрес.
func freeAddr(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("свободный порт не найден: %v", err)
	}
	defer listener.Close()

	return listener.Addr().String()
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

// authService собирает настоящий вход в панель поверх настоящей базы.
//
// Подделка здесь была бы хуже: вход в панель и есть то, что проверяют тесты
// сервера, — а проверять, что подделка согласна сама с собой, незачем.
func authService(t *testing.T, db *store.Store) *authn.Service {
	t.Helper()

	service, err := authn.New(db, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("authn.New() = %v", err)
	}

	// Пароль по умолчанию: тесты входят им, как это сделает администратор после
	// установки.
	if _, err := service.EnsureCredential(); err != nil {
		t.Fatalf("EnsureCredential() = %v", err)
	}

	return service
}

// server поднимает HTTPS-сервер панели на свободном адресе.
func server(t *testing.T) *api.Server {
	t.Helper()

	return start(t, options(t))
}

// start поднимает сервер по готовым вариантам и останавливает его вместе с тестом.
func start(t *testing.T, opts api.Options) *api.Server {
	t.Helper()

	s, err := api.New(opts)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() { _ = s.Run(ctx) }()

	return s
}

// client — клиент, который не проверяет сертификат: в тесте он самоподписанный.
func client() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

// TestServesHTTPS — панель отвечает по шифрованному каналу.
func TestServesHTTPS(t *testing.T) {
	s := server(t)

	resp, err := client().Get("https://" + s.Addr().String() + "/")
	if err != nil {
		t.Fatalf("запрос по HTTPS: %v", err)
	}
	defer resp.Body.Close()

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("тело ответа: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Errorf("код ответа = %d, ожидался 200", resp.StatusCode)
	}
	if string(content) != response {
		t.Errorf("тело ответа = %q, ожидалось %q", content, response)
	}
}

// TestDoesNotServePlainHTTP — запрос по открытому HTTP к тому же адресу не
// обслуживается: иначе панель можно было бы читать по дороге.
func TestDoesNotServePlainHTTP(t *testing.T) {
	s := server(t)

	resp, err := http.Get("http://" + s.Addr().String() + "/")
	if err != nil {
		t.Fatalf("запрос по открытому HTTP: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("код открытого запроса = %d, ожидался 400", resp.StatusCode)
	}
}

// TestStopsOnContextCancel — остановка по сигналу завершается штатно и даёт
// доработать начатому запросу.
//
// Это важнее, чем кажется: Run — единственное, за чем следит вызывающий, и как
// только он вернётся, журнал и база будут закрыты. Запрос, оборванный на
// полуслове или оставшийся писать в закрытый журнал, — это ответ, которого
// администратор не увидит.
func TestStopsOnContextCancel(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})

	opts := options(t)
	opts.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)

		time.Sleep(300 * time.Millisecond)
		fmt.Fprint(w, response)

		close(finished)
	})

	s, err := api.New(opts)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	body := make(chan string, 1)
	requestErr := make(chan error, 1)

	go func() {
		resp, err := client().Get("https://" + s.Addr().String() + "/")
		if err != nil {
			requestErr <- err
			return
		}
		defer resp.Body.Close()

		content, err := io.ReadAll(resp.Body)
		if err != nil {
			requestErr <- err
			return
		}

		body <- string(content)
	}()

	// Контекст отменяется посреди работы обработчика: иначе проверять нечего.
	select {
	case <-started:
	case err := <-requestErr:
		t.Fatalf("запрос не дошёл: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("запрос не дошёл до обработчика")
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() = %v, ожидалось штатное завершение", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("сервер не остановился после отмены контекста")
	}

	// Run уже вернулся: если обработчик к этому моменту не доработал, его оборвали.
	select {
	case <-finished:
	default:
		t.Error("Run вернулся раньше, чем доработал начатый запрос")
	}

	select {
	case got := <-body:
		if got != response {
			t.Errorf("тело ответа = %q, ожидалось %q", got, response)
		}
	case err := <-requestErr:
		t.Errorf("запрос не дошёл: %v", err)
	case <-time.After(5 * time.Second):
		t.Error("ответ не дошёл")
	}
}

// TestStopsWithoutWaitingForSilentConnection — сокет, который подключился и молчит,
// остановку не задерживает.
//
// Молчащие сокеты — обычное дело: сканер портов подключается и молчит. Если ждать
// каждого, панель на сигнал остановки отвечала бы секундами, а в журнал попадала
// бы жалоба на остановку, которой никто не мешал.
func TestStopsWithoutWaitingForSilentConnection(t *testing.T) {
	s, err := api.New(options(t))
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// Рукопожатие до конца, но без единого запроса: так у сервера точно есть это
	// соединение, и в нём точно нет запроса.
	silent, err := tls.Dial("tcp", s.Addr().String(), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("рукопожатие: %v", err)
	}
	defer silent.Close()

	started := time.Now()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() = %v, ожидалось штатное завершение", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("сервер не остановился")
	}

	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("остановка заняла %v, ожидалось меньше секунды", elapsed)
	}
}

// TestNewRequiresHandler — без обработчика серверу нечего отдавать.
func TestNewRequiresHandler(t *testing.T) {
	opts := options(t)
	opts.Handler = nil

	if _, err := api.New(opts); err == nil {
		t.Fatal("New() без обработчика прошёл, ожидалась ошибка")
	}
}

// TestNewRequiresLogger — журнал обязателен: о запуске и ошибках сервера должно
// быть куда написать.
func TestNewRequiresLogger(t *testing.T) {
	opts := options(t)
	opts.Logger = nil

	if _, err := api.New(opts); err == nil {
		t.Fatal("New() без журнала прошёл, ожидалась ошибка")
	}
}

// TestNewRequiresSettings — без настроек панели сервер не знает, кому слушать, а
// решил бы это за администратора и, скорее всего, в худшую сторону.
func TestNewRequiresSettings(t *testing.T) {
	opts := options(t)
	opts.Store = nil

	if _, err := api.New(opts); err == nil {
		t.Fatal("New() без настроек панели прошёл, ожидалась ошибка")
	}
}

// TestNewRequiresCertificate — без сертификата сервер поднялся бы, но не смог бы
// ответить ни одному клиенту: рукопожатие падает уже после того, как панель
// объявила себя работающей.
func TestNewRequiresCertificate(t *testing.T) {
	opts := options(t)
	opts.Certificate = tls.Certificate{}

	if _, err := api.New(opts); err == nil {
		t.Fatal("New() без сертификата прошёл, ожидалась ошибка")
	}
}

// TestNewReportsBusyAddress — занятый адрес виден сразу, а не при первом запросе.
func TestNewReportsBusyAddress(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() = %v", err)
	}
	defer busy.Close()

	opts := options(t)
	busySettings := settings(t)
	busySettings.Port = portOf(t, busy.Addr().String())
	if err := opts.Store.SavePanelSettings(busySettings); err != nil {
		t.Fatalf("SavePanelSettings() = %v", err)
	}

	if _, err := api.New(opts); err == nil {
		t.Fatal("New() на занятом адресе прошёл, ожидалась ошибка")
	}
}
