package hycore_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/apernet/hysteria/core/v2/client"

	"github.com/vibecodinguser/hysteria-ward/internal/hycore"
)

// fakeAuth — подмена панели: отвечает, кто стоит за строкой авторизации.
type fakeAuth map[string]string

func (f fakeAuth) Auth(auth string) (string, bool) {
	id, ok := f[auth]
	return id, ok
}

// TestServerProxiesTCPToTarget — сквозной сценарий задачи: клиент с верным
// паролем подключается к серверу и получает через него ответ от цели.
// Клиент здесь берётся из ядра и живёт только в тесте: продукт клиентом не
// управляет, он лишь играет роль настоящего клиента Hysteria 2.
func TestServerProxiesTCPToTarget(t *testing.T) {
	echoAddr, closeEcho := startEchoServer(t)
	defer closeEcho()

	srv, err := hycore.Start(hycore.Config{
		ListenAddr:    "127.0.0.1:0",
		TLS:           testTLSConfig(t),
		Authenticator: fakeAuth{"secret": "alice"},
	})
	if err != nil {
		t.Fatalf("Start() = %v, ожидался запуск сервера", err)
	}
	defer srv.Stop()

	c, _, err := client.NewClient(&client.Config{
		ServerAddr: srv.Addr(),
		Auth:       "secret",
		TLSConfig:  client.TLSConfig{InsecureSkipVerify: true},
	})
	if err != nil {
		t.Fatalf("клиент не подключился к серверу: %v", err)
	}
	defer c.Close()

	conn, err := c.TCP(echoAddr)
	if err != nil {
		t.Fatalf("TCP() = %v, ожидалось соединение через прокси", err)
	}
	defer conn.Close()

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
