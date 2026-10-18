package api_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/api"
	"github.com/vibecodinguser/hysteria-ward/internal/certs"
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

	return api.Options{
		Listen:      "127.0.0.1:0",
		Certificate: certificate,
		Handler:     http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, response) }),
		Logger:      slog.New(slog.DiscardHandler),
	}
}

// server поднимает HTTPS-сервер панели на свободном адресе.
func server(t *testing.T) *api.Server {
	t.Helper()

	s, err := api.New(options(t))
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

// TestNewRequiresAddress — пустой адрес отвергается. Пустая строка в net.Listen
// означает прослушивание всех интерфейсов, то есть панель, выставленную наружу
// помимо воли администратора.
func TestNewRequiresAddress(t *testing.T) {
	opts := options(t)
	opts.Listen = ""

	if _, err := api.New(opts); err == nil {
		t.Fatal("New() с пустым адресом прошёл, ожидалась ошибка")
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
	opts.Listen = busy.Addr().String()

	if _, err := api.New(opts); err == nil {
		t.Fatal("New() на занятом адресе прошёл, ожидалась ошибка")
	}
}
