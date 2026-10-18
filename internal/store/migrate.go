package store

import (
	"fmt"
	"os"

	bolt "go.etcd.io/bbolt"
)

// schemaVersion — текущая версия схемы. Растёт на единицу с каждой миграцией.
//
// Это переменная, а не константа, ровно для одного: тест подменяет её вместе со
// списком миграций и проверяет сам механизм, пока реальных миграций нет.
var schemaVersion = 1

// migrations — шаги миграции по порядку: migrations[i] переводит схему с версии
// i+1 на i+2.
var migrations []func(tx *bolt.Tx) error

// migrate применяет недостающие миграции к существующей базе.
func (s *Store) migrate() error {
	from, err := s.SchemaVersion()
	if err != nil {
		return err
	}

	switch {
	case from == schemaVersion:
		return s.db.Update(writeMeta)
	case from > schemaVersion:
		return fmt.Errorf("база версии схемы %d новее панели (%d): обновите панель", from, schemaVersion)
	}

	if len(migrations) < schemaVersion-1 {
		return fmt.Errorf("не хватает миграций: есть %d, нужно %d", len(migrations), schemaVersion-1)
	}

	if err := s.backup(from); err != nil {
		return err
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		// Бакеты — часть схемы: старая база могла не знать о тех, что появились
		// позже, поэтому доводим их набор до текущего до шагов миграции.
		if err := createBuckets(tx); err != nil {
			return err
		}

		for version := from; version < schemaVersion; version++ {
			step := migrations[version-1]
			if err := step(tx); err != nil {
				return fmt.Errorf("миграция до версии %d: %w", version+1, err)
			}
		}

		return writeMeta(tx)
	})
}

// backup снимает копию базы перед миграцией. Копируется не файл, а транзакция:
// файл на диске в этот момент открыт и меняется, а WriteTo даёт согласованный
// снимок. Копия доходит до диска раньше, чем начнётся миграция.
func (s *Store) backup(from int) error {
	name := fmt.Sprintf("%s.backup-v%d", s.path, from)

	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("копия базы %q: %w", name, err)
	}

	err = s.db.View(func(tx *bolt.Tx) error {
		if _, err := tx.WriteTo(f); err != nil {
			return fmt.Errorf("копия базы %q: %w", name, err)
		}

		return nil
	})
	if err != nil {
		_ = f.Close()
		return err
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("копия базы %q: %w", name, err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("копия базы %q: %w", name, err)
	}

	return nil
}
