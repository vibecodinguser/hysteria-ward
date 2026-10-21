package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/mcp"
)

// handler собирает MCP с одним инструментом, который возвращает аргументы.
func handler(t *testing.T) *mcp.Handler {
	t.Helper()

	h, err := mcp.New(mcp.Options{
		Authenticate: func(token string) (bool, error) {
			if token == "bad-error" {
				return false, errors.New("база недоступна")
			}

			return token == "secret", nil
		},
		Tools: []mcp.Tool{{
			Name:        "echo",
			Description: "возвращает аргументы",
			InputSchema: map[string]any{"type": "object"},
		}},
		Call: func(_ context.Context, name string, arguments json.RawMessage) (any, error) {
			if name != "echo" {
				return nil, errors.New("неизвестный инструмент")
			}
			if string(arguments) == `{"fail":true}` {
				return nil, errors.New("не получилось")
			}

			return json.RawMessage(arguments), nil
		},
		ServerName:    "test",
		ServerVersion: "v1",
		Logger:        slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	return h
}

// call отправляет JSON-RPC и возвращает ответ.
func call(t *testing.T, h *mcp.Handler, token, method string, id any, params any) (*http.Response, map[string]any) {
	t.Helper()

	body := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		body["id"] = id
	}
	if params != nil {
		body["params"] = params
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(string(raw)))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	resp := rec.Result()
	t.Cleanup(func() { _ = resp.Body.Close() })

	var answer map[string]any
	if resp.Header.Get("Content-Type") == "application/json; charset=utf-8" {
		if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("ответ не разобран: %v", err)
		}
	}

	return resp, answer
}

// TestMCPRequiresBearer — без ключа и с чужим ключом MCP не отвечает.
func TestMCPRequiresBearer(t *testing.T) {
	h := handler(t)

	for _, token := range []string{"", "чужой"} {
		resp, answer := call(t, h, token, "initialize", 1, map[string]any{})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("токен %q: код %d, ожидался 401", token, resp.StatusCode)
		}
		if answer["error"] == nil {
			t.Errorf("токен %q: нет JSON-RPC ошибки: %+v", token, answer)
		}
	}
}

// TestMCPInitialize — рукопожатие возвращает версию, возможности и имя сервера.
func TestMCPInitialize(t *testing.T) {
	h := handler(t)

	resp, answer := call(t, h, "secret", "initialize", 1, map[string]any{
		"protocolVersion": "2025-03-26",
		"clientInfo":      map[string]any{"name": "test"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код %d, ожидался 200", resp.StatusCode)
	}

	result, ok := answer["result"].(map[string]any)
	if !ok {
		t.Fatalf("нет result: %+v", answer)
	}
	if result["protocolVersion"] != "2025-03-26" {
		t.Errorf("protocolVersion = %v, ожидалась запрошенная версия", result["protocolVersion"])
	}
	server, ok := result["serverInfo"].(map[string]any)
	if !ok || server["name"] != "test" || server["version"] != "v1" {
		t.Errorf("serverInfo = %+v", result["serverInfo"])
	}
}

// TestMCPToolsListAndCall — список инструментов и вызов возвращают то, что
// объявлено, а ошибка инструмента приходит как isError.
func TestMCPToolsListAndCall(t *testing.T) {
	h := handler(t)

	_, answer := call(t, h, "secret", "tools/list", 2, nil)
	result := answer["result"].(map[string]any)
	tools := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("инструментов %d, ожидался 1", len(tools))
	}
	if tools[0].(map[string]any)["name"] != "echo" {
		t.Errorf("имя инструмента = %v", tools[0])
	}

	_, answer = call(t, h, "secret", "tools/call", 3, map[string]any{
		"name":      "echo",
		"arguments": map[string]any{"x": 1},
	})
	result = answer["result"].(map[string]any)
	if result["isError"] != false {
		t.Errorf("isError = %v, ожидался false", result["isError"])
	}
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, `"x":1`) {
		t.Errorf("текст результата = %q", text)
	}

	_, answer = call(t, h, "secret", "tools/call", 4, map[string]any{
		"name":      "echo",
		"arguments": map[string]any{"fail": true},
	})
	result = answer["result"].(map[string]any)
	if result["isError"] != true {
		t.Errorf("ошибка инструмента не помечена: %+v", result)
	}
}

// TestMCPRejectsUnknownTool — имя вне tools/list не выполняется.
func TestMCPRejectsUnknownTool(t *testing.T) {
	h := handler(t)

	resp, answer := call(t, h, "secret", "tools/call", 5, map[string]any{"name": "rm"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код %d, ожидался 200", resp.StatusCode)
	}

	errAnswer, ok := answer["error"].(map[string]any)
	if !ok || errAnswer["code"].(float64) != -32602 {
		t.Errorf("ответ = %+v, ожидалась ошибка параметров", answer)
	}
}

// TestMCPNotificationDoesNotGetBody — уведомление подтверждается кодом 202 и не
// получает JSON-ответа.
func TestMCPNotificationDoesNotGetBody(t *testing.T) {
	h := handler(t)

	resp, answer := call(t, h, "secret", "notifications/initialized", nil, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("код %d, ожидался 202", resp.StatusCode)
	}
	if len(answer) != 0 {
		t.Errorf("уведомление получило тело: %+v", answer)
	}
}

// TestMCPRejectsGet — поток по GET не поддерживается.
func TestMCPRejectsGet(t *testing.T) {
	h := handler(t)

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("код %d, ожидался 405", rec.Code)
	}
}
