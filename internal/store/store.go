// Пакет store хранит всё изменяемое состояние панели в одной базе bbolt.
//
// Это единственное место в проекте, знающее про bbolt: наружу торчат только
// свои типы. База знает версию своей схемы; при открытии поверх старой базы
// недостающие миграции применяются по порядку, а перед ними снимается копия
// файла — чтобы неудачное обновление не унесло данные.
package store

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/vibecodinguser/hysteria-ward/internal/build"
)

// schemaVersion — текущая версия схемы. Растёт на единицу с каждой миграцией.
//
// Это переменная, а не константа, ровно для одного: тест подменяет её вместе со
// списком миграций и проверяет сам механизм, пока реальных миграций нет.
var schemaVersion = 1

// migrations — шаги миграции по порядку: migrations[i] переводит схему с версии
// i+1 на i+2.
var migrations []func(tx *bolt.Tx) error

// Бакеты из раздела 4 docs/plan.md.
var (
	bucketMeta     = []byte("meta")
	bucketConfig   = []byte("config")
	bucketUsers    = []byte("users")
	bucketSessions = []byte("sessions")
	bucketAppState = []byte("appstate")
	bucketMetrics  = []byte("metrics")
	bucketEvents   = []byte("events")
)

// schemaBuckets — бакеты, которые обязаны существовать в текущей схеме.
var schemaBuckets = [][]byte{
	bucketMeta, bucketConfig, bucketUsers, bucketSessions,
	bucketAppState, bucketMetrics, bucketEvents,
}

var (
	keySchemaVersion = []byte("schema_version")
	keyCreatedAt     = []byte("created_at")
	keyAppVersion    = []byte("app_version")
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
	if err := s.prepare(fresh); err != nil {
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

// prepare приводит базу к текущей схеме.
func (s *Store) prepare(fresh bool) error {
	if fresh {
		return s.init()
	}

	return s.migrate()
}

// init заводит схему в новой базе. Мигрировать тут нечего, поэтому и копия не
// нужна: терять пока нечего, файл только что создан.
func (s *Store) init() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := createBuckets(tx); err != nil {
			return err
		}

		return writeMeta(tx)
	})
}

// migrate применяет недостающие миграции к существующей базе.
func (s *Store) migrate() error {
	var from int
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		from, err = readSchemaVersion(tx)
		return err
	})
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

// isFresh сообщает, что файла базы ещё нет: значит, база создаётся с нуля и
// мигрировать в ней нечего.
func isFresh(path string) (bool, error) {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, os.ErrNotExist):
		return true, nil
	default:
		return false, fmt.Errorf("файл базы %q: %w", path, err)
	}
}

func createBuckets(tx *bolt.Tx) error {
	for _, name := range schemaBuckets {
		if _, err := tx.CreateBucketIfNotExists(name); err != nil {
			return fmt.Errorf("бакет %q: %w", name, err)
		}
	}

	return nil
}

// writeMeta записывает текущую версию схемы и версию приложения. Дату создания
// не трогает: она ставится один раз и переживает обновления.
func writeMeta(tx *bolt.Tx) error {
	b := tx.Bucket(bucketMeta)
	if b == nil {
		return errors.New("нет бакета meta")
	}

	var version [8]byte
	binary.BigEndian.PutUint64(version[:], uint64(schemaVersion))
	if err := b.Put(keySchemaVersion, version[:]); err != nil {
		return fmt.Errorf("запись версии схемы: %w", err)
	}

	if b.Get(keyCreatedAt) == nil {
		created := time.Now().UTC().Format(time.RFC3339)
		if err := b.Put(keyCreatedAt, []byte(created)); err != nil {
			return fmt.Errorf("запись даты создания: %w", err)
		}
	}

	if err := b.Put(keyAppVersion, []byte(build.Current().Version)); err != nil {
		return fmt.Errorf("запись версии приложения: %w", err)
	}

	return nil
}

func readSchemaVersion(tx *bolt.Tx) (int, error) {
	b := tx.Bucket(bucketMeta)
	if b == nil {
		return 0, errors.New("база без бакета meta: схема не инициализирована")
	}

	raw := b.Get(keySchemaVersion)
	if raw == nil {
		return 0, errors.New("база без версии схемы: схема не инициализирована")
	}
	if len(raw) != 8 {
		return 0, fmt.Errorf("версия схемы испорчена: %d байт", len(raw))
	}

	return int(binary.BigEndian.Uint64(raw)), nil
}
