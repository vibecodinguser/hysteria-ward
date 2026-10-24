package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	bolt "go.etcd.io/bbolt"
	"golang.org/x/net/publicsuffix"
)

// Настройки панели — то, что панель знает о себе самой: кому она показывает себя,
// на каком порту и под каким именем её открывают. Лежат в appstate, а не в config:
// config описывает сервер Hysteria 2, а это настройки самой панели.

const (
	// defaultPanelPort — порт панели по умолчанию.
	defaultPanelPort = 8443

	// minPort и maxPort — границы порта. Ноль означал бы «выбери сам», то есть не
	// настройку, а её отсутствие.
	minPort = 1
	maxPort = 65535
)

// keyPanel — ключ, под которым настройки панели лежат в appstate.
var keyPanel = []byte("panel")

// Access — кому панель показывает себя.
type Access string

const (
	// AccessTunnel — только тому, кто на самом сервере или пробросил порт через
	// SSH-туннель: панель слушает петлю, снаружи её не видно.
	AccessTunnel Access = "tunnel"

	// AccessPublic — всем. Панель становится видна сканерам, поэтому такой режим
	// включают явным действием, а не получают по умолчанию.
	AccessPublic Access = "public"
)

// PanelMode — где живёт имя панели.
type PanelMode string

const (
	// PanelModeServer — панель открывается по домену сервера. Подходит для
	// SSH-туннеля: снаружи панель не слушает, а имя нужно для сертификата.
	PanelModeServer PanelMode = "server"

	// PanelModeSubdomain — панель открывается по отдельному имени, по умолчанию
	// panel.<домен>. Это режим по умолчанию: так панель работала до появления
	// выбора.
	PanelModeSubdomain PanelMode = "subdomain"
)

// Host — кого слушать в этом режиме.
//
// Публичный режим слушает все интерфейсы: у VPS адрес может смениться, и
// привязаться к сегодняшнему значило бы однажды остаться без панели.
func (a Access) Host() string {
	if a == AccessPublic {
		return "0.0.0.0"
	}

	return "127.0.0.1"
}

// PanelSettings — настройки панели: кому слушать, на каком порту и под каким
// именем открываться.
//
// Пустой Mode — не ошибка, а запись старой панели: до появления выбора режима
// домен панели выводился из домена сервера. Такая запись читается как
// subdomain, кроме домена, который сам начинается с panel.: его прежнее
// поведение — панель на том же имени. Новый установщик всегда пишет Mode явно.
type PanelSettings struct {
	Access Access    `json:"access"`
	Port   int       `json:"port"`
	Mode   PanelMode `json:"mode,omitempty"`
	Domain string    `json:"domain,omitempty"`
}

// DefaultPanelSettings — настройки новой панели: наружу она не выставлена, а
// домен панели по умолчанию — panel.<домен>.
func DefaultPanelSettings() PanelSettings {
	return PanelSettings{
		Access: AccessTunnel,
		Port:   defaultPanelPort,
		Mode:   PanelModeSubdomain,
	}
}

// Addr — адрес, который панель занимает при таких настройках.
func (p PanelSettings) Addr() string {
	return net.JoinHostPort(p.Access.Host(), strconv.Itoa(p.Port))
}

// Host возвращает имя, по которому открывается панель.
//
// В режиме server это домен сервера. В режиме subdomain — явно заданный домен
// или panel.<домен>. Для старой записи без Mode действует прежнее правило:
// домен, начинающийся с panel., уже считается именем панели.
func (p PanelSettings) Host(serverDomain string) string {
	serverDomain = strings.ToLower(strings.TrimSpace(serverDomain))
	if serverDomain == "" {
		return ""
	}

	switch p.Mode {
	case PanelModeServer:
		return serverDomain
	case PanelModeSubdomain:
		if domain := strings.ToLower(strings.TrimSpace(p.Domain)); domain != "" {
			return domain
		}

		return "panel." + serverDomain
	}

	// Старая запись: Mode ещё не было. Если домен сервера сам начинается с
	// panel., второго имени не появляется — иначе вышло бы panel.panel.<домен>.
	if p.Domain == "" && strings.HasPrefix(serverDomain, "panel.") {
		return serverDomain
	}
	if domain := strings.ToLower(strings.TrimSpace(p.Domain)); domain != "" {
		return domain
	}

	return "panel." + serverDomain
}

// Hosts перечисляет имена одного сертификата: домен сервера и имя панели.
// Дубликаты убираются, поэтому режим server даёт одно имя.
func (p PanelSettings) Hosts(serverDomain string) []string {
	serverDomain = strings.ToLower(strings.TrimSpace(serverDomain))
	if serverDomain == "" {
		return nil
	}

	panelDomain := p.Host(serverDomain)
	if panelDomain == "" || panelDomain == serverDomain {
		return []string{serverDomain}
	}

	return []string{serverDomain, panelDomain}
}

// effectiveMode возвращает режим с учётом записи старой панели.
func (p PanelSettings) effectiveMode(serverDomain string) PanelMode {
	if p.Mode != "" {
		return p.Mode
	}

	serverDomain = strings.ToLower(strings.TrimSpace(serverDomain))
	if p.Domain == "" && strings.HasPrefix(serverDomain, "panel.") {
		return PanelModeServer
	}

	return PanelModeSubdomain
}

// Validate проверяет, что настройки годны к работе. Связь с доменом сервера
// проверяет ValidateFor: без него нельзя понять, допустим ли выбранный режим.
func (p PanelSettings) Validate() error {
	if err := p.Access.Validate(); err != nil {
		return err
	}

	if p.Port < minPort || p.Port > maxPort {
		return fmt.Errorf("порт панели: %d вне границ %d—%d", p.Port, minPort, maxPort)
	}

	switch p.Mode {
	case "", PanelModeServer, PanelModeSubdomain:
	default:
		return fmt.Errorf("режим домена панели: неизвестное значение %q", p.Mode)
	}

	if p.Mode == PanelModeServer {
		if p.Domain != "" {
			return errors.New("в режиме server домен панели не задаётся: он совпадает с доменом сервера")
		}

		return nil
	}

	if p.Domain == "" {
		return nil
	}

	domain, err := normalizeDomain(p.Domain)
	if err != nil {
		return fmt.Errorf("домен панели: %w", err)
	}
	if net.ParseIP(domain) != nil {
		return fmt.Errorf("домен панели: %q — для ACME нужен домен, а не IP-адрес", domain)
	}
	if !strings.Contains(domain, ".") {
		return fmt.Errorf("домен панели: %q — нужно имя вида panel.example.com", domain)
	}

	return nil
}

// ValidateFor проверяет настройки вместе с доменом сервера.
func (p PanelSettings) ValidateFor(serverDomain string) error {
	if err := p.Validate(); err != nil {
		return err
	}

	serverDomain, err := normalizeDomain(serverDomain)
	if err != nil {
		return fmt.Errorf("домен сервера: %w", err)
	}
	if serverDomain == "" {
		return errors.New("домен сервера не задан")
	}

	// У IP-адреса нет registrable domain, и правило «панель в том же базовом
	// домене» к нему неприменимо. Старые установки на адресе не ломаем: ACME для
	// IP всё равно не выписать, но панель должна запускаться как раньше. Запрет
	// публичной панели на домене сервера остаётся: он не про базовый домен.
	if net.ParseIP(serverDomain) != nil {
		if p.Access == AccessPublic && p.effectiveMode(serverDomain) == PanelModeServer {
			return errors.New("публичная панель не может жить на домене сервера: выберите отдельный поддомен")
		}

		return nil
	}

	host := p.Host(serverDomain)
	mode := p.effectiveMode(serverDomain)

	if mode == PanelModeSubdomain {
		if host == serverDomain {
			return errors.New("в режиме subdomain домен панели должен отличаться от домена сервера")
		}
		if !sameRegistrableDomain(serverDomain, host) {
			return fmt.Errorf("домен панели %q должен быть в том же базовом домене, что и %q", host, serverDomain)
		}
	}

	if p.Access == AccessPublic && host == serverDomain {
		// Старая база с доменом panel.* жила так до появления режимов, и
		// ломать её при обновлении нельзя. Новый установщик server + public
		// не создаёт: там режим всегда записан явно.
		if p.Mode == "" && p.Domain == "" && strings.HasPrefix(serverDomain, "panel.") {
			return nil
		}

		return errors.New("публичная панель не может жить на домене сервера: выберите отдельный поддомен")
	}

	return nil
}

// Validate проверяет, что режим известен.
func (a Access) Validate() error {
	switch a {
	case AccessTunnel, AccessPublic:
		return nil
	default:
		return fmt.Errorf("режим доступа панели: неизвестное значение %q", a)
	}
}

// withDefaults дополняет незаданное значениями по умолчанию.
//
// Разница между «не задано» и «выключено» здесь та же, что и в конфигурации
// сервера: поле, оставшееся пустым, означает базу, записанную прежней панелью, а
// не панель, выставленную наружу по недосмотру. Mode намеренно не заполняется:
// пустой режим — признак старой записи, и его нужно разрешить по домену сервера.
func (p PanelSettings) withDefaults() PanelSettings {
	defaults := DefaultPanelSettings()

	if p.Access == "" {
		p.Access = defaults.Access
	}
	if p.Port == 0 {
		p.Port = defaults.Port
	}

	return p
}

// normalizeDomain приводит домен панели к тому виду, в котором он хранится.
func (p PanelSettings) normalizeDomain() (PanelSettings, error) {
	if p.Domain == "" {
		return p, nil
	}

	domain, err := normalizeDomain(p.Domain)
	if err != nil {
		return PanelSettings{}, fmt.Errorf("домен панели: %w", err)
	}
	p.Domain = domain

	return p, nil
}

// normalized приводит настройки к хранимому виду и дополняет незаданное.
func (p PanelSettings) normalized() (PanelSettings, error) {
	return p.withDefaults().normalizeDomain()
}

// sameRegistrableDomain сравнивает базовые домены по публичному суффиксу, а не по
// последним двум меткам: у example.co.uk их тоже две, но базовый домен другой.
func sameRegistrableDomain(a, b string) bool {
	baseA, err := publicsuffix.EffectiveTLDPlusOne(a)
	if err != nil {
		return false
	}
	baseB, err := publicsuffix.EffectiveTLDPlusOne(b)
	if err != nil {
		return false
	}

	return strings.EqualFold(baseA, baseB)
}

// PanelSettings возвращает настройки панели. Незаданные настройки — не ошибка:
// это новая или старая база, а панель в них работает по умолчанию.
func (s *Store) PanelSettings() (PanelSettings, error) {
	var settings PanelSettings
	var domain string

	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketAppState)
		if b == nil {
			return errors.New("нет бакета appstate")
		}

		domain = strings.ToLower(strings.TrimSpace(string(b.Get(keyDomain))))

		raw := b.Get(keyPanel)
		if raw == nil {
			settings = DefaultPanelSettings()
			// База без ключа panel, но с доменом panel.* — это ещё старая
			// схема, где второго имени не было.
			if strings.HasPrefix(domain, "panel.") {
				settings.Mode = ""
			}

			return nil
		}

		if err := json.Unmarshal(raw, &settings); err != nil {
			return fmt.Errorf("разбор настроек панели: %w", err)
		}

		normalized, err := settings.normalized()
		if err != nil {
			return err
		}
		settings = normalized

		return nil
	})
	if err != nil {
		return PanelSettings{}, err
	}

	if domain != "" {
		if err := settings.ValidateFor(domain); err != nil {
			return PanelSettings{}, fmt.Errorf("настройки панели: %w", err)
		}
	} else if err := settings.Validate(); err != nil {
		// Негодные настройки — ошибка, а не повод взять умолчание: испорченная
		// база не должна молча превращаться в панель, выставленную наружу.
		return PanelSettings{}, fmt.Errorf("настройки панели: %w", err)
	}

	return settings, nil
}

// SavePanelSettings записывает настройки панели.
func (s *Store) SavePanelSettings(settings PanelSettings) error {
	settings, err := settings.normalizeDomain()
	if err != nil {
		return fmt.Errorf("настройки панели: %w", err)
	}
	if err := settings.Validate(); err != nil {
		return fmt.Errorf("настройки панели: %w", err)
	}

	raw, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("сериализация настроек панели: %w", err)
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketAppState)
		if b == nil {
			return errors.New("нет бакета appstate")
		}

		if err := b.Put(keyPanel, raw); err != nil {
			return fmt.Errorf("запись настроек панели: %w", err)
		}

		return nil
	})
}
