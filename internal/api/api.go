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
	"sync"
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

	// mu защищает silent — соединения, которые подключились, но ещё ничего не
	// сказали, — и stopping: остановку панели.
	mu       sync.Mutex
	silent   map[net.Conn]struct{}
	stopping bool
}

// New занимает адрес и готовит сервер.
//
// Адрес занимается сразу, а не при первом запросе: занятый порт должно быть видно
// до того, как панель начнёт что-то делать. Пустой адрес отвергается отдельно — он
// означал бы прослушивание всех интерфейсов, а это не то, что панель имеет право
// решить за администратора.
func New(opts Options) (*Server, error) {
	if opts.Handler == nil {
		return nil, errors.New("не задан обработчик")
	}
	if opts.Logger == nil {
		return nil, errors.New("не задан журнал")
	}
	if opts.Listen == "" {
		return nil, errors.New("не задан адрес панели")
	}
	if len(opts.Certificate.Certificate) == 0 {
		// Без сертификата сервер поднялся бы, но не смог бы ответить ни одному
		// клиенту: рукопожатие падает уже после того, как панель объявила себя
		// работающей.
		return nil, errors.New("не задан сертификат")
	}

	listener, err := net.Listen("tcp", opts.Listen)
	if err != nil {
		return nil, fmt.Errorf("не удалось занять адрес %q: %w", opts.Listen, err)
	}

	server := &Server{
		listener: listener,
		logger:   opts.Logger,
		silent:   make(map[net.Conn]struct{}),
	}
	server.http = &http.Server{
		Handler:           opts.Handler,
		ConnState:         server.trackConn,
		ErrorLog:          slog.NewLogLogger(opts.Logger.Handler(), slog.LevelError),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{opts.Certificate},
			MinVersion:   tls.VersionTLS12,
		},
	}

	opts.Logger.Info("интерфейс открыт", "addr", listener.Addr().String())

	return server, nil
}

// Addr — адрес, который слушает панель.
func (s *Server) Addr() net.Addr {
	return s.listener.Addr()
}

// trackConn берёт на заметку соединения, которые подключились, но ещё не сказали
// ни слова: при остановке их нужно закрыть самим.
func (s *Server) trackConn(c net.Conn, state http.ConnState) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if state != http.StateNew {
		delete(s.silent, c)
		return
	}

	if s.stopping {
		// Панель уже останавливается: запроса в этом соединении никто не ждёт, а
		// ждать его — лишние секунды к остановке.
		_ = c.Close()
		return
	}

	s.silent[c] = struct{}{}
}

// closeSilent закрывает соединения, в которых не было ни одного запроса, и дальше
// не даёт появиться новым.
//
// Без этого панель останавливалась бы не сразу: остановка соединения, которое
// молчит, ждёт пять секунд — Go считает такое соединение живым, потому что в нём
// ещё может начаться запрос. А молчащих хватает: любой сканер портов подключается
// и молчит. Запрос в таком соединении не выполняется, ждать нечего.
func (s *Server) closeSilent() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.stopping = true

	for c := range s.silent {
		_ = c.Close()
		delete(s.silent, c)
	}
}

// Run обслуживает запросы, пока не отменят контекст.
//
// Начатые запросы успевают завершиться: ServeTLS возвращается сразу после закрытия
// слушателя, а Shutdown в это время ещё ждёт обработчиков, поэтому Run дожидается
// его — иначе вызывающий закрыл бы журнал и базу у запроса под руками.
//
// Срыв по-хорошему остановки Run ошибкой не считает: он уходит в журнал. Наружу
// возвращается только отказ самого обслуживания.
func (s *Server) Run(ctx context.Context) error {
	stopped := make(chan struct{})
	served := make(chan struct{})

	go func() {
		defer close(served)

		select {
		case <-ctx.Done():
		case <-stopped:
		}

		// Молчащие соединения закрываются первыми, иначе ожидание начатых
		// запросов растянется на них.
		s.closeSilent()

		graceful, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := s.http.Shutdown(graceful); err != nil {
			s.logger.Error("интерфейс не остановился по-хорошему", "err", err)
		}
	}()

	// Сертификат уже лежит в TLSConfig, поэтому имена файлов пусты.
	err := s.http.ServeTLS(s.listener, "", "")
	close(stopped)
	<-served

	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return fmt.Errorf("обслуживание запросов: %w", err)
}
