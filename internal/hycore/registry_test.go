package hycore_test

import (
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/apernet/hysteria/core/v2/client"

	"github.com/vibecodinguser/hysteria-ward/internal/hycore"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
	"github.com/vibecodinguser/hysteria-ward/internal/users"
)

// Тесты на стыке ядра и списка пользователей: панель не только решает, кого
// пускать, но и достаёт до тех, кто уже подключился. Проверяется это настоящим
// клиентом Hysteria 2 — другого способа убедиться, что соединение и правда
// рвётся, нет.

// registryTraffic — список пользователей в роли приёмника трафика. В бою эту роль
// играет cli.recorder: он ещё и пополняет статистику, а здесь важен только
// ответ «продолжать или рвать».
type registryTraffic struct {
	*users.Registry
}

func (r registryTraffic) Traffic(id string, tx, rx uint64) bool {
	_, ok := r.Registry.Traffic(id, tx, rx)

	return ok
}

// userRegistry заводит базу с одним пользователем и список поверх неё.
func userRegistry(t *testing.T, enabled bool) (*store.Store, store.User, *users.Registry) {
	t.Helper()

	db, err := store.Open(filepath.Join(t.TempDir(), "hysteria-ward.db"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	user, err := db.CreateUser(store.User{Name: "alice", Password: "s3cret", Enabled: enabled})
	if err != nil {
		t.Fatalf("CreateUser() = %v", err)
	}

	registry, err := users.New(db)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	return db, user, registry
}

// TestDisabledUserCannotConnect — выключенный пользователь не проходит
// авторизацию: ключ у клиента остался, а сервер его больше не знает.
func TestDisabledUserCannotConnect(t *testing.T) {
	db, user, registry := userRegistry(t, false)

	srv := startServer(t, hycore.Config{Authenticator: registry, Traffic: registryTraffic{registry}})

	cfg := trustingClient("s3cret")
	cfg.ServerAddr = srv.Addr()

	if c, _, err := client.NewClient(&cfg); err == nil {
		_ = c.Close()
		t.Fatal("выключенный пользователь подключился")
	}

	// Включили — и тот же ключ работает.
	user.Enabled = true
	if _, err := db.UpdateUser(user); err != nil {
		t.Fatalf("UpdateUser() = %v", err)
	}
	if err := registry.Reload(); err != nil {
		t.Fatalf("Reload() = %v", err)
	}

	c, _, err := client.NewClient(&cfg)
	if err != nil {
		t.Fatalf("включённый пользователь не подключился: %v", err)
	}
	_ = c.Close()
}

// TestDeletedUserIsDisconnected — удаление достаёт до активного соединения:
// следующий же пакет закрывает его. Так же работает и выключение — рычаг у них
// один, учёт трафика.
func TestDeletedUserIsDisconnected(t *testing.T) {
	db, user, registry := userRegistry(t, true)

	echoAddr, closeEcho := startEchoServer(t)
	defer closeEcho()

	srv := startServer(t, hycore.Config{Authenticator: registry, Traffic: registryTraffic{registry}})

	conn := dial(t, srv, trustingClient("s3cret"), echoAddr)
	roundTrip(t, conn, []byte("до удаления"))

	if err := db.DeleteUser(user.ID); err != nil {
		t.Fatalf("DeleteUser() = %v", err)
	}
	if err := registry.Reload(); err != nil {
		t.Fatalf("Reload() = %v", err)
	}

	// Первый пакет после удаления сервер уже не пропустит: соединение закрывается
	// с ошибкой, и клиент видит обрыв, а не тишину.
	if err := tryRoundTrip(conn, []byte("после удаления")); err == nil {
		t.Fatal("соединение удалённого пользователя осталось рабочим")
	}

	// И подключиться заново тем же ключом нельзя.
	cfg := trustingClient("s3cret")
	cfg.ServerAddr = srv.Addr()
	if c, _, err := client.NewClient(&cfg); err == nil {
		_ = c.Close()
		t.Fatal("удалённый пользователь подключился заново")
	}
}

// TestPasswordChangeDisconnects — смена пароля достаёт до активного соединения:
// учёт трафика видит, что версия пароля в строке соединения устарела, и рвёт его.
// Клиент переподключается уже новым ключом.
func TestPasswordChangeDisconnects(t *testing.T) {
	db, user, registry := userRegistry(t, true)

	echoAddr, closeEcho := startEchoServer(t)
	defer closeEcho()

	srv := startServer(t, hycore.Config{Authenticator: registry, Traffic: registryTraffic{registry}})

	conn := dial(t, srv, trustingClient("s3cret"), echoAddr)
	roundTrip(t, conn, []byte("до смены пароля"))

	user.Password = "новый"
	if _, err := db.UpdateUser(user); err != nil {
		t.Fatalf("UpdateUser() = %v", err)
	}
	if err := registry.Reload(); err != nil {
		t.Fatalf("Reload() = %v", err)
	}

	if err := tryRoundTrip(conn, []byte("после смены пароля")); err == nil {
		t.Fatal("соединение со старым паролем осталось рабочим")
	}

	// Новый ключ работает: отзыв не закрыл пользователя целиком.
	cfg := trustingClient("новый")
	cfg.ServerAddr = srv.Addr()
	fresh, _, err := client.NewClient(&cfg)
	if err != nil {
		t.Fatalf("новый пароль не подключился: %v", err)
	}
	_ = fresh.Close()
}

// tryRoundTrip отправляет байты и ждёт ответа, возвращая ошибку вместо падения
// теста: вызывающему нужно проверить именно обрыв.
func tryRoundTrip(conn net.Conn, payload []byte) error {
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	defer func() { _ = conn.SetDeadline(time.Time{}) }()

	if _, err := conn.Write(payload); err != nil {
		return err
	}

	got := make([]byte, len(payload))

	_, err := io.ReadFull(conn, got)

	return err
}
