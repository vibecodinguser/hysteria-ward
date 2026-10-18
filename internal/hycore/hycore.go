// Пакет hycore поднимает ядро Hysteria 2 внутри процесса панели.
//
// Это единственное место в проекте, знающее про core/v2 и extras/v2: наружу
// торчат только свои типы. Панель отдаёт ядру готовый сертификат и обработчики
// авторизации и учёта трафика, поэтому сама знает про каждого клиента всё
// напрямую, без разбора логов и опроса состояния чужой службы.
package hycore

import (
	"crypto/tls"
	"fmt"
	"net"

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
	TLS *tls.Config

	// ObfsPassword включает обфускацию salamander. Пустая строка — выключена.
	ObfsPassword string

	// Authenticator отвечает, кто стоит за строкой авторизации.
	Authenticator Authenticator

	// Traffic принимает сведения о трафике. Может быть nil, если учёт не нужен.
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
	core server.Server
	conn net.PacketConn
}

// Start поднимает ядро и начинает принимать подключения.
func Start(cfg Config) (*Server, error) {
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
	}
	if cfg.Traffic != nil {
		coreCfg.TrafficLogger = &trafficAdapter{next: cfg.Traffic}
	}

	core, err := server.NewServer(coreCfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ядро не поднялось: %w", err)
	}

	s := &Server{core: core, conn: packetConn}
	go func() { _ = s.core.Serve() }()

	return s, nil
}

// Addr возвращает адрес, который сервер фактически слушает.
func (s *Server) Addr() net.Addr {
	return s.conn.LocalAddr()
}

// Stop останавливает ядро и разрывает активные соединения.
func (s *Server) Stop() error {
	return s.core.Close()
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
func tlsConfigOf(cfg *tls.Config) server.TLSConfig {
	if cfg == nil {
		return server.TLSConfig{}
	}
	return server.TLSConfig{
		Certificates:   cfg.Certificates,
		GetCertificate: cfg.GetCertificate,
	}
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
