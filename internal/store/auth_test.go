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

// TestCredentialRoundTrip — пароль администратора лежит в базе хешем: панель
// читает его при каждом входе, а не держит в памяти.
func TestCredentialRoundTrip(t *testing.T) {
	s := openStore(t)

	if _, _, ok, err := s.Credential(); err != nil {
		t.Fatalf("Credential() = %v", err)
	} else if ok {
		t.Error("в новой базе уже есть пароль")
	}

	hash, err := authn.Hash("пароль")
	if err != nil {
		t.Fatalf("Hash() = %v", err)
	}

	if err := s.SaveCredential("admin", hash); err != nil {
		t.Fatalf("SaveCredential() = %v", err)
	}

	name, got, ok, err := s.Credential()
	if err != nil {
		t.Fatalf("Credential() = %v", err)
	}
	if !ok {
		t.Fatal("пароль не сохранился")
	}
	if name != "admin" || got != hash {
		t.Errorf("учётные данные = %q и %q, ожидались %q и %q", name, got, "admin", hash)
	}
}

// TestSessionSurvivesRestart — сессия переживает перезапуск панели: иначе
// обновление выбрасывало бы администратора из панели, а он в этот момент может
// чинить именно то, ради чего перезапуск и затевался.
func TestSessionSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hysteria-ward.db")

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	session := authn.Session{
		Token:   "токен",
		CSRF:    "csrf",
		Created: time.Now().Truncate(time.Millisecond),
		Seen:    time.Now().Truncate(time.Millisecond),
	}
	if err := s.SaveSession(session); err != nil {
		t.Fatalf("SaveSession() = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer reopened.Close()

	got, ok, err := reopened.Session(session.Token)
	if err != nil {
		t.Fatalf("Session() = %v", err)
	}
	if !ok {
		t.Fatal("сессия не пережила перезапуск")
	}

	if got.CSRF != session.CSRF {
		t.Errorf("csrf = %q, ожидался %q", got.CSRF, session.CSRF)
	}
	if !got.Created.Equal(session.Created) || !got.Seen.Equal(session.Seen) {
		t.Errorf("времена сессии = %v и %v, ожидались %v и %v", got.Created, got.Seen, session.Created, session.Seen)
	}
}

// TestDeleteSessionIsIdempotent — выход из двух окон одновременно не должен
// ссориться из-за того, что сессии уже нет.
func TestDeleteSessionIsIdempotent(t *testing.T) {
	s := openStore(t)

	if err := s.DeleteSession("нет такой"); err != nil {
		t.Fatalf("DeleteSession() = %v", err)
	}
}

// TestDeleteStaleSessions — выметаются только брошенные сессии.
func TestDeleteStaleSessions(t *testing.T) {
	s := openStore(t)

	now := time.Now()
	stale := authn.Session{Token: "отжившая", CSRF: "csrf", Created: now.Add(-time.Hour), Seen: now.Add(-time.Hour)}
	alive := authn.Session{Token: "живая", CSRF: "csrf", Created: now, Seen: now}

	if err := s.SaveSession(stale); err != nil {
		t.Fatalf("SaveSession() = %v", err)
	}
	if err := s.SaveSession(alive); err != nil {
		t.Fatalf("SaveSession() = %v", err)
	}

	removed, err := s.DeleteStaleSessions(now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("DeleteStaleSessions() = %v", err)
	}
	if removed != 1 {
		t.Errorf("выметено %d сессий, ожидалась 1", removed)
	}

	if _, ok, err := s.Session(stale.Token); err != nil {
		t.Fatalf("Session() = %v", err)
	} else if ok {
		t.Error("отжившая сессия осталась")
	}

	if _, ok, err := s.Session(alive.Token); err != nil {
		t.Fatalf("Session() = %v", err)
	} else if !ok {
		t.Error("живую сессию вымели")
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
