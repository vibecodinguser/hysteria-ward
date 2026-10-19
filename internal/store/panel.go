package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"

	bolt "go.etcd.io/bbolt"
)

// Настройки панели — то, что панель знает о себе самой: кому она показывает себя
// и на каком порту. Лежат в appstate, а не в config: config описывает сервер
// Hysteria 2, а это настройки самой панели.

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

// PanelSettings — настройки панели: кому слушать и на каком порту.
type PanelSettings struct {
	Access Access `json:"access"`
	Port   int    `json:"port"`
}

// DefaultPanelSettings — настройки новой панели: наружу она не выставлена.
func DefaultPanelSettings() PanelSettings {
	return PanelSettings{Access: AccessTunnel, Port: defaultPanelPort}
}

// Addr — адрес, который панель занимает при таких настройках.
func (p PanelSettings) Addr() string {
	return net.JoinHostPort(p.Access.Host(), strconv.Itoa(p.Port))
}

// Validate проверяет, что настройки годны к работе.
func (p PanelSettings) Validate() error {
	if err := p.Access.Validate(); err != nil {
		return err
	}

	if p.Port < minPort || p.Port > maxPort {
		return fmt.Errorf("порт панели: %d вне границ %d—%d", p.Port, minPort, maxPort)
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
// не панель, выставленную наружу по недосмотру.
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

// PanelSettings возвращает настройки панели. Незаданные настройки — не ошибка:
// это новая или старая база, а панель в них работает по умолчанию.
func (s *Store) PanelSettings() (PanelSettings, error) {
	settings := DefaultPanelSettings()

	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketAppState)
		if b == nil {
			return errors.New("нет бакета appstate")
		}

		raw := b.Get(keyPanel)
		if raw == nil {
			return nil
		}

		if err := json.Unmarshal(raw, &settings); err != nil {
			return fmt.Errorf("разбор настроек панели: %w", err)
		}

		settings = settings.withDefaults()

		return nil
	})
	if err != nil {
		return PanelSettings{}, err
	}

	// Негодные настройки — ошибка, а не повод взять умолчание: испорченная база
	// не должна молча превращаться в панель, выставленную наружу.
	if err := settings.Validate(); err != nil {
		return PanelSettings{}, fmt.Errorf("настройки панели: %w", err)
	}

	return settings, nil
}

// SavePanelSettings записывает настройки панели.
func (s *Store) SavePanelSettings(settings PanelSettings) error {
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
