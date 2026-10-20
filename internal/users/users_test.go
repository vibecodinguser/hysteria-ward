package users_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/store"
	"github.com/vibecodinguser/hysteria-ward/internal/users"
)

// openStore открывает пустую базу во временном каталоге.
func openStore(t *testing.T) *store.Store {
	t.Helper()

	s, err := store.Open(filepath.Join(t.TempDir(), "hysteria-ward.db"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	return s
}

// addUser заводит включённого пользователя и возвращает его.
func addUser(t *testing.T, s *store.Store, name, password string) store.User {
	t.Helper()

	user, err := s.CreateUser(store.User{Name: name, Password: password, Enabled: true})
	if err != nil {
		t.Fatalf("CreateUser() = %v", err)
	}

	return user
}

// TestAuthFindsEnabledUser — верный пароль называет пользователя: по этому
// идентификатору ядро считает его трафик.
func TestAuthFindsEnabledUser(t *testing.T) {
	s := openStore(t)
	user := addUser(t, s, "alice", "s3cret")

	registry, err := users.New(s)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	connection, ok := registry.Auth("s3cret")
	if !ok {
		t.Fatal("верный пароль не принят")
	}

	id, ok := registry.Resolve(connection)
	if !ok {
		t.Fatalf("соединение %q не разобрано", connection)
	}
	if id != user.ID {
		t.Errorf("пользователь = %q, ожидался %q", id, user.ID)
	}

	// Голый идентификатор пользователя — не строка соединения: без версии пароля
	// он не говорит, с каким ключом открыто соединение.
	if _, ok := registry.Resolve(user.ID); ok {
		t.Error("голый идентификатор пользователя принят за соединение")
	}

	if _, ok := registry.Auth("s3cret!"); ok {
		t.Error("неверный пароль принят")
	}
	if _, ok := registry.Auth(""); ok {
		t.Error("пустой пароль принят")
	}
}

// TestAuthSkipsDisabled — выключенный пользователь не проходит авторизацию: его
// ключ остался у клиента, но пускать по нему больше нельзя.
func TestAuthSkipsDisabled(t *testing.T) {
	s := openStore(t)
	user := addUser(t, s, "alice", "s3cret")

	user.Enabled = false
	if _, err := s.UpdateUser(user); err != nil {
		t.Fatalf("UpdateUser() = %v", err)
	}

	registry, err := users.New(s)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	if id, ok := registry.Auth("s3cret"); ok {
		t.Errorf("выключенный пользователь %q пущен", id)
	}
}

// TestTrafficStopsForDisabledAndDeleted — учёт трафика — это и есть рычаг: пока он
// отвечает false, ядро рвёт соединение. Значит, выключение и удаление достают и до
// тех, кто уже подключился.
func TestTrafficStopsForDisabledAndDeleted(t *testing.T) {
	s := openStore(t)
	user := addUser(t, s, "alice", "s3cret")

	registry, err := users.New(s)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	connection, ok := registry.Auth("s3cret")
	if !ok {
		t.Fatal("верный пароль не принят")
	}

	if id, ok := registry.Traffic(connection, 10, 10); !ok || id != user.ID {
		t.Fatalf("трафик включённого = %q, %v; ожидался %q", id, ok, user.ID)
	}

	user.Enabled = false
	if _, err := s.UpdateUser(user); err != nil {
		t.Fatalf("UpdateUser() = %v", err)
	}
	if err := registry.Reload(); err != nil {
		t.Fatalf("Reload() = %v", err)
	}

	if _, ok := registry.Traffic(connection, 10, 10); ok {
		t.Error("трафик выключенного пользователя пропущен")
	}

	if err := s.DeleteUser(user.ID); err != nil {
		t.Fatalf("DeleteUser() = %v", err)
	}
	if err := registry.Reload(); err != nil {
		t.Fatalf("Reload() = %v", err)
	}

	if _, ok := registry.Traffic(connection, 10, 10); ok {
		t.Error("трафик удалённого пользователя пропущен")
	}

	// Неизвестное соединение — тоже отказ: связи с ним больше нет.
	if _, ok := registry.Traffic("никого", 1, 1); ok {
		t.Error("трафик неизвестного соединения пропущен")
	}
}

// TestPasswordChangeDropsOpenConnections — новый пароль пускает, старый уже нет, и
// соединение, открытое старым ключом, обрывается: учёт трафика видит, что версия
// пароля в строке соединения больше не совпадает с текущей.
func TestPasswordChangeDropsOpenConnections(t *testing.T) {
	s := openStore(t)
	user := addUser(t, s, "alice", "старый")

	registry, err := users.New(s)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	old, ok := registry.Auth("старый")
	if !ok {
		t.Fatal("старый пароль не принят")
	}
	registry.Online(old, true)

	user.Password = "новый"
	if _, err := s.UpdateUser(user); err != nil {
		t.Fatalf("UpdateUser() = %v", err)
	}
	if err := registry.Reload(); err != nil {
		t.Fatalf("Reload() = %v", err)
	}

	if _, ok := registry.Auth("старый"); ok {
		t.Error("старый пароль всё ещё пускает")
	}
	if _, ok := registry.Resolve(old); ok {
		t.Error("старое соединение всё ещё опознаётся")
	}
	if _, ok := registry.Traffic(old, 1, 1); ok {
		t.Error("трафик старого соединения пропущен")
	}

	// Отключение старого соединения снимает отметку «в сети», хотя строка уже не
	// проходит проверку: иначе пользователь остался бы в сети без соединений.
	registry.Online(old, false)
	if registry.IsOnline(user.ID) {
		t.Error("старое соединение оставило пользователя в сети")
	}

	fresh, ok := registry.Auth("новый")
	if !ok {
		t.Fatal("новый пароль не принят")
	}
	if id, ok := registry.Traffic(fresh, 1, 1); !ok || id != user.ID {
		t.Errorf("новое соединение = %q, %v; ожидался пользователь %q", id, ok, user.ID)
	}
}

// TestOnlineState — интерфейс показывает, кто подключён. Состояние живёт в
// памяти: после перезапуска панели подключатся заново.
func TestOnlineState(t *testing.T) {
	s := openStore(t)
	user := addUser(t, s, "alice", "s3cret")

	registry, err := users.New(s)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	connection, ok := registry.Auth("s3cret")
	if !ok {
		t.Fatal("верный пароль не принят")
	}

	if registry.IsOnline(user.ID) {
		t.Error("пользователь сразу считается подключённым")
	}

	registry.Online(connection, true)
	if !registry.IsOnline(user.ID) {
		t.Error("подключение не запомнено")
	}

	registry.Online(connection, false)
	if registry.IsOnline(user.ID) {
		t.Error("отключение не запомнено")
	}

	// Удалённого пользователя список подключённых не помнит: иначе запись о нём
	// жила бы до перезапуска панели.
	registry.Online(connection, true)
	if err := s.DeleteUser(user.ID); err != nil {
		t.Fatalf("DeleteUser() = %v", err)
	}
	if err := registry.Reload(); err != nil {
		t.Fatalf("Reload() = %v", err)
	}
	if registry.IsOnline(user.ID) {
		t.Error("удалённый пользователь остался в списке подключённых")
	}
}

// TestPasswordInUse — два пользователя с одним паролем означали бы, что сервер не
// знает, кто именно подключился.
func TestPasswordInUse(t *testing.T) {
	s := openStore(t)
	alice := addUser(t, s, "alice", "same")
	bob := addUser(t, s, "bob", "other")

	registry, err := users.New(s)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	if !registry.PasswordInUse("same", "") {
		t.Error("занятый пароль назван свободным")
	}
	if registry.PasswordInUse("same", alice.ID) {
		t.Error("свой пароль назван занятым")
	}
	if registry.PasswordInUse("free", "") {
		t.Error("свободный пароль назван занятым")
	}
	if !registry.PasswordInUse("other", alice.ID) {
		t.Error("пароль другого пользователя не найден")
	}
	if registry.PasswordInUse("other", bob.ID) {
		t.Error("свой пароль назван занятым")
	}
}

// TestNewPassword — пароль длинный, случайный и без символов, которые пришлось
// бы экранировать в ключе или URL.
func TestNewPassword(t *testing.T) {
	seen := make(map[string]bool)

	for i := 0; i < 50; i++ {
		password, err := users.NewPassword()
		if err != nil {
			t.Fatalf("NewPassword() = %v", err)
		}

		if len(password) < 20 {
			t.Errorf("пароль %q короче 20 символов", password)
		}
		if strings.ContainsAny(password, "+/= ") {
			t.Errorf("пароль %q содержит символ, требующий экранирования", password)
		}
		if seen[password] {
			t.Fatalf("пароль %q повторился", password)
		}

		seen[password] = true
	}
}
