package hycore_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/apernet/hysteria/core/v2/client"
	coreErrs "github.com/apernet/hysteria/core/v2/errors"

	"github.com/vibecodinguser/hysteria-ward/internal/hycore"
)

// fakeAuth — подмена панели: отвечает, кто стоит за строкой авторизации.
type fakeAuth map[string]string

func (f fakeAuth) Auth(auth string) (string, bool) {
	id, ok := f[auth]
	return id, ok
}

// fakeTraffic — подмена панели: запоминает, что сервер сообщил об учёте.
// Вызовы идут из разных горутин, поэтому счётчики под замком.
type fakeTraffic struct {
	mu     sync.Mutex
	totals map[string]*trafficTotals
}

// trafficTotals — накопленное по одному пользователю: объёмы и признак online.
type trafficTotals struct {
	tx, rx uint64
	online bool
}

func newFakeTraffic() *fakeTraffic {
	return &fakeTraffic{totals: make(map[string]*trafficTotals)}
}

func (f *fakeTraffic) Traffic(userID string, tx, rx uint64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	t := f.entry(userID)
	t.tx += tx
	t.rx += rx

	return true
}

func (f *fakeTraffic) Online(userID string, online bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.entry(userID).online = online
}

func (f *fakeTraffic) entry(userID string) *trafficTotals {
	t, ok := f.totals[userID]
	if !ok {
		t = &trafficTotals{}
		f.totals[userID] = t
	}

	return t
}

func (f *fakeTraffic) snapshot(userID string) trafficTotals {
	f.mu.Lock()
	defer f.mu.Unlock()

	if t, ok := f.totals[userID]; ok {
		return *t
	}

	return trafficTotals{}
}

// TestServerProxiesTCPToTarget — сквозной сценарий задачи: клиент с верным
// паролем подключается к серверу и получает через него ответ от цели.
// Клиент здесь берётся из ядра и живёт только в тесте: продукт клиентом не
// управляет, он лишь играет роль настоящего клиента Hysteria 2.
func TestServerProxiesTCPToTarget(t *testing.T) {
	echoAddr, closeEcho := startEchoServer(t)
	defer closeEcho()

	srv := startServer(t, hycore.Config{
		Authenticator: fakeAuth{"secret": "alice"},
	})

	conn := dial(t, srv, insecureClient("secret"), echoAddr)

	want := []byte("ping through hysteria")
	if _, err := conn.Write(want); err != nil {
		t.Fatalf("запись через прокси: %v", err)
	}

	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("чтение через прокси: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("через прокси вернулось %q, ожидалось %q", got, want)
	}
}

// TestServerRejectsUnknownPassword — неизвестный пароль не проходит: наш
// Authenticator спрашивают, и его отказ доходит до клиента.
func TestServerRejectsUnknownPassword(t *testing.T) {
	srv := startServer(t, hycore.Config{
		Authenticator: fakeAuth{"secret": "alice"},
	})

	c, _, err := client.NewClient(&client.Config{
		ServerAddr: srv.Addr(),
		Auth:       "wrong",
		TLSConfig:  client.TLSConfig{InsecureSkipVerify: true},
	})
	if err == nil {
		_ = c.Close()
		t.Fatal("клиент с неизвестным паролем подключился, ожидался отказ")
	}

	// Именно отказ авторизации, а не любая другая ошибка: иначе тест проходил
	// бы и на неработающем сервере.
	var authErr coreErrs.AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("клиент получил %v, ожидался отказ авторизации", err)
	}
}

// TestServerReportsTrafficPerUser — сервер сообщает панели, кто подключился и
// сколько байт прошло. На этом стоит вся статистика пользователей и разрыв по
// исчерпанной квоте.
func TestServerReportsTrafficPerUser(t *testing.T) {
	echoAddr, closeEcho := startEchoServer(t)
	defer closeEcho()

	traffic := newFakeTraffic()
	srv := startServer(t, hycore.Config{
		Authenticator: fakeAuth{"secret": "alice"},
		Traffic:       traffic,
	})

	conn := dial(t, srv, insecureClient("secret"), echoAddr)

	payload := []byte("traffic must be counted")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("запись через прокси: %v", err)
	}
	if _, err := io.ReadFull(conn, make([]byte, len(payload))); err != nil {
		t.Fatalf("чтение через прокси: %v", err)
	}

	// Учёт приходит асинхронно: байты проходят через ядро уже после того, как
	// их увидел тест.
	waitFor(t, "признак online для alice", func() bool {
		return traffic.snapshot("alice").online
	})
	waitFor(t, "учёт байт для alice", func() bool {
		got := traffic.snapshot("alice")
		return got.tx+got.rx >= uint64(len(payload))
	})
}

// startServer поднимает сервер и убирает его за собой. Тесты задают только то,
// что относится к их сценарию: адрес и сертификат подставляются по умолчанию.
func startServer(t *testing.T, cfg hycore.Config) *hycore.Server {
	t.Helper()

	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:0"
	}
	if cfg.TLS == nil {
		cfg.TLS = testTLSConfig(t)
	}

	srv, err := hycore.Start(cfg)
	if err != nil {
		t.Fatalf("Start() = %v, ожидался запуск сервера", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })

	return srv
}

// dial поднимает клиента к нашему серверу и открывает через него TCP до цели.
func dial(t *testing.T, srv *hycore.Server, cfg client.Config, target string) net.Conn {
	t.Helper()

	cfg.ServerAddr = srv.Addr()
	c, _, err := client.NewClient(&cfg)
	if err != nil {
		t.Fatalf("клиент не подключился к серверу: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	conn, err := c.TCP(target)
	if err != nil {
		t.Fatalf("TCP() = %v, ожидалось соединение через прокси", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

// insecureClient — клиент, который не проверяет сертификат: в тестах он
// самоподписанный.
func insecureClient(auth string) client.Config {
	return client.Config{
		Auth:      auth,
		TLSConfig: client.TLSConfig{InsecureSkipVerify: true},
	}
}

// waitFor ждёт, пока условие выполнится, иначе роняет тест.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("не дождались: %s", what)
}

// startEchoServer поднимает TCP-сервер, возвращающий всё, что получил.
func startEchoServer(t *testing.T) (addr string, closeFn func()) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo-сервер не поднялся: %v", err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	return ln.Addr().String(), func() { _ = ln.Close() }
}

// testTLSConfig собирает самоподписанный сертификат прямо в памяти. Сертификат
// нужен только тесту: сервер обязан брать его из настроек, а не выпускать сам.
func testTLSConfig(t *testing.T) *tls.Config {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ключ не сгенерировался: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "hysteria-ward-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("сертификат не собрался: %v", err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}
}
