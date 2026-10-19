// Пакет keys собирает клиентский ключ — ссылку hysteria2://, по которой клиент
// подключается к серверу.
//
// Ссылка собирается заново при каждом запросе, а не хранится: всё, что в ней есть,
// уже лежит в настройках сервера, а копия рано или поздно разошлась бы с ними.
package keys

import (
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Scheme — схема ссылки Hysteria 2.
const Scheme = "hysteria2"

// Params — всё, что нужно знать о сервере, кроме пароля клиента.
type Params struct {
	// Domain — имя, по которому клиенты видят сервер. Оно же идёт в SNI.
	Domain string

	// Listen — адрес ядра: из него берётся порт. Сам адрес в ссылку не идёт:
	// клиенты подключаются к домену, а не к петле.
	Listen string

	// ObfsPassword включает обфускацию salamander. Пустой — выключена.
	ObfsPassword string

	// Certificate — сертификат сервера, из него считается отпечаток для пиннинга.
	Certificate tls.Certificate
}

// URI собирает ссылку для пользователя.
//
// В ссылке есть всё, что нужно клиенту и чего у него нет: адрес с портом, пароль,
// SNI, обфускация и отпечаток сертификата. Без отпечатка ключ оставался бы
// рабочим и после подмены сертификата — а подменённый сертификат означает чужой
// сервер посередине.
func URI(p Params, password string) (string, error) {
	domain := strings.TrimSpace(p.Domain)
	if domain == "" {
		return "", errors.New("домен сервера не задан")
	}

	port, err := portOf(p.Listen)
	if err != nil {
		return "", err
	}

	pin, err := Pin(p.Certificate)
	if err != nil {
		return "", err
	}

	host := domain
	if port != "" && port != "443" {
		host = net.JoinHostPort(domain, port)
	}

	query := url.Values{}
	query.Set("sni", domain)
	query.Set("pinSHA256", pin)

	if p.ObfsPassword != "" {
		query.Set("obfs", "salamander")
		query.Set("obfs-password", p.ObfsPassword)
	}

	uri := &url.URL{
		Scheme:   Scheme,
		User:     url.User(password),
		Host:     host,
		Path:     "/",
		RawQuery: query.Encode(),
	}

	return uri.String(), nil
}

// Pin считает отпечаток сертификата для pinSHA256.
//
// Это SHA-256 всего сертификата в том виде, в каком его предъявляют, — так его
// считает и клиент Hysteria, и `openssl x509 -fingerprint -sha256`. Формат
// отпечатка — тот же, что у openssl: байты через двоеточие, большими буквами.
// Регистр и двоеточия клиент всё равно отбрасывает, но администратору привычнее
// так.
func Pin(cert tls.Certificate) (string, error) {
	if len(cert.Certificate) == 0 {
		return "", errors.New("отпечаток: нет сертификата")
	}

	sum := sha256.Sum256(cert.Certificate[0])

	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}

	return strings.Join(parts, ":"), nil
}

// portOf достаёт порт из адреса прослушивания. Пустой порт — не ошибка: в ссылке
// его просто не будет, и клиент возьмёт 443.
func portOf(listen string) (string, error) {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("адрес сервера %q: %w", listen, err)
	}

	return port, nil
}
