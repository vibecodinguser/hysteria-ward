// Пакет certs отвечает за сертификаты панели: самоподписанный для отладки и,
// дальше, выпущенный ACME для домена.
package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"time"
)

// selfSignedLifetime — на сколько выписывается отладочный сертификат. Он живёт до
// появления настоящего, поэтому срок небольшой.
const selfSignedLifetime = 30 * 24 * time.Hour

// clockSkew — запас назад по времени: часы сервера и браузера расходятся, и
// сертификат «из будущего» браузер отверг бы.
const clockSkew = time.Hour

// SelfSigned выписывает самоподписанный сертификат для перечисленных имён: имена
// хостов попадают в DNSNames, адреса — в IPAddresses. Пустые имена пропускаются.
//
// Такой сертификат браузер не признаёт, поэтому он годится только для отладочного
// запуска — посмотреть панель до того, как у неё появится домен. Подменять им
// настоящий сертификат нельзя.
func SelfSigned(hosts ...string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("ключ сертификата: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("номер сертификата: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName(hosts)},
		NotBefore:    now.Add(-clockSkew),
		NotAfter:     now.Add(selfSignedLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	for _, host := range hosts {
		if host == "" {
			continue
		}

		if ip := net.ParseIP(host); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
			continue
		}

		template.DNSNames = append(template.DNSNames, host)
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("выпуск сертификата: %w", err)
	}

	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("разбор выпущенного сертификата: %w", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
		Leaf:        leaf,
	}, nil
}

// commonName берёт первое непустое имя, а если имён нет — имя продукта: совсем без
// общего имени сертификат выглядит подозрительно для любого, кто на него смотрит.
func commonName(hosts []string) string {
	for _, host := range hosts {
		if host != "" {
			return host
		}
	}

	return "hysteria-ward"
}
