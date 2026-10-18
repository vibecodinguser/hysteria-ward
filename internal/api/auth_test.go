package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/api"
)

// Тесты входа: панель отдаёт данные только вошедшему, а менять что-либо даёт
// только своему интерфейсу.
//
// Сроки жизни сессии здесь не проверяются — для этого нужны подменяемые часы, и
// живут они в тестах authn. Здесь проверяется другое: что без сессии панель
// молчит, а с ней — работает.

// sessionCookieName — имя cookie, которым панель узнаёт вошедшего.
const sessionCookieName = "hysteria_ward_session"

// sessionAnswer — ответ о своём входе.
type sessionAnswer struct {
	Name string `json:"name"`
	CSRF string `json:"csrf"`
}

// statusAnswer — ответ о состоянии панели.
type statusAnswer struct {
	Version     string `json:"version"`
	CoreRunning bool   `json:"core_running"`
}

// errorAnswer — ответ с ошибкой.
type errorAnswer struct {
	Error string `json:"error"`
}

// do отправляет запрос к панели. Пустое тело — запрос без тела, nil вместо cookie
// — запрос без сессии.
func do(t *testing.T, s *api.Server, method, path, body string, cookie *http.Cookie, headers map[string]string) *http.Response {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, "https://"+s.Addr().String()+path, reader)
	if err != nil {
		t.Fatalf("запрос %s %s: %v", method, path, err)
	}

	if cookie != nil {
		req.AddCookie(cookie)
	}

	for name, value := range headers {
		req.Header.Set(name, value)
	}

	resp, err := client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	return resp
}

// login входит в панель паролем по умолчанию — тем самым, который заводится при
// первом запуске, — и возвращает cookie сессии.
func login(t *testing.T, s *api.Server) *http.Cookie {
	t.Helper()

	resp := do(t, s, http.MethodPost, "/api/login", `{"name":"admin","password":"admin"}`, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("вход: код %d, ожидался 200", resp.StatusCode)
	}

	return sessionCookie(t, resp)
}

// sessionCookie достаёт cookie сессии из ответа.
func sessionCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()

	for _, cookie := range resp.Cookies() {
		if cookie.Name == sessionCookieName {
			return cookie
		}
	}

	t.Fatal("в ответе нет cookie сессии")

	return nil
}

// decode разбирает JSON-ответ.
func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()

	var value T
	if err := json.NewDecoder(resp.Body).Decode(&value); err != nil {
		t.Fatalf("разбор ответа: %v", err)
	}

	return value
}

// TestInterfaceIsOpenWithoutLogin — интерфейс отдаётся и без входа: иначе браузер
// не показал бы и формы входа. Данных в нём нет — они за /api/.
func TestInterfaceIsOpenWithoutLogin(t *testing.T) {
	s := server(t)

	resp := do(t, s, http.MethodGet, "/", "", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код ответа = %d, ожидался 200", resp.StatusCode)
	}

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("тело ответа: %v", err)
	}
	if string(content) != response {
		t.Errorf("тело ответа = %q, ожидалось %q", content, response)
	}
}

// TestProtectedEndpointsNeedLogin — без сессии панель не рассказывает ничего:
// ни кто вошёл, ни что с ядром. Сессия при этом проверяется в базе, поэтому
// выдуманная cookie не годится.
func TestProtectedEndpointsNeedLogin(t *testing.T) {
	s := server(t)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/session"},
		{http.MethodGet, "/api/status"},
		{http.MethodPost, "/api/logout"},
	}

	for _, c := range cases {
		for name, cookie := range map[string]*http.Cookie{
			"без cookie":          nil,
			"с выдуманной cookie": {Name: sessionCookieName, Value: "выдуманная"},
		} {
			resp := do(t, s, c.method, c.path, "", cookie, nil)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s %s (%s): код %d, ожидался 401", c.method, c.path, name, resp.StatusCode)
				continue
			}

			if answer := decode[errorAnswer](t, resp); answer.Error == "" {
				t.Errorf("%s %s (%s): отказ без объяснения", c.method, c.path, name)
			}
		}
	}
}

// TestLoginLogoutCycle — полный круг: вход, работа под сессией, выход и отказ
// после выхода.
func TestLoginLogoutCycle(t *testing.T) {
	s := server(t)

	// Неверный пароль не заводит сессию.
	resp := do(t, s, http.MethodPost, "/api/login", `{"name":"admin","password":"не тот"}`, nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("вход с неверным паролем: код %d, ожидался 401", resp.StatusCode)
	}

	session := login(t, s)

	// Cookie не должна достаться скрипту и не должна уходить по открытому
	// каналу: панель отдаётся только по HTTPS.
	if !session.HttpOnly {
		t.Error("cookie сессии не HttpOnly: до неё дотянется скрипт на странице")
	}
	if !session.Secure {
		t.Error("cookie сессии не Secure: она уйдёт и по открытому каналу")
	}
	if session.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, ожидался Strict", session.SameSite)
	}

	// Под сессией панель работает.
	resp = do(t, s, http.MethodGet, "/api/session", "", session, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("своя сессия: код %d, ожидался 200", resp.StatusCode)
	}

	entered := decode[sessionAnswer](t, resp)
	if entered.Name != "admin" {
		t.Errorf("имя = %q, ожидалось %q", entered.Name, "admin")
	}
	if entered.CSRF == "" {
		t.Fatal("csrf-токен не выдан")
	}

	resp = do(t, s, http.MethodGet, "/api/status", "", session, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("состояние: код %d, ожидался 200", resp.StatusCode)
	}

	status := decode[statusAnswer](t, resp)
	if status.Version == "" {
		t.Error("панель не сказала своей версии")
	}

	// Изменяющий запрос без csrf-токена не проходит: cookie чужой сайт пришлёт,
	// а заголовок подставить не сможет.
	resp = do(t, s, http.MethodPost, "/api/logout", "", session, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("выход без csrf-токена: код %d, ожидался 403", resp.StatusCode)
	}

	resp = do(t, s, http.MethodPost, "/api/logout", "", session, map[string]string{"X-CSRF-Token": "чужой"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("выход с чужим csrf-токеном: код %d, ожидался 403", resp.StatusCode)
	}

	// Сессия на месте: отказ не должен её закрывать.
	resp = do(t, s, http.MethodGet, "/api/session", "", session, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("сессия после отказа: код %d, ожидался 200", resp.StatusCode)
	}

	resp = do(t, s, http.MethodPost, "/api/logout", "", session, map[string]string{"X-CSRF-Token": entered.CSRF})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("выход: код %d, ожидался 204", resp.StatusCode)
	}

	cleared := sessionCookie(t, resp)
	if cleared.Value != "" || cleared.MaxAge >= 0 {
		t.Errorf("cookie после выхода = %q (Max-Age %d), ожидалась пустая с истёкшим сроком", cleared.Value, cleared.MaxAge)
	}

	// После выхода та же cookie больше ничего не открывает.
	resp = do(t, s, http.MethodGet, "/api/session", "", session, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("после выхода: код %d, ожидался 401", resp.StatusCode)
	}
}

// TestLoginRejectsBrokenRequest — мусор вместо запроса на вход не должен валить
// панель и не должен пускать внутрь.
func TestLoginRejectsBrokenRequest(t *testing.T) {
	s := server(t)

	for name, body := range map[string]string{
		"не JSON":     "просто строка",
		"пустой":      "",
		"чужая схема": `["admin","admin"]`,
	} {
		resp := do(t, s, http.MethodPost, "/api/login", body, nil, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("вход (%s): код %d, ожидался 400", name, resp.StatusCode)
		}
	}
}

// TestUnknownAPIPathIsNotFound — опечатка в запросе должна выглядеть как ошибка, а
// не как страница интерфейса.
func TestUnknownAPIPathIsNotFound(t *testing.T) {
	s := server(t)

	resp := do(t, s, http.MethodGet, "/api/такого-нет", "", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("код ответа = %d, ожидался 404", resp.StatusCode)
	}

	if answer := decode[errorAnswer](t, resp); answer.Error == "" {
		t.Error("ответ без объяснения")
	}
}
