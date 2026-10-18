package store_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// TestOpenCreatesAllSchemaBuckets — схема заводит все бакеты из раздела 4 плана,
// а не только те, что понадобились первыми. Проверяем чужими глазами: открываем
// тот же файл тем же bbolt, что и сама панель.
func TestOpenCreatesAllSchemaBuckets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hysteria-ward.db")

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("база не открылась на чтение: %v", err)
	}
	defer db.Close()

	want := []string{"meta", "config", "users", "sessions", "appstate", "metrics", "events"}
	err = db.View(func(tx *bolt.Tx) error {
		for _, name := range want {
			if tx.Bucket([]byte(name)) == nil {
				t.Errorf("нет бакета %q", name)
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("View() = %v", err)
	}
}

// TestOpenCreatesAndReopensDatabase — база создаётся при первом запуске и
// переживает перезапуск панели: версия схемы читается из неё же, а не берётся
// из константы в коде.
func TestOpenCreatesAndReopensDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hysteria-ward.db")

	first, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v, ожидалось создание базы", err)
	}

	version, err := first.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion() = %v", err)
	}
	if version == 0 {
		t.Error("версия схемы равна нулю, ожидалась записанная в базе")
	}

	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("файл базы не создан: %v", err)
	}

	second, err := store.Open(path)
	if err != nil {
		t.Fatalf("повторный Open() = %v, база не пережила перезапуск", err)
	}
	defer second.Close()

	reopened, err := second.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion() = %v", err)
	}
	if reopened != version {
		t.Errorf("после перезапуска версия схемы %d, была %d", reopened, version)
	}
}

// TestOpenInitializesEmptyFile — пустой файл на месте базы (его мог создать
// установщик или оборванная запись) не считается схемой: вместо вечной ошибки
// база заводится заново.
func TestOpenInitializesEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hysteria-ward.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("пустой файл не создался: %v", err)
	}

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v, ожидалась инициализация пустого файла", err)
	}
	defer s.Close()

	if _, err := s.SchemaVersion(); err != nil {
		t.Fatalf("SchemaVersion() = %v", err)
	}
}

// TestNoBackupWhenSchemaIsCurrent — если мигрировать нечего, копия не создаётся:
// иначе каждый запуск панели плодил бы бэкапы на пустом месте.
func TestNoBackupWhenSchemaIsCurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hysteria-ward.db")

	first, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	second, err := store.Open(path)
	if err != nil {
		t.Fatalf("повторный Open() = %v", err)
	}
	defer second.Close()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() = %v", err)
	}

	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".backup-") {
			t.Errorf("создана копия %q, хотя миграция не требовалась", entry.Name())
		}
	}
}
