// Пакет mcp отдаёт панель ИИ-агентам по протоколу MCP.
//
// Это отдельная поверхность, а не второй вход в /api/: ключ MCP не открывает
// cookie-сессию и не даёт административных прав. Пока поддерживаются только
// инструменты чтения, поэтому ключ не может изменить сервер.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

const (
	// ProtocolVersion — версия MCP, которую панель предлагает по умолчанию.
	ProtocolVersion = "2025-06-18"

	// maxBody — предел запроса. MCP-запросы короткие; большой JSON — это либо
	// ошибка клиента, либо попытка занять память.
	maxBody = 64 << 10
)

// supportedVersions — версии, которые панель понимает. Клиент и сервер
// договариваются о версии при initialize.
var supportedVersions = []string{"2024-11-05", "2025-03-26", ProtocolVersion}

// Tool — описание инструмента для tools/list.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ToolHandler выполняет инструмент и возвращает значение, которое уйдёт агенту
// как JSON-текст.
type ToolHandler func(ctx context.Context, name string, arguments json.RawMessage) (any, error)

// Options — зависимости MCP-сервера.
type Options struct {
	// Authenticate проверяет Bearer-токен. Ошибка означает проблему хранилища,
	// а не «ключ не подошёл»: отказ без ошибки — это false.
	Authenticate func(token string) (bool, error)

	// Tools и Call — что умеет сервер. Без Call tools/list может быть пустым.
	Tools []Tool
	Call  ToolHandler

	// ServerName и ServerVersion попадают в initialize.
	ServerName    string
	ServerVersion string

	// Logger — куда писать внутренние ошибки. Токен сюда не попадает.
	Logger *slog.Logger
}

// Handler — HTTP-обработчик MCP по streamable HTTP без SSE.
type Handler struct {
	authenticate func(string) (bool, error)
	tools        []Tool
	call         ToolHandler
	serverName   string
	version      string
	logger       *slog.Logger
}

// New собирает обработчик.
func New(opts Options) (*Handler, error) {
	if opts.Authenticate == nil {
		return nil, errors.New("не задана проверка API-ключа")
	}
	if opts.Call == nil && len(opts.Tools) != 0 {
		return nil, errors.New("инструменты объявлены, а обработчика нет")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.ServerName == "" {
		opts.ServerName = "hysteria-ward"
	}
	if opts.ServerVersion == "" {
		opts.ServerVersion = "dev"
	}

	return &Handler{
		authenticate: opts.Authenticate,
		tools:        opts.Tools,
		call:         opts.Call,
		serverName:   opts.ServerName,
		version:      opts.ServerVersion,
		logger:       opts.Logger,
	}, nil
}

// ServeHTTP принимает POST с JSON-RPC. GET не поддерживается: сервер не шлёт
// агенту сообщения по своей инициативе, а SSE здесь был бы лишним каналом.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		h.write(w, http.StatusMethodNotAllowed, nil, nil, &rpcError{
			Code:    codeInvalidRequest,
			Message: "MCP принимает только POST",
		})

		return
	}

	token, ok := bearerToken(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="hysteria-ward"`)
		h.write(w, http.StatusUnauthorized, nil, nil, &rpcError{
			Code:    codeUnauthorized,
			Message: "нужен API-ключ",
		})

		return
	}

	allowed, err := h.authenticate(token)
	if err != nil {
		h.logger.Error("проверка API-ключа MCP", "err", err)
		h.write(w, http.StatusInternalServerError, nil, nil, &rpcError{
			Code:    codeInternal,
			Message: "не удалось проверить API-ключ",
		})

		return
	}
	if !allowed {
		w.Header().Set("WWW-Authenticate", `Bearer realm="hysteria-ward"`)
		h.write(w, http.StatusUnauthorized, nil, nil, &rpcError{
			Code:    codeUnauthorized,
			Message: "неверный API-ключ",
		})

		return
	}

	var request rpcRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := dec.Decode(&request); err != nil {
		h.write(w, http.StatusBadRequest, nil, nil, &rpcError{
			Code:    codeParse,
			Message: "тело MCP-запроса не разобрано: " + err.Error(),
		})

		return
	}

	if request.JSONRPC != "2.0" || request.Method == "" {
		h.write(w, http.StatusOK, request.ID, nil, &rpcError{
			Code:    codeInvalidRequest,
			Message: "ожидался JSON-RPC 2.0 с непустым method",
		})

		return
	}

	// Уведомления не имеют id и не получают ответа. Сейчас клиент присылает
	// только notifications/initialized, но неизвестное уведомление тоже не
	// должно превращаться в ошибку.
	if request.ID == nil {
		w.WriteHeader(http.StatusAccepted)

		return
	}

	switch request.Method {
	case "initialize":
		h.write(w, http.StatusOK, request.ID, h.initialize(request.Params), nil)

	case "ping":
		h.write(w, http.StatusOK, request.ID, map[string]any{}, nil)

	case "tools/list":
		h.write(w, http.StatusOK, request.ID, map[string]any{"tools": h.tools}, nil)

	case "tools/call":
		h.callTool(w, r, request)

	default:
		h.write(w, http.StatusOK, request.ID, nil, &rpcError{
			Code:    codeMethodNotFound,
			Message: "неизвестный метод: " + request.Method,
		})
	}
}

// initialize отвечает на рукопожатие. Если клиент знает версию, которую знает
// панель, возвращается она; иначе — текущая версия панели.
func (h *Handler) initialize(params json.RawMessage) map[string]any {
	var request struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &request)

	version := ProtocolVersion
	for _, supported := range supportedVersions {
		if request.ProtocolVersion == supported {
			version = supported
			break
		}
	}

	return map[string]any{
		"protocolVersion": version,
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    h.serverName,
			"version": h.version,
		},
	}
}

// callTool выполняет инструмент. Ошибка инструмента — это не ошибка протокола:
// агент должен увидеть её в result с isError, чтобы сам решил, что делать.
func (h *Handler) callTool(w http.ResponseWriter, r *http.Request, request rpcRequest) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		h.write(w, http.StatusOK, request.ID, nil, &rpcError{
			Code:    codeInvalidParams,
			Message: "параметры tools/call не разобраны: " + err.Error(),
		})

		return
	}
	if params.Name == "" {
		h.write(w, http.StatusOK, request.ID, nil, &rpcError{
			Code:    codeInvalidParams,
			Message: "не задано имя инструмента",
		})

		return
	}

	if !h.hasTool(params.Name) {
		h.write(w, http.StatusOK, request.ID, nil, &rpcError{
			Code:    codeInvalidParams,
			Message: "неизвестный инструмент: " + params.Name,
		})

		return
	}

	arguments := params.Arguments
	if len(arguments) == 0 || string(arguments) == "null" {
		arguments = json.RawMessage("{}")
	}

	value, err := h.call(r.Context(), params.Name, arguments)
	if err != nil {
		h.write(w, http.StatusOK, request.ID, toolResult(h.errorText(err), true), nil)

		return
	}

	text, err := json.Marshal(value)
	if err != nil {
		h.logger.Error("результат MCP не закодирован", "tool", params.Name, "err", err)
		h.write(w, http.StatusOK, request.ID, toolResult("результат инструмента не закодирован", true), nil)

		return
	}

	h.write(w, http.StatusOK, request.ID, toolResult(string(text), false), nil)
}

// hasTool не даёт вызвать то, чего нет в tools/list.
func (h *Handler) hasTool(name string) bool {
	for _, tool := range h.tools {
		if tool.Name == name {
			return true
		}
	}

	return false
}

// errorText не отдаёт агенту пустую строку вместо причины.
func (h *Handler) errorText(err error) string {
	if err == nil {
		return "инструмент завершился ошибкой"
	}

	return err.Error()
}

// toolResult собирает ответ tools/call в формате MCP.
func toolResult(text string, failed bool) map[string]any {
	return map[string]any{
		"content": []map[string]string{
			{"type": "text", "text": text},
		},
		"isError": failed,
	}
}

// bearerToken вытаскивает токен из Authorization. Другие способы намеренно не
// поддерживаются: один заголовок проще не перепутать с cookie.
func bearerToken(r *http.Request) (string, bool) {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}

	return parts[1], true
}

// rpcRequest — входящий JSON-RPC. ID указатель, чтобы отличить уведомление от
// запроса; params остаются сырыми до разбора конкретным методом.
type rpcRequest struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params"`
}

// rpcResponse — ответ JSON-RPC. Result и Error взаимоисключающие.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError — ошибка протокола.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
	codeUnauthorized   = -32001
)

// write отправляет JSON-ответ. id == nil означает уведомление или ошибку до
// разбора запроса; в JSON тогда уходит null.
func (h *Handler) write(w http.ResponseWriter, status int, id *json.RawMessage, result any, rpcErr *rpcError) {
	answer := rpcResponse{
		JSONRPC: "2.0",
		ID:      json.RawMessage("null"),
		Result:  result,
		Error:   rpcErr,
	}
	if id != nil {
		answer.ID = *id
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(answer); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		h.logger.Error("ответ MCP не отправлен", "err", err)
	}
}
