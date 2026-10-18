// Пакет api поднимает HTTPS-сервер панели.
//
// Только шифрованный канал: открытый HTTP на том же порту не обслуживается,
// поэтому подсмотреть пароль администратора по дороге негде.
package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

const (
	// shutdownTimeout — сколько сервер ждёт завершения начатых запросов при
	// остановке.
	shutdownTimeout = 5 * time.Second

	// readHeaderTimeout — предел на чтение заголовков: без него одно медленное
	// соединение держит горутину сколько угодно.
	readHeaderTimeout = 10 * time.Second

	// idleTimeout — сколько держать простаивающее соединение.
	idleTimeout = time.Minute
)

// Options — что слушать и что отдавать.
type Options struct {
	// Listen — адрес панели.
	Listen string

	// Certificate — сертификат для HTTPS.
	Certificate tls.Certificate

	// Handler — что отдавать.
	Handler http.Handler

	// Logger — куда писать о запуске, остановке и ошибках сервера.
	Logger *slog.Logger
}

// Server — HTTPS-сервер панели.
type Server struct {
	listener net.Listener
	http     *http.Server
	logger   *slog.Logger
}

// New занимает адрес и готовит сервер.
//
// Адрес занимается сразу, а не при первом запросе: занятый порт и битый
// сертификат должны быть видны до того, как панель начнёт что-то делать.
func New(opts Options) (*Server, error) {
	if opts.Handler == nil {
		return nil, errors.New("не задан обработчик")
	}
	if opts.Logger == nil {
		return nil, errors.New("не задан журнал")
	}

	listener, err := net.Listen("tcp", opts.Listen)
	if err != nil {
		return nil, fmt.Errorf("адрес %q не занят: %w", opts.Listen, err)
	}

	server := &Server{
		listener: listener,
		logger:   opts.Logger,
		http: &http.Server{
			Handler:           opts.Handler,
			ErrorLog:          slog.NewLogLogger(opts.Logger.Handler(), slog.LevelError),
			ReadHeaderTimeout: readHeaderTimeout,
			IdleTimeout:       idleTimeout,
			TLSConfig: &tls.Config{
				Certificates: []tls.Certificate{opts.Certificate},
				MinVersion:   tls.VersionTLS12,
			},
		},
	}

	opts.Logger.Info("интерфейс открыт", "addr", listener.Addr().String())

	return server, nil
}

// Addr — адрес, который слушает панель.
func (s *Server) Addr() net.Addr {
	return s.listener.Addr()
}

// Run обслуживает запросы, пока не отменят контекст.
//
// Начатые запросы успевают завершиться: остановка по сигналу не должна рвать
// ответ администратору.
func (s *Server) Run(ctx context.Context) error {
	finished := make(chan struct{})

	go func() {
		select {
		case <-ctx.Done():
			graceful, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()

			if err := s.http.Shutdown(graceful); err != nil {
				s.logger.Error("интерфейс не остановился по-хорошему", "err", err)
			}
		case <-finished:
		}
	}()

	// Сертификат уже лежит в TLSConfig, поэтому имена файлов пусты.
	err := s.http.ServeTLS(s.listener, "", "")
	close(finished)

	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return fmt.Errorf("обслуживание запросов: %w", err)
}
