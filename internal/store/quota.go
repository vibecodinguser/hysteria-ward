package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// QuotaUsage — расход трафика пользователя за текущий период квоты.
//
// Расход лежит отдельно от записи пользователя: правка имени, пароля или самой
// квоты не должна затирать счётчик, который ведёт горячий путь. Период — месяц
// по UTC; Since — его начало, чтобы после перезапуска не считать старое.
type QuotaUsage struct {
	Used  uint64    `json:"used"`
	Since time.Time `json:"since"`
}

// QuotaUsages возвращает расход всех пользователей.
func (s *Store) QuotaUsages() (map[string]QuotaUsage, error) {
	usages := make(map[string]QuotaUsage)

	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketQuota)
		if b == nil {
			return errors.New("нет бакета quota")
		}

		return b.ForEach(func(key, raw []byte) error {
			var usage QuotaUsage
			if err := json.Unmarshal(raw, &usage); err != nil {
				return fmt.Errorf("расход пользователя %q: %w", key, err)
			}

			usages[string(key)] = usage

			return nil
		})
	})
	if err != nil {
		return nil, err
	}

	return usages, nil
}

// SaveQuotaUsages сохраняет расход. Записи пользователей, которых нет в карте,
// не трогаются: удаление идёт отдельной командой вместе с пользователем.
func (s *Store) SaveQuotaUsages(usages map[string]QuotaUsage) error {
	if len(usages) == 0 {
		return nil
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketQuota)
		if b == nil {
			return errors.New("нет бакета quota")
		}

		for id, usage := range usages {
			raw, err := json.Marshal(usage)
			if err != nil {
				return fmt.Errorf("расход пользователя %q: %w", id, err)
			}

			if err := b.Put([]byte(id), raw); err != nil {
				return fmt.Errorf("расход пользователя %q: %w", id, err)
			}
		}

		return nil
	})
}

// ResetQuota обнуляет расход пользователя и ставит начало периода.
func (s *Store) ResetQuota(id string, since time.Time) error {
	raw, err := json.Marshal(QuotaUsage{Since: since})
	if err != nil {
		return fmt.Errorf("расход пользователя %q: %w", id, err)
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketQuota)
		if b == nil {
			return errors.New("нет бакета quota")
		}

		if err := b.Put([]byte(id), raw); err != nil {
			return fmt.Errorf("расход пользователя %q: %w", id, err)
		}

		return nil
	})
}
