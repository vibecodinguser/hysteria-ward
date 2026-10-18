// Пакет hycore поднимает ядро Hysteria 2 внутри процесса панели.
//
// Это единственное место в проекте, знающее про core/v2 и extras/v2: наружу
// торчат только свои типы. Панель отдаёт ядру готовый сертификат и обработчики
// авторизации и учёта трафика, поэтому знает про каждого пользователя всё
// напрямую, без разбора логов и опроса состояния чужой службы.
package hycore

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"github.com/apernet/hysteria/core/v2/server"
	"github.com/apernet/hysteria/extras/v2/obfs"
)

// Config — всё, что нужно, чтобы поднять сервер.
type Config struct {
	// ListenAddr — адрес и порт для прослушивания. Порт 0 означает «возьми
	// свободный»: фактический адрес вернёт Addr.
	ListenAddr string

	// TLS — готовый сертификат из настроек панели. Ядро его не выпускает:
	// за сертификаты отвечает отдельная подсистема.
	TLS tls.Certificate

	// ObfsPassword включает обфускацию salamander. Пустая строка — выключена.
	ObfsPassword string

	// Authenticator отвечает, кто стоит за строкой авторизации.
	Authenticator Authenticator

	// Traffic принимает сведения о трафике. Обязателен: на нём стоит вся
	// статистика пользователей и разрыв по исчерпанной квоте.
	Traffic TrafficRecorder
}

// Authenticator сообщает, какому пользователю принадлежит строка авторизации,
// пришедшая от клиента. Разбор строки — забота реализации, а не ядра.
type Authenticator interface {
	Auth(auth string) (userID string, ok bool)
}

// TrafficRecorder принимает сведения о трафике и состоянии пользователей.
//
// Вызывается на каждый пакет, поэтому реализация обязана быть дешёвой.
// tx и rx — приращения за вызов, а не накопленные значения.
//
// Traffic возвращает false, чтобы ядро разорвало соединение пользователя:
// на этом стоит разрыв по исчерпанной квоте.
type TrafficRecorder interface {
	Traffic(userID string, tx, rx uint64) bool
	Online(userID string, online bool)
}

// Server — запущенное ядро.
type Server struct {
	core     server.Server
	conn     net.PacketConn
	done     chan error
	stopping atomic.Bool
	stopOnce sync.Once
}

// Start поднимает ядро и начинает принимать подключения.
func Start(cfg Config) (*Server, error) {
	if cfg.Authenticator == nil {
		return nil, errors.New("не задан источник пользователей")
	}
	if cfg.Traffic == nil {
		return nil, errors.New("не задан приёмник учёта трафика")
	}

	addr, err := net.ResolveUDPAddr("udp", cfg.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("разбор адреса %q: %w", cfg.ListenAddr, err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("сокет %q: %w", cfg.ListenAddr, err)
	}

	packetConn, err := obfuscate(conn, cfg.ObfsPassword)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	coreCfg := &server.Config{
		TLSConfig:     tlsConfigOf(cfg.TLS),
		Conn:          packetConn,
		Authenticator: &authAdapter{next: cfg.Authenticator},
		TrafficLogger: &trafficAdapter{next: cfg.Traffic},
	}

	core, err := server.NewServer(coreCfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ядро не поднялось: %w", err)
	}

	s := &Server{core: core, conn: packetConn, done: make(chan error, 1)}
	go func() {
		err := serveOnce(s.core)
		if s.stopping.Load() {
			err = nil
		}

		s.done <- err
		close(s.done)
	}()

	return s, nil
}

// Addr возвращает адрес, который сервер фактически слушает.
func (s *Server) Addr() net.Addr {
	return s.conn.LocalAddr()
}

// Done закрывается, когда ядро перестало обслуживать. Ошибка в канале — причина
// падения; nil, если сервер остановили через Stop.
func (s *Server) Done() <-chan error {
	return s.done
}

// Stop останавливает ядро и разрывает активные соединения. Повторный вызов
// безопасен: завершение уже остановленного сервера — не ошибка.
func (s *Server) Stop() error {
	s.stopping.Store(true)

	var err error
	s.stopOnce.Do(func() {
		err = s.core.Close()
	})

	return err
}

// serveOnce прогоняет Serve и превращает панику ядра в ошибку. У панели нет ни
// watchdog, ни супервизора: необработанная паника убила бы весь процесс вместе с
// интерфейсом, а так падение хотя бы попадёт в журнал.
func serveOnce(core server.Server) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("паника ядра: %v", r)
		}
	}()

	return core.Serve()
}

// obfuscate накрывает сокет обфускацией salamander. Пустой пароль означает, что
// обфускация выключена и сокет остаётся как есть.
func obfuscate(conn net.PacketConn, password string) (net.PacketConn, error) {
	if password == "" {
		return conn, nil
	}

	wrapped, err := obfs.WrapPacketConnSalamander(conn, []byte(password))
	if err != nil {
		return nil, fmt.Errorf("обфускация: %w", err)
	}

	return wrapped, nil
}

// tlsConfigOf переносит сертификат из настроек панели в словарь ядра.
func tlsConfigOf(cert tls.Certificate) server.TLSConfig {
	if cert.PrivateKey == nil {
		return server.TLSConfig{}
	}

	return server.TLSConfig{Certificates: []tls.Certificate{cert}}
}

// authAdapter приводит наш интерфейс к тому, что просит ядро.
type authAdapter struct {
	next Authenticator
}

func (a *authAdapter) Authenticate(_ net.Addr, auth string, _ uint64) (bool, string) {
	id, ok := a.next.Auth(auth)
	return ok, id
}

// trafficAdapter приводит наш приёмник к тому, что просит ядро. Слежение за
// отдельными потоками панели пока не нужно: объёмы приходят через LogTraffic.
type trafficAdapter struct {
	next TrafficRecorder
}

func (a *trafficAdapter) LogTraffic(id string, tx, rx uint64) bool {
	return a.next.Traffic(id, tx, rx)
}

func (a *trafficAdapter) LogOnlineState(id string, online bool) {
	a.next.Online(id, online)
}

func (a *trafficAdapter) TraceStream(_ server.HyStream, _ *server.StreamStats) {}

func (a *trafficAdapter) UntraceStream(_ server.HyStream) {}
