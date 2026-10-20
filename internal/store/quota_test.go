package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// TestQuotaUsageRoundTrip — расход переживает закрытие базы, а сброс обнуляет
// его: счётчик квоты не должен теряться при обновлении панели.
func TestQuotaUsageRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hysteria-ward.db")

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	since := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	if err := s.SaveQuotaUsages(map[string]store.QuotaUsage{
		"alice": {Used: 1234, Since: since},
	}); err != nil {
		t.Fatalf("SaveQuotaUsages() = %v", err)
	}

	usages, err := s.QuotaUsages()
	if err != nil {
		t.Fatalf("QuotaUsages() = %v", err)
	}
	if got := usages["alice"].Used; got != 1234 {
		t.Errorf("расход = %d, ожидался 1234", got)
	}
	if !usages["alice"].Since.Equal(since) {
		t.Errorf("начало периода = %v, ожидалось %v", usages["alice"].Since, since)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer reopened.Close()

	usages, err = reopened.QuotaUsages()
	if err != nil {
		t.Fatalf("QuotaUsages() = %v", err)
	}
	if got := usages["alice"].Used; got != 1234 {
		t.Errorf("после перезапуска расход = %d, ожидался 1234", got)
	}

	if err := reopened.ResetQuota("alice", since); err != nil {
		t.Fatalf("ResetQuota() = %v", err)
	}

	usages, err = reopened.QuotaUsages()
	if err != nil {
		t.Fatalf("QuotaUsages() = %v", err)
	}
	if got := usages["alice"].Used; got != 0 {
		t.Errorf("после сброса расход = %d, ожидался 0", got)
	}
}

// TestDeleteUserDropsQuota — удалённый пользователь не оставляет расход, который
// потом попал бы новому пользователю с тем же случайным идентификатором.
func TestDeleteUserDropsQuota(t *testing.T) {
	s := openStore(t)

	user, err := s.CreateUser(store.User{Name: "alice", Password: "s3cret", Enabled: true})
	if err != nil {
		t.Fatalf("CreateUser() = %v", err)
	}

	if err := s.SaveQuotaUsages(map[string]store.QuotaUsage{
		user.ID: {Used: 10, Since: time.Now()},
	}); err != nil {
		t.Fatalf("SaveQuotaUsages() = %v", err)
	}

	if err := s.DeleteUser(user.ID); err != nil {
		t.Fatalf("DeleteUser() = %v", err)
	}

	usages, err := s.QuotaUsages()
	if err != nil {
		t.Fatalf("QuotaUsages() = %v", err)
	}
	if _, found := usages[user.ID]; found {
		t.Error("расход удалённого пользователя остался в базе")
	}
}
