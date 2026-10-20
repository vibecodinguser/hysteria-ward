package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/authn"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// *store.Store обязан уметь то, что нужно authn. Проверка стоит здесь, чтобы
// расхождение нашлось при сборке, а не при первом входе в панель.
var _ authn.Storage = (*store.Store)(nil)

// TestCredentialRoundTrip — учётные данные администратора лежат в базе: панель
// читает их при каждом входе, а не держит в памяти.
func TestCredentialRoundTrip(t *testing.T) {
	s := openStore(t)

	if _, ok, err := s.Credential(); err != nil {
		t.Fatalf("Credential() = %v", err)
	} else if ok {
		t.Error("в новой базе уже есть пароль")
	}

	hash, err := authn.Hash("пароль")
	if err != nil {
		t.Fatalf("Hash() = %v", err)
	}

	want := authn.Credential{Name: "admin", Hash: hash}
	if err := s.SaveCredential(want); err != nil {
		t.Fatalf("SaveCredential() = %v", err)
	}

	got, ok, err := s.Credential()
	if err != nil {
		t.Fatalf("Credential() = %v", err)
	}
	if !ok {
		t.Fatal("пароль не сохранился")
	}
	if got != want {
		t.Errorf("учётные данные = %q и %q, ожидались %q и %q", got.Name, got.Hash, want.Name, want.Hash)
	}
}

// TestSaveCredentialDropsSessions — смена пароля закрывает все входы. Иначе
// украденная вместе с cookie сессия пережила бы смену пароля, то есть ровно тот
// случай, ради которого пароль и меняют.
func TestSaveCredentialDropsSessions(t *testing.T) {
	s := openStore(t)

	now := time.Now()
	for _, token := range []string{"первая", "вторая"} {
		if err := s.SaveSession(authn.Session{
			Token: token, CSRF: "csrf", Created: now, Seen: now,
		}); err != nil {
			t.Fatalf("SaveSession() = %v", err)
		}
	}

	hash, err := authn.Hash("новый пароль")
	if err != nil {
		t.Fatalf("Hash() = %v", err)
	}
	if err := s.SaveCredential(authn.Credential{Name: "admin", Hash: hash}); err != nil {
		t.Fatalf("SaveCredential() = %v", err)
	}

	for _, token := range []string{"первая", "вторая"} {
		if _, ok, err := s.Session(token); err != nil {
			t.Fatalf("Session() = %v", err)
		} else if ok {
			t.Errorf("сессия %q пережила смену пароля", token)
		}
	}

	// Новый вход после смены пароля работает: закрыты прежние сессии, а не вход
	// вообще.
	if err := s.SaveSession(authn.Session{
		Token: "третья", CSRF: "csrf", Created: now, Seen: now,
	}); err != nil {
		t.Fatalf("SaveSession() = %v", err)
	}
	if _, ok, err := s.Session("третья"); err != nil || !ok {
		t.Errorf("новая сессия не сохранилась: ok=%v, err=%v", ok, err)
	}
}

// openStore открывает пустую базу во временном каталоге.
func openStore(t *testing.T) *store.Store {
	t.Helper()

	s, err := store.Open(filepath.Join(t.TempDir(), "hysteria-ward.db"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
	})

	return s
}
