// Пакет store хранит всё изменяемое состояние панели в одной базе bbolt.
//
// Это единственное место в проекте, знающее про bbolt: наружу торчат только
// свои типы. База знает версию своей схемы; при открытии поверх старой базы
// недостающие миграции применяются по порядку, а перед ними снимается копия
// файла — чтобы неудачное обновление не унесло данные.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Store — открытая база панели.
type Store struct {
	db   *bolt.DB
	path string
}

// Open открывает базу, создаёт её при первом запуске и приводит схему к текущей
// версии. Каталог данных создаётся, если его нет.
func Open(path string) (*Store, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("каталог данных %q: %w", dir, err)
	}

	fresh, err := isFresh(path)
	if err != nil {
		return nil, err
	}

	// Таймаут, чтобы второй запуск панели упал с внятной ошибкой, а не повис
	// навсегда на файловой блокировке.
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("открытие базы %q: %w", path, err)
	}

	s := &Store{db: db, path: path}

	if fresh {
		err = s.initSchema()
	} else {
		err = s.migrate()
	}
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	return s, nil
}

// Close закрывает базу.
func (s *Store) Close() error {
	return s.db.Close()
}

// SchemaVersion возвращает версию схемы, записанную в базе.
func (s *Store) SchemaVersion() (int, error) {
	var version int
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		version, err = readSchemaVersion(tx)
		return err
	})
	if err != nil {
		return 0, err
	}

	return version, nil
}

// initSchema заводит схему в новой базе. Мигрировать тут нечего, поэтому и
// копия не нужна: файл только что создан, терять нечего.
func (s *Store) initSchema() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := createBuckets(tx); err != nil {
			return err
		}

		return writeMeta(tx)
	})
}

// isFresh сообщает, что базы ещё нет: файл отсутствует или пуст. Пустой файл
// базой не считается — его мог создать установщик или оборванная запись, а схемы
// в нём нет.
func isFresh(path string) (bool, error) {
	info, err := os.Stat(path)
	switch {
	case err == nil:
		return info.Size() == 0, nil
	case errors.Is(err, os.ErrNotExist):
		return true, nil
	default:
		return false, fmt.Errorf("файл базы %q: %w", path, err)
	}
}
