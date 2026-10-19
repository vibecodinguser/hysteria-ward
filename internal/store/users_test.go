package store_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// TestUsersAreEmptyAtFirst — новая панель пользователей не выдумывает: список
// пуст, и это не ошибка.
func TestUsersAreEmptyAtFirst(t *testing.T) {
	s := openStore(t)

	users, err := s.Users()
	if err != nil {
		t.Fatalf("Users() = %v", err)
	}
	if len(users) != 0 {
		t.Errorf("пользователей %d, ожидалось 0", len(users))
	}

	if _, found, err := s.User("кого-то"); err != nil || found {
		t.Errorf("User() = found %v, err %v; ожидалось «нет»", found, err)
	}
}

// TestUserRoundTrip — пользователь переживает закрытие базы: пароль лежит в файле
// в открытом виде (ADR-0006), и именно поэтому его можно выдать повторно.
func TestUserRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hysteria-ward.db")

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	created, err := s.CreateUser(store.User{Name: "alice", Password: "s3cret", Enabled: true})
	if err != nil {
		t.Fatalf("CreateUser() = %v", err)
	}
	if created.ID == "" {
		t.Fatal("идентификатор не выдан")
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Error("даты создания и изменения не заполнены")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer reopened.Close()

	got, found, err := reopened.User(created.ID)
	if err != nil || !found {
		t.Fatalf("User() = found %v, err %v", found, err)
	}
	if got.Password != "s3cret" || !got.Enabled {
		t.Errorf("пользователь = %+v, ожидались пароль s3cret и включённость", got)
	}
	if !got.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("дата создания = %v, ожидалась %v", got.CreatedAt, created.CreatedAt)
	}
}

// TestUsersComeSorted — список идёт по именам и без учёта регистра: так его
// показывает интерфейс.
func TestUsersComeSorted(t *testing.T) {
	s := openStore(t)

	for _, name := range []string{"борис", "Carol", "alice"} {
		if _, err := s.CreateUser(store.User{Name: name, Password: name + "-pass", Enabled: true}); err != nil {
			t.Fatalf("CreateUser(%q) = %v", name, err)
		}
	}

	users, err := s.Users()
	if err != nil {
		t.Fatalf("Users() = %v", err)
	}

	want := []string{"alice", "Carol", "борис"}
	if len(users) != len(want) {
		t.Fatalf("пользователей %d, ожидалось %d", len(users), len(want))
	}

	for i, name := range want {
		if users[i].Name != name {
			t.Errorf("пользователь %d = %q, ожидался %q", i, users[i].Name, name)
		}
	}
}

// TestCreateUserRejectsDuplicate — имена уникальны, и регистр их не различает:
// «alice» и «Alice» — один и тот же человек для того, кто ищет, кому выдать ключ.
func TestCreateUserRejectsDuplicate(t *testing.T) {
	s := openStore(t)

	if _, err := s.CreateUser(store.User{Name: "alice", Password: "one"}); err != nil {
		t.Fatalf("CreateUser() = %v", err)
	}

	for _, name := range []string{"alice", "Alice"} {
		if _, err := s.CreateUser(store.User{Name: name, Password: "two"}); !errors.Is(err, store.ErrUserExists) {
			t.Errorf("CreateUser(%q) = %v, ожидалась ErrUserExists", name, err)
		}
	}
}

// TestUpdateUserKeepsIdentity — правка меняет имя, пароль и выключатель, но не
// идентификатор и не дату создания: по ним считаются связи и статистика.
func TestUpdateUserKeepsIdentity(t *testing.T) {
	s := openStore(t)

	created, err := s.CreateUser(store.User{Name: "alice", Password: "one", Enabled: true})
	if err != nil {
		t.Fatalf("CreateUser() = %v", err)
	}

	created.Name = "alisa"
	created.Enabled = false
	created.Password = "two"
	updated, err := s.UpdateUser(created)
	if err != nil {
		t.Fatalf("UpdateUser() = %v", err)
	}

	if updated.ID != created.ID {
		t.Errorf("идентификатор = %q, ожидался %q", updated.ID, created.ID)
	}
	if updated.Name != "alisa" {
		t.Errorf("имя = %q, ожидалось %q", updated.Name, "alisa")
	}
	if updated.Enabled {
		t.Error("пользователь остался включённым")
	}
	if updated.Password != "two" {
		t.Errorf("пароль = %q, ожидался %q", updated.Password, "two")
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("дата создания = %v, ожидалась %v", updated.CreatedAt, created.CreatedAt)
	}

	if _, err := s.UpdateUser(store.User{ID: "никого"}); !errors.Is(err, store.ErrUserMissing) {
		t.Errorf("UpdateUser() = %v, ожидалась ErrUserMissing", err)
	}
}

// TestUpdateUserRejectsTakenName — переименовать в чужое имя нельзя: два
// пользователя с одним именем запутали бы того, кто выдаёт ключи.
func TestUpdateUserRejectsTakenName(t *testing.T) {
	s := openStore(t)

	first, err := s.CreateUser(store.User{Name: "alice", Password: "one"})
	if err != nil {
		t.Fatalf("CreateUser() = %v", err)
	}
	if _, err := s.CreateUser(store.User{Name: "bob", Password: "two"}); err != nil {
		t.Fatalf("CreateUser() = %v", err)
	}

	first.Name = "bob"
	if _, err := s.UpdateUser(first); !errors.Is(err, store.ErrUserExists) {
		t.Errorf("UpdateUser() = %v, ожидалась ErrUserExists", err)
	}

	// Своё имя, набранное другим регистром, конфликтом не считается.
	first.Name = "ALICE"
	if _, err := s.UpdateUser(first); err != nil {
		t.Errorf("UpdateUser(своё имя) = %v", err)
	}
}

// TestDeleteUser — удаление убирает запись; повторное удаление не ошибка, потому
// что команда уже выполнена.
func TestDeleteUser(t *testing.T) {
	s := openStore(t)

	created, err := s.CreateUser(store.User{Name: "alice", Password: "s3cret"})
	if err != nil {
		t.Fatalf("CreateUser() = %v", err)
	}

	if err := s.DeleteUser(created.ID); err != nil {
		t.Fatalf("DeleteUser() = %v", err)
	}
	if _, found, err := s.User(created.ID); err != nil || found {
		t.Errorf("после удаления = found %v, err %v", found, err)
	}

	if err := s.DeleteUser(created.ID); err != nil {
		t.Errorf("повторное DeleteUser() = %v", err)
	}

	// Освободившееся имя можно занять снова.
	if _, err := s.CreateUser(store.User{Name: "alice", Password: "other"}); err != nil {
		t.Errorf("CreateUser(освободившееся имя) = %v", err)
	}
}

// TestUserIDsDiffer — идентификаторы не повторяются: иначе новый пользователь
// унаследовал бы статистику удалённого.
func TestUserIDsDiffer(t *testing.T) {
	s := openStore(t)

	seen := make(map[string]bool)
	for i := 0; i < 20; i++ {
		user, err := s.CreateUser(store.User{Name: "user-" + strings.Repeat("x", i), Password: "pass"})
		if err != nil {
			t.Fatalf("CreateUser() = %v", err)
		}
		if seen[user.ID] {
			t.Fatalf("идентификатор %q повторился", user.ID)
		}

		seen[user.ID] = true
	}
}
