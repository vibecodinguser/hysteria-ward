package store

import (
	"os"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// TestMigrationAppliesStepsAndBacksUp — механизм миграций целиком: старая база
// доводится до текущей версии, данные целы, а перед этим появляется копия.
//
// Настоящих миграций пока нет — схема только появилась, — поэтому список шагов
// и целевую версию подменяем: иначе механизм нечем проверить.
func TestMigrationAppliesStepsAndBacksUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hysteria-ward.db")
	prepareDatabase(t, path, 1)

	restore := migrations
	migrations = []func(tx *bolt.Tx) error{
		func(tx *bolt.Tx) error {
			return tx.Bucket(bucketConfig).Put([]byte("migrated"), []byte("yes"))
		},
	}
	oldVersion := schemaVersion
	schemaVersion = 2
	t.Cleanup(func() {
		migrations = restore
		schemaVersion = oldVersion
	})

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open() = %v, ожидалась миграция", err)
	}
	defer s.Close()

	version, err := s.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion() = %v", err)
	}
	if version != 2 {
		t.Errorf("версия схемы %d, ожидалась 2", version)
	}

	if _, err := os.Stat(path + ".backup-v1"); err != nil {
		t.Errorf("копия базы перед миграцией не создана: %v", err)
	}

	// Копия снята до миграции: в ней старая версия схемы и те же данные.
	backup, err := bolt.Open(path+".backup-v1", 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("копия не открылась: %v", err)
	}
	defer backup.Close()

	err = backup.View(func(tx *bolt.Tx) error {
		backupVersion, err := readSchemaVersion(tx)
		if err != nil {
			return err
		}
		if backupVersion != 1 {
			t.Errorf("в копии версия схемы %d, ожидалась 1", backupVersion)
		}

		if got := string(tx.Bucket(bucketUsers).Get([]byte("alice"))); got != "secret" {
			t.Errorf("в копии данные пользователя %q, ожидались %q", got, "secret")
		}

		return nil
	})
	if err != nil {
		t.Fatalf("View() копии = %v", err)
	}

	err = s.db.View(func(tx *bolt.Tx) error {
		for _, name := range schemaBuckets {
			if tx.Bucket(name) == nil {
				t.Errorf("после миграции нет бакета %q", name)
			}
		}

		if got := string(tx.Bucket(bucketConfig).Get([]byte("migrated"))); got != "yes" {
			t.Errorf("шаг миграции не применился: config.migrated = %q", got)
		}

		if got := string(tx.Bucket(bucketUsers).Get([]byte("alice"))); got != "secret" {
			t.Errorf("данные пользователя после миграции %q, ожидались %q", got, "secret")
		}

		return nil
	})
	if err != nil {
		t.Fatalf("View() = %v", err)
	}
}

// TestOpenRejectsNewerSchema — база от более новой панели не открывается:
// старая панель не понимает её схему и, мигрируя вслепую, испортила бы данные.
func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hysteria-ward.db")
	prepareDatabase(t, path, schemaVersion+1)

	s, err := Open(path)
	if err == nil {
		_ = s.Close()
		t.Fatal("база новее панели открылась, ожидалась ошибка")
	}
}

// prepareDatabase создаёт базу с заданной версией схемы и данными, которые
// миграция трогать не должна.
func prepareDatabase(t *testing.T, path string, version int) {
	t.Helper()

	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatalf("старая база не открылась: %v", err)
	}

	err = db.Update(func(tx *bolt.Tx) error {
		meta, err := tx.CreateBucketIfNotExists(bucketMeta)
		if err != nil {
			return err
		}

		if err := meta.Put(keySchemaVersion, encodeVersion(version)); err != nil {
			return err
		}
		users, err := tx.CreateBucketIfNotExists(bucketUsers)
		if err != nil {
			return err
		}

		return users.Put([]byte("alice"), []byte("secret"))
	})
	if err != nil {
		t.Fatalf("подготовка старой базы: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("закрытие старой базы: %v", err)
	}
}
