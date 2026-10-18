package store

import (
	"encoding/json"
	"errors"
	"fmt"

	bolt "go.etcd.io/bbolt"

	"github.com/vibecodinguser/hysteria-ward/internal/config"
)

// keyServerConfig — где в бакете config лежит конфигурация сервера.
var keyServerConfig = []byte("server")

// configBucket достаёт бакет config. Его заводит схема, поэтому отсутствие бакета
// означает испорченную базу, а не обычное состояние.
func configBucket(tx *bolt.Tx) (*bolt.Bucket, error) {
	b := tx.Bucket(bucketConfig)
	if b == nil {
		return nil, errors.New("нет бакета config")
	}

	return b, nil
}

// Config возвращает конфигурацию сервера. Пока её не сохраняли, это значения по
// умолчанию: новая панель не должна требовать настройки, чтобы просто открыться.
//
// Недостающие поля дополняются значениями по умолчанию: базу могла записать
// прежняя панель, которой этих полей ещё не было.
func (s *Store) Config() (config.Config, error) {
	var cfg config.Config

	err := s.db.View(func(tx *bolt.Tx) error {
		b, err := configBucket(tx)
		if err != nil {
			return err
		}

		raw := b.Get(keyServerConfig)
		if raw == nil {
			cfg = config.Default()
			return nil
		}

		if err := json.Unmarshal(raw, &cfg); err != nil {
			return fmt.Errorf("разбор конфигурации: %w", err)
		}

		cfg = cfg.FillDefaults()

		return nil
	})
	if err != nil {
		return config.Config{}, err
	}

	return cfg, nil
}

// SaveConfig сохраняет конфигурацию. Сама по себе она не применяется: пока
// панель не сказала «Применить», ядро работает со старым конфигом.
//
// Здесь нет проверки годности: правки копятся как черновик, а ругаться на
// негодный конфиг должен тот, кто его применяет.
func (s *Store) SaveConfig(cfg config.Config) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("сериализация конфигурации: %w", err)
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := configBucket(tx)
		if err != nil {
			return err
		}

		if err := b.Put(keyServerConfig, raw); err != nil {
			return fmt.Errorf("запись конфигурации: %w", err)
		}

		return nil
	})
}
