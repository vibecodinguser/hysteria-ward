package users_test

import (
	"testing"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/store"
	"github.com/vibecodinguser/hysteria-ward/internal/users"
)

// TestQuotaStopsAuthAndTraffic — предел сначала пускает, а на пакете, который
// выводит за него, рвёт соединение. Сброс возвращает доступ.
func TestQuotaStopsAuthAndTraffic(t *testing.T) {
	s := openStore(t)

	user, err := s.CreateUser(store.User{
		Name:       "alice",
		Password:   "s3cret",
		Enabled:    true,
		QuotaBytes: 100,
	})
	if err != nil {
		t.Fatalf("CreateUser() = %v", err)
	}

	registry, err := users.New(s)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	connection, ok := registry.Auth("s3cret")
	if !ok {
		t.Fatal("пользователь с неизрасходованной квотой не авторизуется")
	}

	if _, ok := registry.Traffic(connection, 60, 30); !ok {
		t.Fatal("трафик в пределах квоты не прошёл")
	}

	if _, ok := registry.Traffic(connection, 11, 0); ok {
		t.Fatal("трафик сверх квоты прошёл")
	}

	if _, ok := registry.Auth("s3cret"); ok {
		t.Fatal("пользователь с исчерпанной квотой всё ещё авторизуется")
	}

	used, limit, _ := registry.Quota(user.ID)
	if used != 100 || limit != 100 {
		t.Errorf("Quota() = %d/%d, ожидалось 100/100", used, limit)
	}

	if err := registry.ResetQuota(user.ID); err != nil {
		t.Fatalf("ResetQuota() = %v", err)
	}

	if _, ok := registry.Auth("s3cret"); !ok {
		t.Fatal("после сброса квоты пользователь не авторизуется")
	}

	used, _, _ = registry.Quota(user.ID)
	if used != 0 {
		t.Errorf("после сброса расход = %d, ожидался 0", used)
	}
}

// TestQuotaUsageSurvivesRestart — расход пишется в базу и читается новой
// панелью: перезапуск не должен дарить пользователю новый месяц.
func TestQuotaUsageSurvivesRestart(t *testing.T) {
	s := openStore(t)

	user, err := s.CreateUser(store.User{
		Name:       "alice",
		Password:   "s3cret",
		Enabled:    true,
		QuotaBytes: 1000,
	})
	if err != nil {
		t.Fatalf("CreateUser() = %v", err)
	}

	registry, err := users.New(s)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	connection, ok := registry.Auth("s3cret")
	if !ok {
		t.Fatal("Auth() = false")
	}
	if _, ok := registry.Traffic(connection, 100, 200); !ok {
		t.Fatal("Traffic() = false")
	}
	if err := registry.FlushQuotas(); err != nil {
		t.Fatalf("FlushQuotas() = %v", err)
	}

	restarted, err := users.New(s)
	if err != nil {
		t.Fatalf("New() после перезапуска = %v", err)
	}

	used, limit, _ := restarted.Quota(user.ID)
	if used != 300 || limit != 1000 {
		t.Errorf("после перезапуска Quota() = %d/%d, ожидалось 300/1000", used, limit)
	}
}

// TestQuotaResetsAtMonthBoundary — старый период не считается: первый день
// нового месяца начинается с нуля.
func TestQuotaResetsAtMonthBoundary(t *testing.T) {
	s := openStore(t)

	user, err := s.CreateUser(store.User{
		Name:       "alice",
		Password:   "s3cret",
		Enabled:    true,
		QuotaBytes: 1000,
	})
	if err != nil {
		t.Fatalf("CreateUser() = %v", err)
	}

	old := time.Now().UTC().AddDate(0, -1, 0)
	if err := s.SaveQuotaUsages(map[string]store.QuotaUsage{
		user.ID: {Used: 900, Since: time.Date(old.Year(), old.Month(), 1, 0, 0, 0, 0, time.UTC)},
	}); err != nil {
		t.Fatalf("SaveQuotaUsages() = %v", err)
	}

	registry, err := users.New(s)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	used, _, since := registry.Quota(user.ID)
	if used != 0 {
		t.Errorf("расход прошлого месяца = %d, ожидался 0", used)
	}
	if since.Month() != time.Now().UTC().Month() {
		t.Errorf("начало периода = %v, ожидался текущий месяц", since)
	}
}
