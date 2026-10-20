package api_test

import (
	"net/http"
	"testing"
)

// healthJSON — ответ проверки работоспособности.
type healthJSON struct {
	Status string `json:"status"`
}

// TestHealthIsOpenWithoutLogin — проверка работоспособности доступна без входа:
// по ней установщик, обновление и loopback-e2e ждут готовности панели. Никаких
// данных о сервере она не отдаёт.
func TestHealthIsOpenWithoutLogin(t *testing.T) {
	s := server(t)

	resp := do(t, s, http.MethodGet, "/api/health", "", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("проверка работоспособности = %d, ожидался 200", resp.StatusCode)
	}

	answer := decode[healthJSON](t, resp)
	if answer.Status != "ok" {
		t.Errorf("статус = %q, ожидался ok", answer.Status)
	}
}

// TestHealthDoesNotReplaceAuth — проверка работоспособности не открывает
// защищённые эндпоинты: без сессии они по-прежнему отвечают 401.
func TestHealthDoesNotReplaceAuth(t *testing.T) {
	s := server(t)

	health := do(t, s, http.MethodGet, "/api/health", "", nil, nil)
	if health.StatusCode != http.StatusOK {
		t.Fatalf("проверка работоспособности = %d, ожидался 200", health.StatusCode)
	}

	resp := do(t, s, http.MethodGet, "/api/status", "", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("состояние без входа = %d, ожидался 401", resp.StatusCode)
	}
}
