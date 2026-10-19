package api_test

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/api"
	"github.com/vibecodinguser/hysteria-ward/internal/authn"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// panelAnswer — ответ о доступе к панели.
type panelAnswer struct {
	Access string `json:"access"`
	Addr   string `json:"addr"`
}

// enter входит в панель и возвращает cookie сессии и csrf-токен: без второго
// изменяющий запрос не пройдёт.
func enter(t *testing.T, s *api.Server, name, password string) (*http.Cookie, string) {
	t.Helper()

	body := fmt.Sprintf(`{"name":%q,"password":%q}`, name, password)
	resp := do(t, s, http.MethodPost, "/api/login", body, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("вход: код %d, ожидался 200", resp.StatusCode)
	}

	return sessionCookie(t, resp), decode[sessionAnswer](t, resp).CSRF
}

// changePassword меняет пароль администратора в обход passwd: тесту нужен вход,
// который не считается паролем по умолчанию.
func changePassword(t *testing.T, opts api.Options) {
	t.Helper()

	hash, err := authn.Hash("новый пароль")
	if err != nil {
		t.Fatalf("Hash() = %v", err)
	}

	if err := opts.Auth.SetCredential(authn.Credential{Name: authn.DefaultName, Hash: hash}); err != nil {
		t.Fatalf("SetCredential() = %v", err)
	}
}

// TestSwitchesAccessWithoutRestart — публичный доступ включается и выключается на
// ходу: панель не перезапускается, а перевешивает слушатель.
//
// Запрос, которым пришла команда, при этом доходит до конца — он идёт по уже
// установленному соединению, а закрывается только дверь для новых.
func TestSwitchesAccessWithoutRestart(t *testing.T) {
	opts := options(t)
	changePassword(t, opts)

	s := start(t, opts)
	cookie, csrf := enter(t, s, authn.DefaultName, "новый пароль")

	// Новая панель спрятана на петле: снаружи её не видно.
	answer := decode[panelAnswer](t, do(t, s, http.MethodGet, "/api/panel", "", cookie, nil))
	if answer.Access != string(store.AccessTunnel) {
		t.Fatalf("режим = %q, ожидался %q", answer.Access, store.AccessTunnel)
	}
	if host := hostOf(t, answer.Addr); host != "127.0.0.1" {
		t.Errorf("панель слушает %q, ожидалась петля", answer.Addr)
	}

	resp := do(t, s, http.MethodPost, "/api/panel/access", `{"access":"public"}`, cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("включение публичного доступа: код %d, ожидался 200", resp.StatusCode)
	}

	answer = decode[panelAnswer](t, resp)
	if answer.Access != string(store.AccessPublic) {
		t.Errorf("режим = %q, ожидался %q", answer.Access, store.AccessPublic)
	}
	if host := hostOf(t, answer.Addr); host != "0.0.0.0" {
		t.Errorf("панель слушает %q, ожидались все интерфейсы", answer.Addr)
	}

	// Выбор записан в базу: после перезапуска панель встанет так же.
	settings, err := opts.Store.PanelSettings()
	if err != nil {
		t.Fatalf("PanelSettings() = %v", err)
	}
	if settings.Access != store.AccessPublic {
		t.Errorf("в базе режим %q, ожидался %q", settings.Access, store.AccessPublic)
	}

	// Панель работает дальше — тем же соединением, если клиент его переиспользует.
	if resp := do(t, s, http.MethodGet, "/api/session", "", cookie, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("запрос после переключения: код %d, ожидался 200", resp.StatusCode)
	}

	resp = do(t, s, http.MethodPost, "/api/panel/access", `{"access":"tunnel"}`, cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("выключение публичного доступа: код %d, ожидался 200", resp.StatusCode)
	}

	answer = decode[panelAnswer](t, resp)
	if answer.Access != string(store.AccessTunnel) {
		t.Errorf("режим = %q, ожидался %q", answer.Access, store.AccessTunnel)
	}
	if host := hostOf(t, answer.Addr); host != "127.0.0.1" {
		t.Errorf("панель слушает %q, ожидалась петля", answer.Addr)
	}
}

// TestPublicAccessWaitsForPasswordChange — с паролем по умолчанию панель наружу не
// открывается: этот пароль знает кто угодно, он написан в документации.
func TestPublicAccessWaitsForPasswordChange(t *testing.T) {
	s := server(t)
	cookie, csrf := enter(t, s, authn.DefaultName, authn.DefaultPassword)

	resp := do(t, s, http.MethodPost, "/api/panel/access", `{"access":"public"}`, cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("включение публичного доступа: код %d, ожидался 409", resp.StatusCode)
	}

	if s.Access() != store.AccessTunnel {
		t.Error("панель открыта наружу, хотя пароль по умолчанию не сменён")
	}
}

// TestTunnelIsInvisibleFromOutside — в режиме туннеля панель слушает петлю, и по
// внешнему адресу машины к ней не подключиться. Это и есть «снаружи её не видно»,
// проверенное попыткой подключения, а не чтением настройки.
func TestTunnelIsInvisibleFromOutside(t *testing.T) {
	s := server(t)
	outside := outsideAddr(t)

	address := net.JoinHostPort(outside, strconv.Itoa(portOf(t, s.Addr().String())))

	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("панель ответила по внешнему адресу %q, хотя должна слушать только петлю", address)
	}
}

// hostOf достаёт хост из адреса.
func hostOf(t *testing.T, addr string) string {
	t.Helper()

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("адрес %q не разобран: %v", addr, err)
	}

	return host
}

// outsideAddr — адрес этой машины, кроме петли: по нему панель видно снаружи.
func outsideAddr(t *testing.T) string {
	t.Helper()

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("адреса машины: %v", err)
	}

	for _, addr := range addrs {
		ip, _, err := net.ParseCIDR(addr.String())
		if err != nil || ip.IsLoopback() || ip.To4() == nil {
			continue
		}

		return ip.String()
	}

	t.Skip("у машины нет адреса, кроме петли: снаружи проверять нечего")

	return ""
}
