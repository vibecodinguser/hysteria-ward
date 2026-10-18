package store

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/vibecodinguser/hysteria-ward/internal/build"
)

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

// versionBytes — сколько байт занимает версия схемы в базе.
const versionBytes = 8

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

	if err := b.Put(keySchemaVersion, encodeVersion(schemaVersion)); err != nil {
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

	return decodeVersion(raw)
}

// encodeVersion кодирует версию схемы для хранения в meta: восемь байт
// big-endian, чтобы версия читалась одинаково на любой платформе.
func encodeVersion(version int) []byte {
	var raw [versionBytes]byte
	binary.BigEndian.PutUint64(raw[:], uint64(version))

	return raw[:]
}

func decodeVersion(raw []byte) (int, error) {
	if len(raw) != versionBytes {
		return 0, fmt.Errorf("версия схемы испорчена: %d байт", len(raw))
	}

	return int(binary.BigEndian.Uint64(raw)), nil
}
