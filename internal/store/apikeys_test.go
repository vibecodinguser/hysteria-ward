package store_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// TestAPIKeyRoundTrip — ключ выпускается, аутентифицирует и переживает
// перезапуск. Токен в базе не лежит: только его хеш.
func TestAPIKeyRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hysteria-ward.db")

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	key, token, err := s.CreateAPIKey("MCP")
	if err != nil {
		t.Fatalf("CreateAPIKey() = %v", err)
	}
	if key.ID == "" || key.Name != "MCP" {
		t.Errorf("ключ = %+v, ожидались идентификатор и имя", key)
	}
	if !strings.HasPrefix(token, "hwmcp_") {
		t.Errorf("токен = %q, ожидался префикс hwmcp_", token)
	}

	keys, err := s.APIKeys()
	if err != nil {
		t.Fatalf("APIKeys() = %v", err)
	}
	if len(keys) != 1 || keys[0].ID != key.ID {
		t.Errorf("список ключей = %+v, ожидался один ключ", keys)
	}

	found, ok, err := s.AuthenticateAPIKey(token)
	if err != nil {
		t.Fatalf("AuthenticateAPIKey() = %v", err)
	}
	if !ok || found.ID != key.ID {
		t.Errorf("ключ не прошёл проверку: %+v, %v", found, ok)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	// В записи не должно быть самого токена: утечка базы не должна давать
	// действующий ключ.
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("база не открылась на чтение: %v", err)
	}

	err = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("apikeys"))
		if b == nil {
			t.Fatal("нет бакета apikeys")
		}

		return b.ForEach(func(_, raw []byte) error {
			if strings.Contains(string(raw), token) {
				t.Error("токен API-ключа лежит в базе открытым")
			}

			return nil
		})
	})
	if err != nil {
		t.Fatalf("View() = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() базы на чтение = %v", err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("повторный Open() = %v", err)
	}
	defer reopened.Close()

	if _, ok, err := reopened.AuthenticateAPIKey(token); err != nil || !ok {
		t.Errorf("после перезапуска ключ не прошёл проверку: ok=%v err=%v", ok, err)
	}
}

// TestAPIKeyRejectsWrongToken — чужой или испорченный токен не проходит.
func TestAPIKeyRejectsWrongToken(t *testing.T) {
	s := openStore(t)

	if _, _, err := s.CreateAPIKey("MCP"); err != nil {
		t.Fatalf("CreateAPIKey() = %v", err)
	}

	for _, token := range []string{"", "hwmcp_чужой", "чужой", strings.Repeat("a", 300)} {
		if _, ok, err := s.AuthenticateAPIKey(token); err != nil {
			t.Fatalf("AuthenticateAPIKey(%q) = %v", token, err)
		} else if ok {
			t.Errorf("токен %q прошёл проверку", token)
		}
	}
}

// TestAPIKeyNameUnique — имена различаются людьми, поэтому «MCP» и «mcp» — один
// и тот же ключ.
func TestAPIKeyNameUnique(t *testing.T) {
	s := openStore(t)

	if _, _, err := s.CreateAPIKey("MCP"); err != nil {
		t.Fatalf("CreateAPIKey() = %v", err)
	}

	if _, _, err := s.CreateAPIKey("mcp"); !errors.Is(err, store.ErrAPIKeyExists) {
		t.Errorf("повторное имя: %v, ожидалась ErrAPIKeyExists", err)
	}
}

// TestAPIKeyDeleteRevokes — отозванный ключ больше не аутентифицирует, а
// повторный отзыв говорит, что удалять было нечего.
func TestAPIKeyDeleteRevokes(t *testing.T) {
	s := openStore(t)

	key, token, err := s.CreateAPIKey("MCP")
	if err != nil {
		t.Fatalf("CreateAPIKey() = %v", err)
	}

	if err := s.DeleteAPIKey(key.ID); err != nil {
		t.Fatalf("DeleteAPIKey() = %v", err)
	}

	if _, ok, err := s.AuthenticateAPIKey(token); err != nil {
		t.Fatalf("AuthenticateAPIKey() = %v", err)
	} else if ok {
		t.Error("отозванный ключ прошёл проверку")
	}

	if err := s.DeleteAPIKey(key.ID); !errors.Is(err, store.ErrAPIKeyMissing) {
		t.Errorf("повторный отзыв: %v, ожидалась ErrAPIKeyMissing", err)
	}
}
