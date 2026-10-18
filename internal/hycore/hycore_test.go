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
	"fmt"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/apernet/hysteria/core/v2/client"
	coreErrs "github.com/apernet/hysteria/core/v2/errors"
	"github.com/apernet/hysteria/extras/v2/obfs"

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

// salamanderFactory даёт клиенту сокет с той же обфускацией, что и настоящий
// клиент Hysteria 2: без этого сервер, оборачивающий пакеты, клиента не поймёт.
type salamanderFactory struct {
	psk []byte
}

func (f salamanderFactory) New(net.Addr) (net.PacketConn, error) {
	conn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, err
	}

	return obfs.WrapPacketConnSalamander(conn, f.psk)
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

	conn := dial(t, srv, trustingClient("secret"), echoAddr)

	want := []byte("ping through hysteria")
	if got := roundTrip(t, conn, want); !bytes.Equal(got, want) {
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

	conn := dial(t, srv, trustingClient("secret"), echoAddr)

	payload := []byte("traffic must be counted")
	roundTrip(t, conn, payload)

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

// TestServerObfuscatesWithSalamander — сервер с включённой обфускацией работает
// с клиентом, который оборачивает пакеты тем же паролем. Обёрнут именно клиент,
// поэтому успех доказывает, что сервер пакеты и правда деобфусцирует.
func TestServerObfuscatesWithSalamander(t *testing.T) {
	echoAddr, closeEcho := startEchoServer(t)
	defer closeEcho()

	const password = "obfs-password"
	srv := startServer(t, hycore.Config{
		Authenticator: fakeAuth{"secret": "alice"},
		ObfsPassword:  password,
	})

	cfg := trustingClient("secret")
	cfg.ConnFactory = salamanderFactory{psk: []byte(password)}

	conn := dial(t, srv, cfg, echoAddr)

	want := []byte("hidden from DPI")
	if got := roundTrip(t, conn, want); !bytes.Equal(got, want) {
		t.Errorf("через прокси вернулось %q, ожидалось %q", got, want)
	}
}

// TestStartWithoutCertificateFails — сервер не выпускает сертификат сам: если
// сертификат не настроен, он не поднимается. Иначе панель молча жила бы с
// самодельным сертификатом, которому клиенты не поверят.
func TestStartWithoutCertificateFails(t *testing.T) {
	srv, err := hycore.Start(hycore.Config{
		ListenAddr:    "127.0.0.1:0",
		Authenticator: fakeAuth{"secret": "alice"},
	})
	if err == nil {
		_ = srv.Stop()
		t.Fatal("сервер поднялся без сертификата, ожидалась ошибка")
	}
}

// TestServerListensOnGivenPort — сервер слушает именно тот порт, который задан
// в настройках. Порт 0 в остальных тестах этого не проверяет: он одинаково
// подошёл бы и серверу, который настройку игнорирует.
func TestServerListensOnGivenPort(t *testing.T) {
	port := freeUDPPort(t)

	srv := startServer(t, hycore.Config{
		ListenAddr:    fmt.Sprintf("127.0.0.1:%d", port),
		Authenticator: fakeAuth{"secret": "alice"},
	})

	addr, ok := srv.Addr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("Addr() = %T, ожидался *net.UDPAddr", srv.Addr())
	}
	if addr.Port != port {
		t.Errorf("сервер слушает порт %d, ожидался %d", addr.Port, port)
	}
}

// freeUDPPort находит свободный UDP-порт. Между освобождением и запуском сервера
// порт теоретически может занять кто-то ещё, но в тестовой среде это редкость.
func freeUDPPort(t *testing.T) int {
	t.Helper()

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("не нашёлся свободный порт: %v", err)
	}
	defer conn.Close()

	return conn.LocalAddr().(*net.UDPAddr).Port
}

// TestStartWithoutAuthenticatorFails — панель обязана сказать, кто такой
// пользователь. Без этого первый же клиент уронил бы ядро, причём в отдельной
// горутине, то есть панель бы даже не поняла, что случилось.
func TestStartWithoutAuthenticatorFails(t *testing.T) {
	srv, err := hycore.Start(hycore.Config{
		ListenAddr: "127.0.0.1:0",
		TLS:        testTLSConfig(),
	})
	if err == nil {
		_ = srv.Stop()
		t.Fatal("сервер поднялся без источника пользователей, ожидалась ошибка")
	}
}

// TestStartWithoutTrafficRecorderFails — учёт трафика обязателен. Без него
// панель не узнала бы ни о подключениях, ни о расходах: статистика была бы
// пустой, а квоты не работали бы вовсе.
func TestStartWithoutTrafficRecorderFails(t *testing.T) {
	srv, err := hycore.Start(hycore.Config{
		ListenAddr:    "127.0.0.1:0",
		TLS:           testTLSConfig(),
		Authenticator: fakeAuth{"secret": "alice"},
	})
	if err == nil {
		_ = srv.Stop()
		t.Fatal("сервер поднялся без учёта трафика, ожидалась ошибка")
	}
}

// TestClientWithoutTrustedCertificateFails — контроль к предыдущему: без нашего
// сертификата в корнях клиент не подключается. Если бы тесты отключали проверку
// сертификата, этот сценарий прошёл бы и ничего не доказывал.
func TestClientWithoutTrustedCertificateFails(t *testing.T) {
	srv := startServer(t, hycore.Config{
		Authenticator: fakeAuth{"secret": "alice"},
	})

	c, _, err := client.NewClient(&client.Config{
		ServerAddr: srv.Addr(),
		Auth:       "secret",
		TLSConfig:  client.TLSConfig{ServerName: testServerName},
	})
	if err == nil {
		_ = c.Close()
		t.Fatal("клиент подключился, не доверяя сертификату сервера, ожидалась ошибка")
	}
}

// startServer поднимает сервер и убирает его за собой. Тесты задают только то,
// что относится к их сценарию: адрес и сертификат подставляются по умолчанию.
func startServer(t *testing.T, cfg hycore.Config) *hycore.Server {
	t.Helper()

	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:0"
	}
	if cfg.TLS == nil {
		cfg.TLS = testTLSConfig()
	}
	if cfg.Traffic == nil {
		cfg.Traffic = newFakeTraffic()
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

// roundTrip отправляет байты через соединение и возвращает ответ цели.
func roundTrip(t *testing.T, conn net.Conn, payload []byte) []byte {
	t.Helper()

	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("запись через прокси: %v", err)
	}

	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("чтение через прокси: %v", err)
	}

	return got
}

// testServerName — имя в сертификате и в настройках клиента: по нему клиент
// убеждается, что говорит именно с нашим сервером.
const testServerName = "hysteria-ward.test"

// testCert — самоподписанный сертификат, общий для всех тестов: серверу нужно
// что предъявить, а клиенту — чем проверить. Проверку сертификата тесты не
// отключают, иначе не заметили бы, что сервер подсунул собственный.
var testCert = sync.OnceValue(func() testCertificate {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic("не сгенерировался ключ тестового сертификата: " + err.Error())
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: testServerName},
		DNSNames:     []string{testServerName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic("не собрался тестовый сертификат: " + err.Error())
	}

	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		panic("не разобрался тестовый сертификат: " + err.Error())
	}

	return testCertificate{
		tls:  tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf},
		leaf: leaf,
	}
})

// testCertificate — тестовый сертификат и он же в роли корневого.
type testCertificate struct {
	tls  tls.Certificate
	leaf *x509.Certificate
}

// testTLSConfig — настройки сервера с тестовым сертификатом.
func testTLSConfig() *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{testCert().tls}}
}

// trustingClient — клиент, который доверяет тестовому сертификату и потому
// проверяет его всерьёз.
func trustingClient(auth string) client.Config {
	roots := x509.NewCertPool()
	roots.AddCert(testCert().leaf)

	return client.Config{
		Auth: auth,
		TLSConfig: client.TLSConfig{
			RootCAs:    roots,
			ServerName: testServerName,
		},
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
