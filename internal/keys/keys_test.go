package keys_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/keys"
)

// certificate выпускает самоподписанный сертификат: отпечаток считается по нему,
// и другого источника для теста не нужно.
func certificate(t *testing.T) tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ключ: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "example.com"},
		DNSNames:     []string{"example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("сертификат: %v", err)
	}

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// params — настройки сервера для теста: домен, штатный порт, обфускация.
func params(t *testing.T, obfs string) keys.Params {
	t.Helper()

	return keys.Params{
		Domain:       "example.com",
		Listen:       ":443",
		ObfsPassword: obfs,
		Certificate:  certificate(t),
	}
}

// TestURIContainsEverythingNeeded — в ключе есть адрес, пароль, SNI, обфускация и
// отпечаток: всё, что клиенту нужно, чтобы подключиться, и всё, чем сервер
// отличается от подделки.
func TestURIContainsEverythingNeeded(t *testing.T) {
	p := params(t, "gawrgura")

	uri, err := keys.URI(p, "s3cret")
	if err != nil {
		t.Fatalf("URI() = %v", err)
	}

	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("ссылка %q не разбирается: %v", uri, err)
	}

	if parsed.Scheme != "hysteria2" {
		t.Errorf("схема = %q, ожидалась hysteria2", parsed.Scheme)
	}
	if parsed.User.Username() != "s3cret" {
		t.Errorf("пароль = %q, ожидался s3cret", parsed.User.Username())
	}
	if _, hasPassword := parsed.User.Password(); hasPassword {
		t.Error("пароль попал в пару user:password — клиент прочитает это как userpass")
	}
	// Порт 443 по умолчанию, поэтому в адресе его нет: так короче и так же
	// работает.
	if parsed.Host != "example.com" {
		t.Errorf("адрес = %q, ожидался example.com", parsed.Host)
	}

	query := parsed.Query()
	if got := query.Get("sni"); got != "example.com" {
		t.Errorf("sni = %q, ожидался example.com", got)
	}
	if got := query.Get("obfs"); got != "salamander" {
		t.Errorf("obfs = %q, ожидался salamander", got)
	}
	if got := query.Get("obfs-password"); got != "gawrgura" {
		t.Errorf("obfs-password = %q, ожидался gawrgura", got)
	}
	if got := query.Get("pinSHA256"); got == "" {
		t.Error("в ключе нет отпечатка сертификата")
	}
}

// TestURIOmitsObfsWhenOff — без обфускации её параметров в ключе нет: клиент,
// получивший obfs=salamander с пустым паролем, не подключился бы.
func TestURIOmitsObfsWhenOff(t *testing.T) {
	uri, err := keys.URI(params(t, ""), "s3cret")
	if err != nil {
		t.Fatalf("URI() = %v", err)
	}

	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("ссылка %q не разбирается: %v", uri, err)
	}

	if got := parsed.Query().Get("obfs"); got != "" {
		t.Errorf("obfs = %q, ожидалась пустая строка", got)
	}
	if got := parsed.Query().Get("obfs-password"); got != "" {
		t.Errorf("obfs-password = %q, ожидалась пустая строка", got)
	}
}

// TestURIKeepsUnusualPort — нештатный порт попадает в адрес: клиент должен идти
// туда, где сервер действительно слушает.
func TestURIKeepsUnusualPort(t *testing.T) {
	p := params(t, "")
	p.Listen = "0.0.0.0:8443"

	uri, err := keys.URI(p, "s3cret")
	if err != nil {
		t.Fatalf("URI() = %v", err)
	}

	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("ссылка %q не разбирается: %v", uri, err)
	}

	if parsed.Host != "example.com:8443" {
		t.Errorf("адрес = %q, ожидался example.com:8443", parsed.Host)
	}
}

// TestURIEscapesPassword — пароль может содержать что угодно: в ссылке он
// экранируется, а клиент читает его обратно без потерь.
func TestURIEscapesPassword(t *testing.T) {
	password := "пароль/с:спец@символами?и#решёткой"

	uri, err := keys.URI(params(t, ""), password)
	if err != nil {
		t.Fatalf("URI() = %v", err)
	}

	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("ссылка %q не разбирается: %v", uri, err)
	}

	if got := parsed.User.Username(); got != password {
		t.Errorf("пароль = %q, ожидался %q", got, password)
	}
	if _, hasPassword := parsed.User.Password(); hasPassword {
		t.Error("пароль с двоеточием разобран как пара user:password")
	}
}

// TestURIRequiresDomain — без домена ключ собрать нельзя: адрес и SNI взять
// неоткуда, а ссылка с пустым адресом — это ссылка в никуда.
func TestURIRequiresDomain(t *testing.T) {
	p := params(t, "")
	p.Domain = "  "

	if _, err := keys.URI(p, "s3cret"); err == nil {
		t.Fatal("URI() без домена = nil, ожидалась ошибка")
	}
}

// TestURIRejectsBrokenListen — испорченный адрес сервера виден сразу, а не
// превращается в ключ без порта.
func TestURIRejectsBrokenListen(t *testing.T) {
	p := params(t, "")
	p.Listen = "443"

	if _, err := keys.URI(p, "s3cret"); err == nil {
		t.Fatal("URI() с адресом без порта = nil, ожидалась ошибка")
	}
}

// TestPinMatchesCertificate — отпечаток считается по сертификату и записан так,
// как его понимает клиент Hysteria: регистр и двоеточия он отбрасывает.
func TestPinMatchesCertificate(t *testing.T) {
	cert := certificate(t)

	pin, err := keys.Pin(cert)
	if err != nil {
		t.Fatalf("Pin() = %v", err)
	}

	if !strings.Contains(pin, ":") {
		t.Errorf("отпечаток %q записан без двоеточий", pin)
	}
	if pin != strings.ToUpper(pin) {
		t.Errorf("отпечаток %q записан не большими буквами", pin)
	}

	// Так же нормализует отпечаток клиент Hysteria.
	normalized := strings.ReplaceAll(strings.ToLower(pin), ":", "")
	sum := sha256.Sum256(cert.Certificate[0])
	if normalized != hex.EncodeToString(sum[:]) {
		t.Errorf("отпечаток = %q, ожидался SHA-256 сертификата", pin)
	}
}

// TestPinRequiresCertificate — у пустого сертификата отпечатка нет, и придумывать
// его нельзя: клиент получил бы ключ, который никого не проверяет.
func TestPinRequiresCertificate(t *testing.T) {
	if _, err := keys.Pin(tls.Certificate{}); err == nil {
		t.Fatal("Pin() без сертификата = nil, ожидалась ошибка")
	}
}
