package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

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
