package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/api"
)

// mcpKeyJSON — выпущенный ключ. Токен приходит только в ответе на выпуск.
type mcpKeyJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Token     string `json:"token"`
	CreatedAt string `json:"created_at"`
}

// mcpKeysJSON — список ключей без токенов.
type mcpKeysJSON struct {
	Keys []mcpKeyJSON `json:"keys"`
}

// mcpAnswer — минимальный разбор JSON-RPC ответа.
type mcpAnswer struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// mcpJSON отправляет JSON-RPC в MCP с Bearer-токеном и возвращает ответ.
func mcpJSON(t *testing.T, s *api.Server, token, body string, cookie *http.Cookie) (*http.Response, mcpAnswer) {
	t.Helper()

	headers := map[string]string{"Content-Type": "application/json"}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}

	resp := do(t, s, http.MethodPost, "/mcp", body, cookie, headers)
	var answer mcpAnswer
	if resp.StatusCode != http.StatusNoContent {
		answer = decode[mcpAnswer](t, resp)
	}

	return resp, answer
}

// TestMCPKeyLifecycle — ключ выпускается с токеном один раз, работает в MCP,
// отзывается и после отзыва не пускает. Список ключей токен не раскрывает.
func TestMCPKeyLifecycle(t *testing.T) {
	s := server(t)
	cookie, csrf := enter(t, s, "admin", "admin")

	resp := do(t, s, http.MethodPost, "/api/mcp/keys", `{"name":"agent"}`, cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("выпуск ключа: код %d, ожидался 201", resp.StatusCode)
	}
	key := decode[mcpKeyJSON](t, resp)
	if key.ID == "" || key.Name != "agent" {
		t.Errorf("ключ = %+v", key)
	}
	if !strings.HasPrefix(key.Token, "hwmcp_") {
		t.Fatalf("токен = %q, ожидался префикс hwmcp_", key.Token)
	}

	// Повторное имя не должно создавать второй ключ: человек их не различит.
	resp = do(t, s, http.MethodPost, "/api/mcp/keys", `{"name":"AGENT"}`, cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("повторное имя: код %d, ожидался 409", resp.StatusCode)
	}

	// Список отдаёт имя и идентификатор, но не токен.
	resp = do(t, s, http.MethodGet, "/api/mcp/keys", "", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("список ключей: код %d, ожидался 200", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("тело списка: %v", err)
	}
	if strings.Contains(string(raw), key.Token) {
		t.Error("список ключей раскрывает токен")
	}
	var list mcpKeysJSON
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("список ключей не разобран: %v", err)
	}
	if len(list.Keys) != 1 || list.Keys[0].ID != key.ID {
		t.Errorf("список ключей = %+v", list.Keys)
	}

	// Рукопожатие и список инструментов.
	resp, answer := mcpJSON(t, s, key.Token,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`, nil)
	if resp.StatusCode != http.StatusOK || answer.Error != nil {
		t.Fatalf("initialize: код %d, ответ %+v", resp.StatusCode, answer)
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

	resp, answer = mcpJSON(t, s, key.Token, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil)
	if resp.StatusCode != http.StatusOK || answer.Error != nil {
		t.Fatalf("tools/list: код %d, ответ %+v", resp.StatusCode, answer)
	}
	var tools struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(answer.Result, &tools); err != nil {
		t.Fatalf("tools/list не разобран: %v", err)
	}
	names := make(map[string]bool)
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"server_status", "users_list", "traffic_snapshot"} {
		if !names[want] {
			t.Errorf("нет инструмента %q", want)
		}
	}

	// Вызов read-only инструмента не должен вернуть пароль или клиентский ключ.
	resp, answer = mcpJSON(t, s, key.Token,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"users_list","arguments":{}}}`, nil)
	if resp.StatusCode != http.StatusOK || answer.Error != nil {
		t.Fatalf("tools/call: код %d, ответ %+v", resp.StatusCode, answer)
	}
	var called struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(answer.Result, &called); err != nil {
		t.Fatalf("tools/call не разобран: %v", err)
	}
	if called.IsError || len(called.Content) != 1 {
		t.Errorf("результат tools/call = %+v", called)
	}
	if strings.Contains(called.Content[0].Text, "password") || strings.Contains(called.Content[0].Text, "hysteria2://") {
		t.Errorf("users_list раскрывает секрет: %s", called.Content[0].Text)
	}

	// Cookie администратора без Bearer не должна открывать MCP: это отдельная
	// поверхность, а не второй вход в /api/.
	resp, _ = mcpJSON(t, s, "", `{"jsonrpc":"2.0","id":4,"method":"ping"}`, cookie)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("MCP с одной cookie: код %d, ожидался 401", resp.StatusCode)
	}

	// И наоборот: Bearer-ключ не пускает в /api/.
	resp = do(t, s, http.MethodGet, "/api/status", "", nil,
		map[string]string{"Authorization": "Bearer " + key.Token})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("API с MCP-ключом: код %d, ожидался 401", resp.StatusCode)
	}

	// Отзыв закрывает MCP.
	resp = do(t, s, http.MethodDelete, "/api/mcp/keys/"+key.ID, "", cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("отзыв ключа: код %d, ожидался 204", resp.StatusCode)
	}

	resp, _ = mcpJSON(t, s, key.Token, `{"jsonrpc":"2.0","id":5,"method":"ping"}`, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("MCP после отзыва: код %d, ожидался 401", resp.StatusCode)
	}

	resp = do(t, s, http.MethodDelete, "/api/mcp/keys/"+key.ID, "", cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("повторный отзыв: код %d, ожидался 404", resp.StatusCode)
	}
}

// TestMCPKeyRequiresCSRF — выпуск ключа не проходит без csrf-токена, даже с
// cookie администратора.
func TestMCPKeyRequiresCSRF(t *testing.T) {
	s := server(t)
	cookie, _ := enter(t, s, "admin", "admin")

	resp := do(t, s, http.MethodPost, "/api/mcp/keys", `{"name":"agent"}`, cookie, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("выпуск без csrf: код %d, ожидался 403", resp.StatusCode)
	}
}
