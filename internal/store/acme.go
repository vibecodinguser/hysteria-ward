package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// keyACMEEmail — адрес, на который ACME заводит учётную запись и шлёт письма о
// сертификате. Он не нужен ядру, но нужен выпуску сертификата; поэтому лежит в
// appstate рядом с доменом, а не в конфигурации сервера.
var keyACMEEmail = []byte("acme_email")

// keyACME — способ проверки ACME и всё, что ему сопутствует. Email лежит
// отдельно: он появился раньше остальных настроек и уже есть в настроенных базах.
var keyACME = []byte("acme")

// ErrBadEmail — то, что ввели, не похоже на адрес электронной почты.
var ErrBadEmail = errors.New("недопустимый e-mail")

// ACMESettings — как панель получает сертификат: способ проверки, адрес ACME и
// настройки DNS-01.
type ACMESettings struct {
	// Mode — http (проверка по порту 80) или dns (запись TXT).
	Mode string `json:"mode"`

	// Directory — адрес ACME-каталога. Пустая строка означает Let's Encrypt;
	// подменяется для проверок на испытательном сервере ACME.
	Directory string `json:"directory"`

	// DNSProvider — hook (внешняя программа) или cloudflare. Пустое значение в
	// старых базах означает hook: миграция не нужна.
	DNSProvider string `json:"dns_provider,omitempty"`

	// DNSHook — программа, которая заводит и убирает записи TXT для DNS-01.
	DNSHook string `json:"dns_hook,omitempty"`

	// CloudflareAPIToken — секрет с правом Zone.DNS Edit (и Zone.Zone Read,
	// если зона ищется по имени). Он хранится в базе вместе с остальными
	// настройками и не выводится ни в интерфейс, ни в журнал.
	CloudflareAPIToken string `json:"cloudflare_api_token,omitempty"`

	// CloudflareZoneID — необязательный идентификатор зоны Cloudflare. Если
	// пуст, панель ищет зону по имени домена.
	CloudflareZoneID string `json:"cloudflare_zone_id,omitempty"`
}

// String печатает настройки без секрета. Так случайный лог или сообщение об
// ошибке не раскрывает токен Cloudflare.
func (s ACMESettings) String() string {
	token := "не задан"
	if s.CloudflareAPIToken != "" {
		token = "задан"
	}

	return fmt.Sprintf("ACMESettings{Mode:%s Directory:%s DNSProvider:%s DNSHook:%s CloudflareAPIToken:%s CloudflareZoneID:%s}",
		s.Mode, s.Directory, s.DNSProvider, s.DNSHook, token, s.CloudflareZoneID)
}

// DefaultACMESettings — выпуск по HTTP-01 и Let's Encrypt: работает на любом VPS
// без доступа к DNS-провайдеру.
func DefaultACMESettings() ACMESettings {
	return ACMESettings{Mode: "http"}
}

// withDefaults дополняет незаданное значениями по умолчанию: базу могла записать
// панель, которой этих настроек ещё не было.
func (s ACMESettings) withDefaults() ACMESettings {
	if s.Mode == "" {
		s.Mode = DefaultACMESettings().Mode
	}
	if s.Mode == "dns" && s.DNSProvider == "" {
		s.DNSProvider = "hook"
	}

	return s
}

// Validate проверяет, что выпуск настроен осмысленно.
//
// Существование хука не проверяется: без диска видно только форму ответа, а
// доступность программы выяснится при выпуске.
func (s ACMESettings) Validate() error {
	switch s.Mode {
	case "http":
		if s.DNSProvider != "" || s.DNSHook != "" || s.CloudflareAPIToken != "" || s.CloudflareZoneID != "" {
			return errors.New("для проверки http настройки DNS не нужны")
		}
	case "dns":
		provider := s.DNSProvider
		if provider == "" {
			provider = "hook"
		}

		switch provider {
		case "hook":
			if s.DNSHook == "" {
				return errors.New("для проверки dns с хуком нужна программа: без неё запись TXT заводить некому")
			}
			if s.CloudflareAPIToken != "" || s.CloudflareZoneID != "" {
				return errors.New("для проверки dns с хуком не нужны настройки Cloudflare")
			}
		case "cloudflare":
			if s.CloudflareAPIToken == "" {
				return errors.New("для проверки dns через Cloudflare нужен API-токен")
			}
			if s.DNSHook != "" {
				return errors.New("для проверки dns через Cloudflare не нужен хук")
			}
		default:
			return fmt.Errorf("неизвестный DNS-провайдер: %q", provider)
		}
	default:
		return fmt.Errorf("неизвестный способ проверки ACME: %q", s.Mode)
	}

	if s.Directory != "" {
		target, err := url.Parse(s.Directory)
		if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
			return fmt.Errorf("адрес ACME-каталога должен быть вида https://хост: %q", s.Directory)
		}
	}

	return nil
}

// ACMESettings возвращает настройки выпуска сертификата. Незаданные настройки —
// не ошибка: это новая или старая база.
func (s *Store) ACMESettings() (ACMESettings, error) {
	settings := DefaultACMESettings()

	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketAppState)
		if b == nil {
			return errors.New("нет бакета appstate")
		}

		raw := b.Get(keyACME)
		if raw == nil {
			return nil
		}

		if err := json.Unmarshal(raw, &settings); err != nil {
			return fmt.Errorf("разбор настроек ACME: %w", err)
		}

		settings = settings.withDefaults()

		return nil
	})
	if err != nil {
		return ACMESettings{}, err
	}

	if err := settings.Validate(); err != nil {
		return ACMESettings{}, fmt.Errorf("настройки ACME: %w", err)
	}

	return settings, nil
}

// SaveACMESettings записывает настройки выпуска.
func (s *Store) SaveACMESettings(settings ACMESettings) error {
	settings = settings.withDefaults()
	if err := settings.Validate(); err != nil {
		return fmt.Errorf("настройки ACME: %w", err)
	}

	raw, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("сериализация настроек ACME: %w", err)
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketAppState)
		if b == nil {
			return errors.New("нет бакета appstate")
		}

		if err := b.Put(keyACME, raw); err != nil {
			return fmt.Errorf("запись настроек ACME: %w", err)
		}

		return nil
	})
}

// SaveACMEEmail запоминает адрес для ACME. Пустая строка стирает его.
//
// Установщик пишет адрес вместе с остальными ответами одной транзакцией; метод
// нужен панели и тестам, когда адрес меняется отдельно.
func (s *Store) SaveACMEEmail(email string) error {
	email, err := normalizeACMEEmail(email)
	if err != nil {
		return err
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketAppState)
		if b == nil {
			return errors.New("нет бакета appstate")
		}

		if err := b.Put(keyACMEEmail, []byte(email)); err != nil {
			return fmt.Errorf("e-mail для ACME: %w", err)
		}

		return nil
	})
}

// ACMEEmail возвращает адрес для ACME. Пустая строка означает «не задан».
func (s *Store) ACMEEmail() (string, error) {
	var email string

	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketAppState)
		if b == nil {
			return errors.New("нет бакета appstate")
		}

		email = string(b.Get(keyACMEEmail))

		return nil
	})
	if err != nil {
		return "", err
	}

	return email, nil
}

// normalizeACMEEmail приводит адрес к виду, в котором он хранится.
//
// ACME принимает обычный почтовый адрес, а не строку с именем отправителя:
// display-name в настройках был бы принят за часть адреса и сломал бы запрос.
func normalizeACMEEmail(email string) (string, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return "", nil
	}

	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email {
		return "", fmt.Errorf("%w: %q — укажите адрес вида name@example.com", ErrBadEmail, email)
	}

	// ACME требует домен, а не локальное имя: a@localhost некуда отправить.
	at := strings.LastIndex(address.Address, "@")
	if at <= 0 || !strings.Contains(address.Address[at+1:], ".") {
		return "", fmt.Errorf("%w: %q — нужен полный адрес с доменом", ErrBadEmail, email)
	}

	return address.Address, nil
}
