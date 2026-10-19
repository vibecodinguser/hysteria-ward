package store

import (
	"errors"
	"fmt"
	"net"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// keyDomain — домен, по которому клиенты подключаются к серверу.
var keyDomain = []byte("domain")

// ErrBadDomain — то, что ввели, не похоже на доменное имя. Отдельная ошибка, чтобы
// интерфейс показал причину, а не «внутренняя ошибка».
var ErrBadDomain = errors.New("недопустимый домен")

// ErrDomainNotSet — домен ещё не задан. Без него ключ клиента не собрать: в нём
// не будет ни адреса, ни SNI.
var ErrDomainNotSet = errors.New("домен сервера не задан")

// Domain возвращает домен сервера. Пустая строка означает «не задан».
func (s *Store) Domain() (string, error) {
	var domain string

	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketAppState)
		if b == nil {
			return errors.New("нет бакета appstate")
		}

		domain = string(b.Get(keyDomain))

		return nil
	})
	if err != nil {
		return "", err
	}

	return domain, nil
}

// SaveDomain запоминает домен сервера. Пустая строка стирает его.
//
// Домен — не адрес прослушивания: ядро слушает `:443` и не знает, под каким
// именем его видят клиенты. Это имя нужно ключам (адрес и SNI) и будущему
// сертификату, поэтому живёт рядом с настройками панели, а не в конфигурации
// сервера: смена домена не должна перезапускать ядро.
func (s *Store) SaveDomain(domain string) error {
	domain, err := normalizeDomain(domain)
	if err != nil {
		return err
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketAppState)
		if b == nil {
			return errors.New("нет бакета appstate")
		}

		if err := b.Put(keyDomain, []byte(domain)); err != nil {
			return fmt.Errorf("домен: %w", err)
		}

		return nil
	})
}

// normalizeDomain приводит домен к тому виду, в котором он хранится: без пробелов
// по краям, без схемы и порта, малыми буквами.
//
// Проверяется только форма записи: существует ли имя и указывает ли оно на этот
// сервер, выяснится при подключении клиента и при выпуске сертификата.
func normalizeDomain(domain string) (string, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return "", nil
	}

	switch {
	case strings.Contains(domain, "://"):
		return "", fmt.Errorf("%w: %q — укажите имя без схемы, например example.com", ErrBadDomain, domain)
	case strings.ContainsAny(domain, "/?#"):
		return "", fmt.Errorf("%w: %q — укажите имя без пути и параметров", ErrBadDomain, domain)
	case strings.Contains(domain, ":"):
		return "", fmt.Errorf("%w: %q — порт указывать не нужно, клиенты подключаются к 443", ErrBadDomain, domain)
	case strings.ContainsAny(domain, " \t"):
		return "", fmt.Errorf("%w: %q — в имени не бывает пробелов", ErrBadDomain, domain)
	case len(domain) > 253:
		return "", fmt.Errorf("%w: %q — длиннее 253 символов", ErrBadDomain, domain)
	}

	if net.ParseIP(domain) != nil {
		return domain, nil
	}

	for _, label := range strings.Split(domain, ".") {
		if err := checkLabel(label); err != nil {
			return "", fmt.Errorf("%w: %q — %v", ErrBadDomain, domain, err)
		}
	}

	return domain, nil
}

// checkLabel проверяет одну часть доменного имени.
func checkLabel(label string) error {
	if label == "" {
		return errors.New("пустая часть имени")
	}
	if len(label) > 63 {
		return errors.New("часть имени длиннее 63 символов")
	}

	for i, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0 && i < len(label)-1:
			// Дефис допустим внутри части, но не по краям.
		default:
			return fmt.Errorf("недопустимый символ %q", r)
		}
	}

	return nil
}
