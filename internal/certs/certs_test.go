package certs_test

import (
	"net"
	"slices"
	"testing"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/certs"
)

// TestSelfSignedCoversHosts — отладочный сертификат должен подходить для того
// адреса, по которому открывают панель: имена хостов в DNSNames, адреса в
// IPAddresses.
func TestSelfSignedCoversHosts(t *testing.T) {
	certificate, err := certs.SelfSigned("localhost", "127.0.0.1", "panel.example.com", "")
	if err != nil {
		t.Fatalf("SelfSigned() = %v", err)
	}

	if certificate.Leaf == nil {
		t.Fatal("сертификат не разобран: Leaf пуст")
	}

	names := certificate.Leaf.DNSNames
	addresses := certificate.Leaf.IPAddresses

	for _, host := range []string{"localhost", "panel.example.com"} {
		if !slices.Contains(names, host) {
			t.Errorf("в DNSNames нет %q: %v", host, names)
		}
	}

	if !slices.ContainsFunc(addresses, func(ip net.IP) bool { return ip.String() == "127.0.0.1" }) {
		t.Errorf("в IPAddresses нет 127.0.0.1: %v", addresses)
	}

	if slices.Contains(names, "") {
		t.Errorf("в DNSNames попало пустое имя: %v", names)
	}

	if certificate.Leaf.IsCA {
		t.Error("сертификат помечен как удостоверяющий центр, хотя это обычный сертификат сервера")
	}
}

// TestSelfSignedSignsItself — подпись сделана своим же ключом: иначе сертификат
// не был бы самоподписанным и ни на что не годился бы.
func TestSelfSignedSignsItself(t *testing.T) {
	certificate, err := certs.SelfSigned("localhost")
	if err != nil {
		t.Fatalf("SelfSigned() = %v", err)
	}

	leaf := certificate.Leaf
	if err := leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature); err != nil {
		t.Errorf("подпись не сходится: %v", err)
	}
}

// TestSelfSignedLivesLongEnough — сертификат выписывается на срок с запасом назад
// по времени: расхождение часов не должно его убивать.
func TestSelfSignedLivesLongEnough(t *testing.T) {
	certificate, err := certs.SelfSigned("localhost")
	if err != nil {
		t.Fatalf("SelfSigned() = %v", err)
	}

	leaf := certificate.Leaf
	if !leaf.NotBefore.Before(time.Now()) {
		t.Errorf("NotBefore = %v, ожидалось раньше текущего времени", leaf.NotBefore)
	}
	if leaf.NotAfter.Before(time.Now().AddDate(0, 0, 7)) {
		t.Errorf("NotAfter = %v, ожидался срок не меньше недели", leaf.NotAfter)
	}
}

// TestSelfSignedWithoutHosts — сертификат выпускается даже без имён: отладочный
// запуск не должен падать из-за пустой настройки.
func TestSelfSignedWithoutHosts(t *testing.T) {
	certificate, err := certs.SelfSigned()
	if err != nil {
		t.Fatalf("SelfSigned() = %v", err)
	}

	if certificate.Leaf.Subject.CommonName == "" {
		t.Error("у сертификата нет общего имени")
	}
}
