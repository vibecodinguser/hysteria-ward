package api_test

import (
	"bytes"
	"fmt"
	"image/png"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/api"
	"github.com/vibecodinguser/hysteria-ward/internal/keys"
)

// userJSON — пользователь, каким его отдаёт панель: пароля здесь нет.
type userJSON struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Enabled   bool      `json:"enabled"`
	Online    bool      `json:"online"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// usersJSON — список пользователей.
type usersJSON struct {
	Users []userJSON `json:"users"`
}

// keyJSON — ссылка клиента.
type keyJSON struct {
	URI string `json:"uri"`
}

// serverJSON — настройки, нужные клиентам.
type serverJSON struct {
	Domain string `json:"domain"`
}

// createUser заводит пользователя и возвращает его, каким его увидел интерфейс.
func createUser(t *testing.T, s *api.Server, cookie *http.Cookie, csrf, name string) userJSON {
	t.Helper()

	resp := do(t, s, http.MethodPost, "/api/users", fmt.Sprintf(`{"name":%q}`, name), cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("создание пользователя: код %d, ожидался 201", resp.StatusCode)
	}

	return decode[userJSON](t, resp)
}

// listUsers перечисляет пользователей.
func listUsers(t *testing.T, s *api.Server, cookie *http.Cookie) []userJSON {
	t.Helper()

	resp := do(t, s, http.MethodGet, "/api/users", "", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("список пользователей: код %d, ожидался 200", resp.StatusCode)
	}

	return decode[usersJSON](t, resp).Users
}

// userKey спрашивает ключ пользователя.
func userKey(t *testing.T, s *api.Server, cookie *http.Cookie, id string) string {
	t.Helper()

	resp := do(t, s, http.MethodGet, "/api/users/"+url.PathEscape(id)+"/key", "", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ключ пользователя: код %d, ожидался 200", resp.StatusCode)
	}

	return decode[keyJSON](t, resp).URI
}

// TestUserLifecycle — пользователя заводят, переименовывают, выключают и удаляют,
// а ключ выдаётся в любой момент: пароль лежит в базе обратимо (ADR-0006).
func TestUserLifecycle(t *testing.T) {
	opts := options(t)
	if err := opts.Store.SaveDomain("example.com"); err != nil {
		t.Fatalf("SaveDomain() = %v", err)
	}

	s := start(t, opts)
	cookie, csrf := enter(t, s, "admin", "admin")

	if users := listUsers(t, s, cookie); len(users) != 0 {
		t.Fatalf("сначала пользователей %d, ожидалось 0", len(users))
	}

	alice := createUser(t, s, cookie, csrf, "alice")
	if alice.ID == "" {
		t.Fatal("панель не выдала идентификатор")
	}
	if !alice.Enabled {
		t.Error("заведённый пользователь сразу выключен")
	}

	users := listUsers(t, s, cookie)
	if len(users) != 1 || users[0].Name != "alice" {
		t.Fatalf("список = %+v, ожидался один alice", users)
	}

	// Ключ собирается из того, что лежит в базе: пароль — тот же, что выдан
	// пользователю, отпечаток — от сертификата панели.
	stored, found, err := opts.Store.User(alice.ID)
	if err != nil || !found {
		t.Fatalf("User() = found %v, err %v", found, err)
	}
	if len(stored.Password) < 20 {
		t.Errorf("пароль %q короткий", stored.Password)
	}

	pin, err := keys.Pin(opts.Certificate)
	if err != nil {
		t.Fatalf("Pin() = %v", err)
	}

	uri := userKey(t, s, cookie, alice.ID)
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("ключ %q не разбирается: %v", uri, err)
	}
	if parsed.Scheme != "hysteria2" {
		t.Errorf("схема = %q, ожидалась hysteria2", parsed.Scheme)
	}
	if parsed.User.Username() != stored.Password {
		t.Errorf("пароль в ключе = %q, ожидался выданный", parsed.User.Username())
	}
	if parsed.Host != "example.com" {
		t.Errorf("адрес в ключе = %q, ожидался example.com", parsed.Host)
	}
	if got := parsed.Query().Get("sni"); got != "example.com" {
		t.Errorf("sni = %q, ожидался example.com", got)
	}
	if got := parsed.Query().Get("pinSHA256"); got != pin {
		t.Errorf("отпечаток в ключе = %q, ожидался %q", got, pin)
	}

	// Переименование не меняет ни идентификатор, ни пароль: ключ остаётся рабочим.
	resp := do(t, s, http.MethodPatch, "/api/users/"+url.PathEscape(alice.ID), `{"name":"алиса"}`, cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("переименование: код %d, ожидался 200", resp.StatusCode)
	}
	renamed := decode[userJSON](t, resp)
	if renamed.Name != "алиса" || renamed.ID != alice.ID {
		t.Errorf("после переименования = %+v", renamed)
	}
	if got := userKey(t, s, cookie, alice.ID); got != uri {
		t.Error("после переименования ключ изменился")
	}

	// Выключение видно и в списке, и ядру: список в памяти перечитывается на
	// каждой правке.
	resp = do(t, s, http.MethodPatch, "/api/users/"+url.PathEscape(alice.ID), `{"enabled":false}`, cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("выключение: код %d, ожидался 200", resp.StatusCode)
	}
	if decode[userJSON](t, resp).Enabled {
		t.Error("пользователь остался включённым")
	}
	if _, ok := opts.Users.Auth(stored.Password); ok {
		t.Error("ядро всё ещё пускает выключенного пользователя")
	}

	// Удаление: пользователя больше нет ни в списке, ни у ядра.
	resp = do(t, s, http.MethodDelete, "/api/users/"+url.PathEscape(alice.ID), "", cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("удаление: код %d, ожидался 204", resp.StatusCode)
	}
	if users := listUsers(t, s, cookie); len(users) != 0 {
		t.Errorf("после удаления список = %+v", users)
	}
	if _, ok := opts.Users.Auth(stored.Password); ok {
		t.Error("ядро всё ещё пускает удалённого пользователя")
	}

	// Ключ удалённого не выдаётся: выдавать нечего.
	resp = do(t, s, http.MethodGet, "/api/users/"+url.PathEscape(alice.ID)+"/key", "", cookie, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("ключ удалённого: код %d, ожидался 404", resp.StatusCode)
	}
}

// TestUserPasswordIsRegenerated — новый пароль заменяет старый: прежний ключ
// перестаёт работать, а новый выдаётся панелью.
func TestUserPasswordIsRegenerated(t *testing.T) {
	opts := options(t)
	if err := opts.Store.SaveDomain("example.com"); err != nil {
		t.Fatalf("SaveDomain() = %v", err)
	}

	s := start(t, opts)
	cookie, csrf := enter(t, s, "admin", "admin")

	alice := createUser(t, s, cookie, csrf, "alice")
	before := userKey(t, s, cookie, alice.ID)

	resp := do(t, s, http.MethodPost, "/api/users/"+url.PathEscape(alice.ID)+"/password", "", cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("смена пароля: код %d, ожидался 200", resp.StatusCode)
	}

	after := userKey(t, s, cookie, alice.ID)
	if after == before {
		t.Fatal("после смены пароля ключ остался прежним")
	}

	stored, _, err := opts.Store.User(alice.ID)
	if err != nil {
		t.Fatalf("User() = %v", err)
	}
	if _, ok := opts.Users.Auth(stored.Password); !ok {
		t.Error("ядро не принимает новый пароль")
	}
}

// TestUserKeyShowsQR — ключ отдаётся картинкой: клиенты на телефоне читают её
// камерой.
func TestUserKeyShowsQR(t *testing.T) {
	opts := options(t)
	if err := opts.Store.SaveDomain("example.com"); err != nil {
		t.Fatalf("SaveDomain() = %v", err)
	}

	s := start(t, opts)
	cookie, csrf := enter(t, s, "admin", "admin")

	alice := createUser(t, s, cookie, csrf, "alice")

	resp := do(t, s, http.MethodGet, "/api/users/"+url.PathEscape(alice.ID)+"/qr", "", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("QR-код: код %d, ожидался 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "image/png" {
		t.Errorf("тип содержимого = %q, ожидался image/png", got)
	}

	picture, err := png.Decode(resp.Body)
	if err != nil {
		t.Fatalf("картинка не разбирается: %v", err)
	}

	bounds := picture.Bounds()
	if bounds.Dx() < 200 || bounds.Dy() != bounds.Dx() {
		t.Fatalf("картинка %dx%d, ожидался квадрат не меньше 200 точек", bounds.Dx(), bounds.Dy())
	}

	// В коде есть и тёмные, и светлые точки: однотонная картинка не читается.
	dark := false
	light := false
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, _, _, _ := picture.At(x, y).RGBA()
			if r < 0x8000 {
				dark = true
			} else {
				light = true
			}
		}
	}
	if !dark || !light {
		t.Errorf("в картинке нет ни тёмных, ни светлых точек: тёмные — %v, светлые — %v", dark, light)
	}

	// По краям — белое поле: без него сканер не находит границы кода.
	if r, _, _, _ := picture.At(bounds.Min.X, bounds.Min.Y).RGBA(); r < 0x8000 {
		t.Error("у QR-кода нет белого поля по краям")
	}
}

// TestUsersRequireLoginAndCSRF — список и правки — это данные панели: без входа
// их не видно, а без csrf-токена их не меняют.
func TestUsersRequireLoginAndCSRF(t *testing.T) {
	s := server(t)

	resp := do(t, s, http.MethodGet, "/api/users", "", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("список без входа: код %d, ожидался 401", resp.StatusCode)
	}

	cookie := login(t, s)

	resp = do(t, s, http.MethodPost, "/api/users", `{"name":"alice"}`, cookie, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("создание без csrf-токена: код %d, ожидался 403", resp.StatusCode)
	}

	resp = do(t, s, http.MethodDelete, "/api/users/никого", "", cookie, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("удаление без csrf-токена: код %d, ожидался 403", resp.StatusCode)
	}
}

// TestCreateUserRejectsBadNames — имя нужно людям, но пустое или невидимое имя
// сделало бы список бесполезным.
func TestCreateUserRejectsBadNames(t *testing.T) {
	s := server(t)
	cookie, csrf := enter(t, s, "admin", "admin")

	for _, name := range []string{"", "   ", "имя\nв две строки", strings.Repeat("я", 65)} {
		resp := do(t, s, http.MethodPost, "/api/users", fmt.Sprintf(`{"name":%q}`, name), cookie,
			map[string]string{"X-CSRF-Token": csrf})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("имя %q: код %d, ожидался 400", name, resp.StatusCode)
		}
	}

	createUser(t, s, cookie, csrf, "alice")

	// Второй пользователь с тем же именем запутал бы того, кто выдаёт ключи.
	resp := do(t, s, http.MethodPost, "/api/users", `{"name":"Alice"}`, cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("повтор имени: код %d, ожидался 409", resp.StatusCode)
	}
}

// TestKeyWaitsForDomain — без домена в ключе нет ни адреса, ни SNI: панель
// объясняет это, а не выдаёт ссылку в никуда.
func TestKeyWaitsForDomain(t *testing.T) {
	s := server(t)
	cookie, csrf := enter(t, s, "admin", "admin")

	alice := createUser(t, s, cookie, csrf, "alice")

	resp := do(t, s, http.MethodGet, "/api/users/"+url.PathEscape(alice.ID)+"/key", "", cookie, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("ключ без домена: код %d, ожидался 409", resp.StatusCode)
	}

	resp = do(t, s, http.MethodPut, "/api/server", `{"domain":"example.com"}`, cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("запись домена: код %d, ожидался 200", resp.StatusCode)
	}
	if domain := decode[serverJSON](t, resp).Domain; domain != "example.com" {
		t.Errorf("домен = %q, ожидался example.com", domain)
	}

	if uri := userKey(t, s, cookie, alice.ID); !strings.HasPrefix(uri, "hysteria2://") {
		t.Errorf("ключ = %q, ожидалась ссылка hysteria2://", uri)
	}
}

// TestServerDomainIsValidated — домен проверяется на входе: в ключ должно попадать
// имя, а не адрес со схемой и портом.
func TestServerDomainIsValidated(t *testing.T) {
	s := server(t)
	cookie, csrf := enter(t, s, "admin", "admin")

	resp := do(t, s, http.MethodGet, "/api/server", "", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("чтение домена: код %d, ожидался 200", resp.StatusCode)
	}
	if domain := decode[serverJSON](t, resp).Domain; domain != "" {
		t.Errorf("домен = %q, ожидалась пустая строка", domain)
	}

	for _, domain := range []string{"https://example.com", "example.com:443", "example.com/panel"} {
		resp := do(t, s, http.MethodPut, "/api/server", fmt.Sprintf(`{"domain":%q}`, domain), cookie,
			map[string]string{"X-CSRF-Token": csrf})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("домен %q: код %d, ожидался 400", domain, resp.StatusCode)
		}
	}

	// Домен записан в нормальном виде: в базе он в нижнем регистре.
	resp = do(t, s, http.MethodPut, "/api/server", `{"domain":" Example.COM "}`, cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("запись домена: код %d, ожидался 200", resp.StatusCode)
	}
	if domain := decode[serverJSON](t, resp).Domain; domain != "example.com" {
		t.Errorf("домен = %q, ожидался example.com", domain)
	}

	resp = do(t, s, http.MethodPut, "/api/server", `{"domain":""}`, cookie, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("запись домена без csrf-токена: код %d, ожидался 403", resp.StatusCode)
	}

	// Ответ на запрос без тела — понятная ошибка, а не молчание.
	resp = do(t, s, http.MethodPut, "/api/server", "", cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("запись без тела: код %d, ожидался 400", resp.StatusCode)
	}
	if body := readBody(t, resp); !bytes.Contains([]byte(body), []byte("тело запроса")) {
		t.Errorf("ответ = %q, ожидалось объяснение про тело запроса", body)
	}
}

// readBody читает тело ответа целиком.
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("тело ответа: %v", err)
	}

	return buf.String()
}
