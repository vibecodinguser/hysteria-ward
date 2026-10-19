package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	bolt "go.etcd.io/bbolt"

	"github.com/vibecodinguser/hysteria-ward/internal/authn"
	"github.com/vibecodinguser/hysteria-ward/internal/config"
)

// ErrAlreadyConfigured — база уже была настроена. Начальная настройка не
// переписывает работающую панель: смену домена, доступа и пароля делают из
// интерфейса, а не повторным запуском установщика.
var ErrAlreadyConfigured = errors.New("панель уже настроена")

// InitialSetup — ответы конфигуратора установки, которые нужно сохранить в базу
// одним действием.
//
// Одно действие, а не пять отдельных записей: наполовину записанная настройка
// выглядит как работающая панель, у которой часть ответов потерялась. Ошибка
// откатывает транзакцию целиком, и установщик видит, что настройка не удалась.
type InitialSetup struct {
	Domain    string
	ACMEEmail string
	ACME      ACMESettings
	Config    config.Config
	Panel     PanelSettings
	Admin     authn.Credential
}

// ValidateInitialSetup проверяет ответы, не касаясь базы, и возвращает их в том
// виде, в котором они будут сохранены.
//
// Проверка отделена от записи: установщик вызывает её до изменений в системе, а
// запись — уже после установки бинаря. Опечатка в домене не должна стоить
// установленного пакета или заведённого пользователя.
func ValidateInitialSetup(setup InitialSetup) (InitialSetup, error) {
	domain, err := normalizeDomain(setup.Domain)
	if err != nil {
		return InitialSetup{}, err
	}
	if domain == "" {
		return InitialSetup{}, errors.New("домен сервера не задан")
	}
	if net.ParseIP(domain) != nil {
		return InitialSetup{}, fmt.Errorf("%w: %q — для ACME нужен домен, а не IP-адрес", ErrBadDomain, domain)
	}

	email, err := normalizeACMEEmail(setup.ACMEEmail)
	if err != nil {
		return InitialSetup{}, err
	}
	if email == "" {
		return InitialSetup{}, errors.New("e-mail для ACME не задан")
	}

	acme := setup.ACME.withDefaults()
	if err := acme.Validate(); err != nil {
		return InitialSetup{}, err
	}

	cfg := setup.Config.FillDefaults()
	if err := cfg.Validate(); err != nil {
		return InitialSetup{}, fmt.Errorf("конфигурация сервера: %w", err)
	}

	panel := setup.Panel.withDefaults()
	if err := panel.Validate(); err != nil {
		return InitialSetup{}, err
	}

	if strings.TrimSpace(setup.Admin.Name) == "" {
		return InitialSetup{}, errors.New("не задано имя администратора")
	}
	if setup.Admin.Hash == "" {
		return InitialSetup{}, errors.New("не задан пароль администратора")
	}

	setup.Domain = domain
	setup.ACMEEmail = email
	setup.ACME = acme
	setup.Config = cfg
	setup.Panel = panel

	return setup, nil
}

// ApplyInitialSetup сохраняет начальную настройку одной транзакцией.
//
// Повторная настройка уже настроенной базы отклоняется: ключи, которые могли бы
// быть перезаписаны, — это домен, сертификат и пароль, и терять их из-за
// случайного повторного запуска установщика нельзя.
func (s *Store) ApplyInitialSetup(setup InitialSetup) error {
	setup, err := ValidateInitialSetup(setup)
	if err != nil {
		return err
	}

	rawConfig, err := json.Marshal(setup.Config)
	if err != nil {
		return fmt.Errorf("сериализация конфигурации: %w", err)
	}

	rawPanel, err := json.Marshal(setup.Panel)
	if err != nil {
		return fmt.Errorf("сериализация настроек панели: %w", err)
	}

	rawACME, err := json.Marshal(setup.ACME)
	if err != nil {
		return fmt.Errorf("сериализация настроек ACME: %w", err)
	}

	err = s.db.Update(func(tx *bolt.Tx) error {
		app, err := appStateBucket(tx)
		if err != nil {
			return err
		}

		cfg, err := configBucket(tx)
		if err != nil {
			return err
		}

		auth, err := authBucket(tx)
		if err != nil {
			return err
		}

		if app.Get(keyDomain) != nil || app.Get(keyACMEEmail) != nil ||
			app.Get(keyPanel) != nil || app.Get(keyACME) != nil ||
			cfg.Get(keyServerConfig) != nil ||
			auth.Get(keyAdminHash) != nil {
			return ErrAlreadyConfigured
		}

		if err := app.Put(keyDomain, []byte(setup.Domain)); err != nil {
			return fmt.Errorf("домен: %w", err)
		}
		if err := app.Put(keyACMEEmail, []byte(setup.ACMEEmail)); err != nil {
			return fmt.Errorf("e-mail для ACME: %w", err)
		}
		if err := app.Put(keyACME, rawACME); err != nil {
			return fmt.Errorf("настройки ACME: %w", err)
		}
		if err := app.Put(keyPanel, rawPanel); err != nil {
			return fmt.Errorf("настройки панели: %w", err)
		}
		if err := cfg.Put(keyServerConfig, rawConfig); err != nil {
			return fmt.Errorf("конфигурация: %w", err)
		}
		if err := auth.Put(keyAdminName, []byte(setup.Admin.Name)); err != nil {
			return fmt.Errorf("имя администратора: %w", err)
		}
		if err := auth.Put(keyAdminHash, []byte(setup.Admin.Hash)); err != nil {
			return fmt.Errorf("хеш пароля: %w", err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("начальная настройка: %w", err)
	}

	return nil
}

// appStateBucket достаёт бакет appstate. Его заводит схема, поэтому отсутствие
// бакета означает испорченную базу.
func appStateBucket(tx *bolt.Tx) (*bolt.Bucket, error) {
	b := tx.Bucket(bucketAppState)
	if b == nil {
		return nil, errors.New("нет бакета appstate")
	}

	return b, nil
}
