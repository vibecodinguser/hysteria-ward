// Пакет api поднимает HTTPS-сервер панели.
//
// Только шифрованный канал: открытый HTTP на том же порту не обслуживается,
// поэтому подсмотреть пароль администратора по дороге негде.
package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/authn"
	"github.com/vibecodinguser/hysteria-ward/internal/build"
	"github.com/vibecodinguser/hysteria-ward/internal/certs"
	"github.com/vibecodinguser/hysteria-ward/internal/mcp"
	"github.com/vibecodinguser/hysteria-ward/internal/metrics"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
	"github.com/vibecodinguser/hysteria-ward/internal/users"
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

	// maxRequestBody — предел на тело запроса: панель принимает только короткие
	// команды вроде имени с паролем или режима доступа.
	maxRequestBody = 4 << 10
)

// Options — что слушать и что отдавать.
type Options struct {
	// Certificate — сертификат для HTTPS. В бою вместо него передаётся
	// Certificates: сертификат меняется на ходу, при продлении.
	Certificate tls.Certificate

	// Certificates — держатель сертификата, который умеет меняться без
	// перезапуска панели. Необязателен: тесты и статический запуск обходятся
	// Certificate.
	Certificates *certs.Manager

	// Background — работа, которая идёт всё время жизни панели, например выпуск
	// и продление сертификата. Необязательна.
	Background func(context.Context)

	// Handler — что отдавать после входа: интерфейс панели.
	Handler http.Handler

	// Auth — вход в панель. Обязателен: панель, в которую пускают без пароля, —
	// это не панель.
	Auth *authn.Service

	// Store — база панели: из неё сервер берёт, кому и на каком порту слушать, и
	// в ней же запоминает смену режима.
	Store *store.Store

	// Users — пользователи сервера: кто подключён и кого пускать. Обязательны:
	// без них интерфейс показывал бы список, который ни на что не влияет.
	Users *users.Registry

	// Metrics — трафик и его история. Обязательны: обзор без них показывал бы
	// пустоту вместо того, что через сервер на самом деле прошло.
	Metrics *metrics.Collector

	// CoreRunning сообщает, работает ли ядро. Необязателен: без него панель
	// честно отвечает, что ядро не работает.
	CoreRunning func() bool

	// Apply перезапускает ядро с конфигурацией из базы. Обязателен: без него
	// кнопка «Применить» обещала бы действие, которого нет.
	Apply func() error

	// Logger — куда писать о запуске, остановке и ошибках сервера.
	Logger *slog.Logger
}

// Server — HTTPS-сервер панели.
type Server struct {
	http   *http.Server
	logger *slog.Logger

	// auth, store, users, metrics и coreRunning — то, чем панель отвечает на
	// запросы: вход, настройки, пользователи, трафик и состояние ядра.
	auth         *authn.Service
	store        *store.Store
	users        *users.Registry
	metrics      *metrics.Collector
	certificate  tls.Certificate
	certificates *certs.Manager
	background   func(context.Context)
	coreRunning  func() bool
	apply        func() error
	mcp          *mcp.Handler

	// stopped закрывается при остановке панели. Потоки метрик живут дольше
	// обычного запроса, и без этого сигнала остановка ждала бы их до конца
	// соединения.
	stopped chan struct{}

	// switchMu выстраивает смены режима доступа в очередь: две команды сразу
	// перевешивали бы слушатель друг у друга из-под рук.
	switchMu sync.Mutex

	// mu защищает слушатель и настройки панели, а ещё — silent: соединения,
	// которые подключились, но ещё ничего не сказали, — и stopping: остановку.
	mu       sync.Mutex
	settings store.PanelSettings
	listener net.Listener
	silent   map[net.Conn]struct{}
	stopping bool
}

// New занимает адрес и готовит сервер.
//
// Адрес занимается сразу, а не при первом запросе: занятый порт должно быть видно
// до того, как панель начнёт что-то делать. Кому слушать, решают настройки
// панели, а не сеть: по умолчанию это петля, и снаружи панель не видна.
func New(opts Options) (*Server, error) {
	if opts.Handler == nil {
		return nil, errors.New("не задан обработчик")
	}
	if opts.Logger == nil {
		return nil, errors.New("не задан журнал")
	}
	if opts.Store == nil {
		return nil, errors.New("не заданы настройки панели")
	}
	if opts.Auth == nil {
		return nil, errors.New("не задан вход в панель")
	}
	if opts.Users == nil {
		return nil, errors.New("не заданы пользователи сервера")
	}
	if opts.Metrics == nil {
		return nil, errors.New("не задан учёт трафика")
	}
	if opts.Apply == nil {
		return nil, errors.New("не задано применение конфигурации")
	}
	if len(opts.Certificate.Certificate) == 0 && opts.Certificates == nil {
		// Без сертификата сервер поднялся бы, но не смог бы ответить ни одному
		// клиенту: рукопожатие падает уже после того, как панель объявила себя
		// работающей.
		return nil, errors.New("не задан сертификат")
	}

	settings, err := opts.Store.PanelSettings()
	if err != nil {
		return nil, err
	}

	listener, err := listen(settings)
	if err != nil {
		return nil, err
	}

	server := &Server{
		settings:     settings,
		listener:     listener,
		logger:       opts.Logger,
		auth:         opts.Auth,
		store:        opts.Store,
		users:        opts.Users,
		metrics:      opts.Metrics,
		certificate:  opts.Certificate,
		certificates: opts.Certificates,
		background:   opts.Background,
		coreRunning:  opts.CoreRunning,
		apply:        opts.Apply,
		silent:       make(map[net.Conn]struct{}),
		stopped:      make(chan struct{}),
	}

	mcpHandler, err := mcp.New(mcp.Options{
		Authenticate: func(token string) (bool, error) {
			_, ok, err := opts.Store.AuthenticateAPIKey(token)

			return ok, err
		},
		Tools:         server.mcpTools(),
		Call:          server.callMCPTool,
		ServerName:    build.Name,
		ServerVersion: build.Current().Version,
		Logger:        opts.Logger,
	})
	if err != nil {
		_ = listener.Close()

		return nil, err
	}
	server.mcp = mcpHandler

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if opts.Certificates != nil {
		// Сертификат берётся при каждом рукопожатии: продление подменяет его без
		// перезапуска.
		tlsConfig.GetCertificate = opts.Certificates.GetCertificate
	} else {
		tlsConfig.Certificates = []tls.Certificate{opts.Certificate}
	}

	server.http = &http.Server{
		Handler:           server.routes(opts.Handler),
		ConnState:         server.trackConn,
		ErrorLog:          slog.NewLogLogger(opts.Logger.Handler(), slog.LevelError),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		TLSConfig:         tlsConfig,
	}

	opts.Logger.Info("интерфейс открыт", "addr", settings.Addr(), "access", string(settings.Access))

	return server, nil
}

// listen занимает адрес панели по настройкам.
func listen(settings store.PanelSettings) (net.Listener, error) {
	listener, err := net.Listen("tcp", settings.Addr())
	if err != nil {
		return nil, fmt.Errorf("не удалось занять адрес %q: %w", settings.Addr(), err)
	}

	return listener, nil
}

// routes собирает поверхность панели: вход и то, что доступно вошедшему, — по
// своим путям под /api/, всё остальное — интерфейс.
//
// Интерфейс отдаётся и без входа: иначе браузер не показал бы и формы входа. Ни
// данных, ни состояния панели он не содержит — они за /api/, куда без входа не
// пускают.
func (s *Server) routes(panel http.Handler) http.Handler {
	mux := http.NewServeMux()

	// Вход не защищён сессией: её ещё нет, её и заводят.
	mux.HandleFunc("POST /api/login", s.handleLogin)

	// Проверка работоспособности открыта: по ней ждут готовности панели, а не
	// получают доступ к её состоянию.
	mux.HandleFunc("GET /api/health", s.handleHealth)

	mux.HandleFunc("POST /api/logout", s.protect(s.handleLogout))
	mux.HandleFunc("GET /api/session", s.protect(s.handleSession))
	mux.HandleFunc("GET /api/status", s.protect(s.handleStatus))
	mux.HandleFunc("GET /api/panel", s.protect(s.handlePanel))
	mux.HandleFunc("POST /api/panel/access", s.protect(s.handleSetAccess))

	mux.HandleFunc("GET /api/server", s.protect(s.handleServer))
	mux.HandleFunc("PUT /api/server", s.protect(s.handleSetServer))
	mux.HandleFunc("POST /api/server/apply", s.protect(s.handleApplyServer))

	mux.HandleFunc("GET /api/certificate", s.protect(s.handleCertificate))
	mux.HandleFunc("POST /api/certificate/issue", s.protect(s.handleIssueCertificate))

	mux.HandleFunc("GET /api/users", s.protect(s.handleUsers))
	mux.HandleFunc("POST /api/users", s.protect(s.handleCreateUser))
	mux.HandleFunc("PATCH /api/users/{id}", s.protect(s.handleUpdateUser))
	mux.HandleFunc("DELETE /api/users/{id}", s.protect(s.handleDeleteUser))
	mux.HandleFunc("POST /api/users/{id}/quota/reset", s.protect(s.handleResetQuota))
	mux.HandleFunc("GET /api/users/{id}/key", s.protect(s.handleUserKey))
	mux.HandleFunc("GET /api/users/{id}/qr", s.protect(s.handleUserQR))
	mux.HandleFunc("POST /api/users/{id}/password", s.protect(s.handleNewPassword))

	mux.HandleFunc("GET /api/metrics", s.protect(s.handleMetrics))
	mux.HandleFunc("GET /api/metrics/history", s.protect(s.handleMetricsHistory))

	// Поток живых значений: одно соединение вместо опроса.
	mux.HandleFunc("GET /api/events", s.protect(s.handleEvents))

	// MCP — отдельная поверхность для агентов, со своим ключом. Cookie сессии
	// здесь не принимается: ключ не должен превращаться в административный вход.
	mux.Handle("/mcp", s.mcp)

	mux.HandleFunc("GET /api/mcp/keys", s.protect(s.handleMCPKeys))
	mux.HandleFunc("POST /api/mcp/keys", s.protect(s.handleCreateMCPKey))
	mux.HandleFunc("DELETE /api/mcp/keys/{id}", s.protect(s.handleDeleteMCPKey))

	// Незнакомый путь внутри /api/ — ошибка, а не страница интерфейса.
	mux.HandleFunc("/api/", s.handleUnknownAPI)

	// Всё остальное — интерфейс; он сам разбирает свой маршрут на клиенте.
	mux.Handle("/", panel)

	return mux
}

// Addr — адрес, который слушает панель. nil означает, что слушателя нет: панель
// осталась без адреса, и Run скажет об этом ошибкой.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.listener == nil {
		return nil
	}

	return s.listener.Addr()
}

// address — адрес панели по её настройкам. Журнал и интерфейс показывают его, а не
// то, как записала адрес операционная система: «все интерфейсы» — это 0.0.0.0, а не
// [::], и администратору нужно второе.
func (s *Server) address() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.settings.Addr()
}

// currentCertificate отдаёт сертификат, который панель предъявляет сейчас. Ключи
// собираются по нему же: отпечаток в ключе должен совпадать с тем сертификатом,
// который клиент увидит при подключении.
func (s *Server) currentCertificate() tls.Certificate {
	if s.certificates != nil {
		return s.certificates.Current()
	}

	return s.certificate
}

// Access — режим доступа, в котором панель слушает сейчас.
func (s *Server) Access() store.Access {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.settings.Access
}

// SetAccess переводит панель в другой режим доступа, не перезапуская её.
//
// Начатые запросы при этом не рвутся — в том числе тот, которым пришла сама
// команда: закрывается только дверь для новых, а открытые соединения живут
// дальше. Если новый адрес занять не удалось, панель остаётся там, где была:
// отказ не должен оставлять её без слушателя вовсе.
func (s *Server) SetAccess(access store.Access) error {
	if err := access.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopping {
		return errors.New("панель останавливается")
	}
	if s.settings.Access == access {
		return nil
	}

	previous := s.settings
	next := previous
	next.Access = access

	if err := s.switchLocked(next); err != nil {
		return err
	}

	s.settings = next

	if access == store.AccessPublic {
		s.logger.Warn("панель открыта наружу: её видит кто угодно, кто станет перебирать адреса",
			"addr", next.Addr())
	} else {
		s.logger.Info("панель снова доступна только через SSH-туннель", "addr", next.Addr())
	}

	return nil
}

// switchLocked перевешивает слушатель на адрес из настроек: прежний закрывается,
// новый занимается на его месте. Вызывается под mu.
func (s *Server) switchLocked(settings store.PanelSettings) error {
	previous := s.settings

	// Закрывается только слушатель: соединения, по которым уже идут запросы,
	// закрытие слушателя не трогает.
	if s.listener != nil {
		if err := s.listener.Close(); err != nil {
			return fmt.Errorf("закрытие слушателя: %w", err)
		}
	}

	listener, err := listen(settings)
	if err != nil {
		// Прежний адрес возвращается обратно: панель без слушателя хуже, чем
		// панель в прежнем режиме.
		restored, restoreErr := listen(previous)
		if restoreErr != nil {
			s.listener = nil
			return errors.Join(err, fmt.Errorf("прежний адрес не вернулся: %w", restoreErr))
		}

		s.listener = restored

		return err
	}

	s.listener = listener

	return nil
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

	// Потоки метрик не привязаны к запросу и сами не закончатся: их закрывает
	// остановка, иначе Shutdown ждал бы их до конца соединения.
	close(s.stopped)

	for c := range s.silent {
		_ = c.Close()
		delete(s.silent, c)
	}
}

// Run обслуживает запросы, пока не отменят контекст.
//
// Смена режима доступа перевешивает слушатель на ходу, поэтому обслуживание идёт
// циклом: закрытый ради смены слушатель — не повод считать, что панель
// остановилась.
//
// Начатые запросы успевают завершиться: ServeTLS возвращается сразу после закрытия
// слушателя, а Shutdown в это время ещё ждёт обработчиков, поэтому Run дожидается
// его — иначе вызывающий закрыл бы журнал и базу у запроса под руками.
//
// Срыв по-хорошему остановки Run ошибкой не считает: он уходит в журнал. Наружу
// возвращается только отказ самого обслуживания.
func (s *Server) Run(ctx context.Context) error {
	shut := make(chan struct{})
	var once sync.Once

	stop := func() {
		once.Do(func() {
			// Молчащие соединения закрываются первыми, иначе ожидание начатых
			// запросов растянется на них.
			s.closeSilent()

			graceful, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()

			if err := s.http.Shutdown(graceful); err != nil {
				s.logger.Error("интерфейс не остановился по-хорошему", "err", err)
			}

			close(shut)
		})
	}

	// Отмена контекста — та же остановка.
	stopAfter := context.AfterFunc(ctx, stop)
	defer stopAfter()

	// Выпуск сертификата идёт рядом с обслуживанием: панель не должна ждать
	// ACME, чтобы открыться.
	if s.background != nil {
		go s.background(ctx)
	}

	for {
		s.mu.Lock()
		listener := s.listener
		s.mu.Unlock()

		if listener == nil {
			// Так бывает, если новый адрес не занялся и прежний не вернулся:
			// обслуживать нечего, и делать вид, что панель работает, нельзя.
			return errors.New("панель осталась без адреса")
		}

		// Сертификат уже лежит в TLSConfig, поэтому имена файлов пусты.
		err := s.http.ServeTLS(listener, "", "")

		if s.switched(listener) {
			// Слушатель сменили ради нового режима: обслуживаем новый.
			continue
		}

		stop()
		<-shut

		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return fmt.Errorf("обслуживание запросов: %w", err)
	}
}

// switched сообщает, сменился ли слушатель с того, который обслуживался.
//
// Остановку смена не путает: Shutdown слушатель не подменяет, а только закрывает.
func (s *Server) switched(served net.Listener) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.listener != served
}

// decodeBody разбирает тело запроса с пределом на размер.
func decodeBody(w http.ResponseWriter, r *http.Request, value any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(value)
}
