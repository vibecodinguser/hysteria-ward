package api_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/api"
	"github.com/vibecodinguser/hysteria-ward/internal/certs"
)

// response — что отдаёт обработчик в тесте.
const response = "панель отвечает"

// server поднимает HTTPS-сервер панели на свободном адресе и возвращает его
// вместе с кодом завершения Run.
func server(t *testing.T) (*api.Server, <-chan error) {
	t.Helper()

	certificate, err := certs.SelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatalf("SelfSigned() = %v", err)
	}

	s, err := api.New(api.Options{
		Listen:      "127.0.0.1:0",
		Certificate: certificate,
		Handler:     http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, response) }),
		Logger:      slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	t.Cleanup(cancel)

	return s, done
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
	s, _ := server(t)

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
	s, _ := server(t)

	resp, err := http.Get("http://" + s.Addr().String() + "/")
	if err != nil {
		// Отказ в соединении — тоже отказ обслуживать открытый запрос.
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Errorf("запрос по HTTP обслужился кодом %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Status, "400") {
		t.Errorf("код открытого запроса = %q, ожидалась ошибка запроса", resp.Status)
	}
}

// TestStopsOnContextCancel — остановка по сигналу завершается штатно, без
// сообщения об ошибке.
func TestStopsOnContextCancel(t *testing.T) {
	certificate, err := certs.SelfSigned("localhost")
	if err != nil {
		t.Fatalf("SelfSigned() = %v", err)
	}

	s, err := api.New(api.Options{
		Listen:      "127.0.0.1:0",
		Certificate: certificate,
		Handler:     http.NotFoundHandler(),
		Logger:      slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// Начатый запрос должен успеть завершиться.
	resp, err := client().Get("https://" + s.Addr().String() + "/")
	if err != nil {
		t.Fatalf("запрос по HTTPS: %v", err)
	}
	resp.Body.Close()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() = %v, ожидалось штатное завершение", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("сервер не остановился после отмены контекста")
	}
}

// TestNewRequiresHandler — без обработчика серверу нечего отдавать.
func TestNewRequiresHandler(t *testing.T) {
	_, err := api.New(api.Options{Listen: "127.0.0.1:0", Logger: slog.New(slog.DiscardHandler)})
	if err == nil {
		t.Fatal("New() без обработчика прошёл, ожидалась ошибка")
	}
}

// TestNewRequiresLogger — журнал обязателен: о запуске и ошибках сервера должно
// быть куда написать.
func TestNewRequiresLogger(t *testing.T) {
	_, err := api.New(api.Options{Listen: "127.0.0.1:0", Handler: http.NotFoundHandler()})
	if err == nil {
		t.Fatal("New() без журнала прошёл, ожидалась ошибка")
	}
}

// TestNewReportsBusyAddress — занятый адрес виден сразу, а не при первом запросе.
func TestNewReportsBusyAddress(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() = %v", err)
	}
	defer busy.Close()

	certificate, err := certs.SelfSigned("localhost")
	if err != nil {
		t.Fatalf("SelfSigned() = %v", err)
	}

	_, err = api.New(api.Options{
		Listen:      busy.Addr().String(),
		Certificate: certificate,
		Handler:     http.NotFoundHandler(),
		Logger:      slog.New(slog.DiscardHandler),
	})
	if err == nil {
		t.Fatal("New() на занятом адресе прошёл, ожидалась ошибка")
	}
}
