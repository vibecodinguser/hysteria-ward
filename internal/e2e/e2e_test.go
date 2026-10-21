package e2e_test

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/e2e"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// healthAnswer — ответ проверки работоспособности.
type healthAnswer struct {
	Status string `json:"status"`
}

// statusAnswer — состояние панели для вошедшего.
type statusAnswer struct {
	Version     string `json:"version"`
	CoreRunning bool   `json:"core_running"`
}

// sessionAnswer — кто вошёл.
type sessionAnswer struct {
	Name string `json:"name"`
	CSRF string `json:"csrf"`
}

// userAnswer — пользователь, каким его видит интерфейс.
type userAnswer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// keyAnswer — ключ клиента.
type keyAnswer struct {
	URI string `json:"uri"`
}

// mcpKeyAnswer — выпущенный MCP-ключ.
type mcpKeyAnswer struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

// mcpAnswer — ответ JSON-RPC.
type mcpAnswer struct {
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// TestPanelRoundTrip — базовый сценарий: панель поднимается на свободном порту,
// отвечает на проверку работоспособности, закрывает данные без входа, пускает
// после входа и снова закрывает после выхода.
//
// Это тот же путь, который проходит администратор: HTTPS, форма входа, cookie
// сессии и защищённый эндпоинт. Если авторизация сломается, тест упадёт на
// первом же ожидании 401 или 200.
func TestPanelRoundTrip(t *testing.T) {
	panel := e2e.Start(t)

	// Проверка работоспособности открыта: по ней ждут готовности, а не читают
	// состояние сервера.
	health := panel.Get("/api/health").Expect(t, http.StatusOK)
	var healthBody healthAnswer
	health.Decode(t, &healthBody)
	if healthBody.Status != "ok" {
		t.Errorf("проверка работоспособности = %q, ожидалось ok", healthBody.Status)
	}

	// До входа панель данных не отдаёт.
	panel.Get("/api/status").Expect(t, http.StatusUnauthorized)

	// Интерфейс при этом отдаётся: иначе войти было бы негде.
	index := panel.Get("/").Expect(t, http.StatusOK)
	if !strings.Contains(string(index.Body), `id="app"`) {
		t.Errorf("точка входа интерфейса не отдана: %s", index.Body)
	}

	panel.Login()

	var session sessionAnswer
	panel.Get("/api/session").Expect(t, http.StatusOK).Decode(t, &session)
	if session.Name == "" || session.CSRF == "" {
		t.Errorf("сессия = %+v, ожидались имя и csrf-токен", session)
	}

	var status statusAnswer
	panel.Get("/api/status").Expect(t, http.StatusOK).Decode(t, &status)
	if status.Version == "" {
		t.Error("состояние без версии")
	}

	// Выход закрывает доступ: та же cookie после него ничего не даёт.
	panel.Post("/api/logout", "").Expect(t, http.StatusNoContent)
	panel.Get("/api/status").Expect(t, http.StatusUnauthorized)
}

// TestPanelsUseDifferentPorts — две панели поднимаются одновременно на разных
// свободных портах. Фиксированный порт здесь не годится: в CI его может занять
// что угодно, включая соседний тест.
func TestPanelsUseDifferentPorts(t *testing.T) {
	first := e2e.Start(t)
	second := e2e.Start(t)

	if first.BaseURL() == second.BaseURL() {
		t.Fatalf("обе панели поднялись на одном адресе: %s", first.BaseURL())
	}

	first.Get("/api/health").Expect(t, http.StatusOK)
	second.Get("/api/health").Expect(t, http.StatusOK)
}

// TestStopReleasesPanel — остановка завершает панель штатно и отпускает порт:
// после неё на том же адресе можно поднять новую панель, а временный каталог
// принадлежит тесту и удаляется вместе с ним.
func TestStopReleasesPanel(t *testing.T) {
	panel := e2e.Start(t)
	addr := strings.TrimPrefix(panel.BaseURL(), "https://")

	panel.Stop()

	// Повторная остановка безопасна: Cleanup вызовет её ещё раз.
	panel.Stop()

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("порт %s не освободился: %v", addr, err)
	}
	_ = listener.Close()

	if log := panel.Log(); !strings.Contains(log, "панель остановлена") {
		t.Errorf("в журнале нет записи об остановке: %q", log)
	}
}

// TestScenarioAddsUserAndKey — второй сценарий на том же каркасе: домен задан,
// пользователь заведён через интерфейс, ключ собран по текущему сертификату.
//
// Сценарий не трогает запуск и остановку панели — только вызывает их. Так
// проверяется, что новый сценарий добавляется без правок в каркасе.
func TestScenarioAddsUserAndKey(t *testing.T) {
	panel := e2e.Start(t, e2e.WithSetup(func(db *store.Store) {
		if err := db.SaveDomain("example.com"); err != nil {
			t.Fatalf("домен для e2e: %v", err)
		}
	}))

	panel.Login()

	var created userAnswer
	panel.Post("/api/users", `{"name":"ноутбук"}`).
		Expect(t, http.StatusCreated).
		Decode(t, &created)
	if created.ID == "" || created.Name != "ноутбук" {
		t.Fatalf("пользователь = %+v", created)
	}

	var key keyAnswer
	panel.Get("/api/users/"+url.PathEscape(created.ID)+"/key").
		Expect(t, http.StatusOK).
		Decode(t, &key)

	parsed, err := url.Parse(key.URI)
	if err != nil {
		t.Fatalf("ключ %q не разобран: %v", key.URI, err)
	}
	if parsed.Scheme != "hysteria2" {
		t.Errorf("схема ключа = %q, ожидалась hysteria2", parsed.Scheme)
	}
	// Порт в ключе есть, потому что ядро в тесте слушает не 443: ключ обязан
	// указывать туда, куда действительно можно подключиться.
	if parsed.Hostname() != "example.com" {
		t.Errorf("адрес в ключе = %q, ожидался example.com", parsed.Hostname())
	}
	if parsed.Port() == "" {
		t.Error("в ключе нет порта, хотя ядро слушает не 443")
	}

	query := parsed.Query()
	if query.Get("sni") != "example.com" {
		t.Errorf("SNI = %q, ожидался example.com", query.Get("sni"))
	}
	if query.Get("pinSHA256") == "" {
		t.Error("в ключе нет отпечатка сертификата")
	}
}

// TestScenarioMCPReadOnly — ключ MCP выпускается через интерфейс, проходит
// рукопожатие, перечисляет инструменты и отзывается. Сценарий идёт по настоящему
// HTTPS и проверяет, что MCP поднят вместе с панелью, а не только в модульном
// тесте обработчика.
func TestScenarioMCPReadOnly(t *testing.T) {
	panel := e2e.Start(t)
	panel.Login()

	var key mcpKeyAnswer
	panel.Post("/api/mcp/keys", `{"name":"agent"}`).
		Expect(t, http.StatusCreated).
		Decode(t, &key)
	if key.ID == "" || key.Token == "" {
		t.Fatalf("ключ MCP = %+v", key)
	}

	headers := map[string]string{
		"Authorization": "Bearer " + key.Token,
		"Content-Type":  "application/json",
	}

	var answer mcpAnswer
	panel.Do(http.MethodPost, "/mcp",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		headers).Expect(t, http.StatusOK).Decode(t, &answer)
	if len(answer.Error) != 0 {
		t.Fatalf("initialize вернул ошибку: %s", answer.Error)
	}
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(answer.Result, &initialized); err != nil {
		t.Fatalf("initialize не разобран: %v", err)
	}
	if initialized.ProtocolVersion != "2025-03-26" || initialized.ServerInfo.Name == "" {
		t.Errorf("initialize = %+v", initialized)
	}

	panel.Do(http.MethodPost, "/mcp", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, headers).
		Expect(t, http.StatusOK).Decode(t, &answer)
	var listed struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(answer.Result, &listed); err != nil {
		t.Fatalf("tools/list не разобран: %v", err)
	}
	if len(listed.Tools) == 0 {
		t.Error("tools/list пуст")
	}

	panel.Do(http.MethodDelete, "/api/mcp/keys/"+url.PathEscape(key.ID), "", nil).
		Expect(t, http.StatusNoContent)

	panel.Do(http.MethodPost, "/mcp", `{"jsonrpc":"2.0","id":3,"method":"ping"}`, headers).
		Expect(t, http.StatusUnauthorized)
}
