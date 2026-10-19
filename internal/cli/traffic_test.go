package cli

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/metrics"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
	"github.com/vibecodinguser/hysteria-ward/internal/users"
)

// TestRecorderCountsOnlyAllowedTraffic — приёмник ядра пускает по списку
// пользователей и считает только пущенное: трафик выключенного сервер не
// обслуживает, и в статистику он попадать не должен.
func TestRecorderCountsOnlyAllowedTraffic(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)

	db, err := store.Open(filepath.Join(t.TempDir(), "hysteria-ward.db"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer db.Close()

	user, err := db.CreateUser(store.User{Name: "ноутбук", Password: "пароль", Enabled: true})
	if err != nil {
		t.Fatalf("CreateUser() = %v", err)
	}

	registry, err := users.New(db)
	if err != nil {
		t.Fatalf("users.New() = %v", err)
	}

	collector, err := metrics.New(db, logger)
	if err != nil {
		t.Fatalf("metrics.New() = %v", err)
	}

	recorder := recorder{users: registry, metrics: collector}

	if !recorder.Traffic(user.ID, 100, 200) {
		t.Fatal("включённого не пустили")
	}
	if got := collector.Sample(); got.Tx != 100 || got.Rx != 200 {
		t.Errorf("снимок = %d/%d, ожидались 100/200", got.Tx, got.Rx)
	}

	user.Enabled = false
	if _, err := db.UpdateUser(user); err != nil {
		t.Fatalf("UpdateUser() = %v", err)
	}
	if err := registry.Reload(); err != nil {
		t.Fatalf("Reload() = %v", err)
	}

	if recorder.Traffic(user.ID, 50, 50) {
		t.Fatal("выключенного пустили")
	}
	if got := collector.Sample(); got.Tx != 100 || got.Rx != 200 {
		t.Errorf("трафик выключенного посчитан: снимок = %d/%d", got.Tx, got.Rx)
	}
}
