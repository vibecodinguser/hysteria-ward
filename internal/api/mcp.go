package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/build"
	"github.com/vibecodinguser/hysteria-ward/internal/config"
	"github.com/vibecodinguser/hysteria-ward/internal/mcp"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// mcpKeyAnswer — ключ, каким его видит администратор. Токен сюда не входит: он
// показывается один раз при выпуске.
type mcpKeyAnswer struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// mcpKeysAnswer — список ключей.
type mcpKeysAnswer struct {
	Keys []mcpKeyAnswer `json:"keys"`
}

// newMCPKeyAnswer — ответ на выпуск: здесь токен есть ровно один раз.
type newMCPKeyAnswer struct {
	mcpKeyAnswer
	Token string `json:"token"`
}

// mcpServerStatus — состояние сервера для агента. Секретов здесь нет: от
// masquerade отдаётся только режим, а не путь к сайту или адрес зеркала.
type mcpServerStatus struct {
	Version     string                `json:"version"`
	Commit      string                `json:"commit"`
	Platform    string                `json:"platform"`
	CoreRunning bool                  `json:"core_running"`
	Domain      string                `json:"domain"`
	Masquerade  config.MasqueradeMode `json:"masquerade"`
	Bandwidth   config.Bandwidth      `json:"bandwidth"`

	// Certificate — короткое состояние сертификата, если панель им управляет.
	Certificate *mcpCertificate `json:"certificate,omitempty"`
}

// mcpCertificate — то, что агенту нужно знать о сертификате: выпущен ли он,
// на какие имена и до когда. Текст последней ошибки остаётся в панели.
type mcpCertificate struct {
	Ready       bool      `json:"ready"`
	Placeholder bool      `json:"placeholder"`
	Names       []string  `json:"names"`
	NotAfter    time.Time `json:"not_after"`
}

// handleMCPKeys перечисляет выпущенные MCP-ключи.
func (s *Server) handleMCPKeys(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	keys, err := s.store.APIKeys()
	if err != nil {
		s.logger.Error("список MCP-ключей", "err", err)
		s.answerError(w, http.StatusInternalServerError, "не удалось прочитать MCP-ключи")

		return
	}

	answer := mcpKeysAnswer{Keys: make([]mcpKeyAnswer, 0, len(keys))}
	for _, key := range keys {
		answer.Keys = append(answer.Keys, mcpKeyAnswer{
			ID:        key.ID,
			Name:      key.Name,
			CreatedAt: key.CreatedAt,
		})
	}

	s.answerJSON(w, http.StatusOK, answer)
}

// handleCreateMCPKey выпускает ключ и показывает токен один раз.
func (s *Server) handleCreateMCPKey(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name string `json:"name"`
	}
	if err := decodeBody(w, r, &request); err != nil {
		s.answerError(w, http.StatusBadRequest, "тело запроса: "+err.Error())

		return
	}

	key, token, err := s.store.CreateAPIKey(request.Name)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrAPIKeyExists):
			s.answerError(w, http.StatusConflict, err.Error())
		case errors.Is(err, store.ErrAPIKeyName):
			s.answerError(w, http.StatusBadRequest, err.Error())
		default:
			s.logger.Error("выпуск MCP-ключа", "err", err)
			s.answerError(w, http.StatusInternalServerError, "не удалось выпустить MCP-ключ")
		}

		return
	}

	s.logger.Info("MCP-ключ выпущен", "name", key.Name)

	w.Header().Set("Cache-Control", "no-store")
	s.answerJSON(w, http.StatusCreated, newMCPKeyAnswer{
		mcpKeyAnswer: mcpKeyAnswer{ID: key.ID, Name: key.Name, CreatedAt: key.CreatedAt},
		Token:        token,
	})
}

// handleDeleteMCPKey отзывает ключ. Токен после этого не проходит проверку.
func (s *Server) handleDeleteMCPKey(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAPIKey(r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrAPIKeyMissing) {
			s.answerError(w, http.StatusNotFound, err.Error())

			return
		}

		s.logger.Error("отзыв MCP-ключа", "err", err)
		s.answerError(w, http.StatusInternalServerError, "не удалось отозвать MCP-ключ")

		return
	}

	s.logger.Info("MCP-ключ отозван", "id", r.PathValue("id"))

	w.WriteHeader(http.StatusNoContent)
}

// mcpTools объявляет инструменты только для чтения.
func (s *Server) mcpTools() []mcp.Tool {
	empty := func() map[string]any {
		return map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		}
	}

	return []mcp.Tool{
		{
			Name:        "server_status",
			Description: "Версия панели, состояние ядра, домен, masquerade, пределы скорости и сертификат.",
			InputSchema: empty(),
		},
		{
			Name:        "users_list",
			Description: "Пользователи с квотой, расходом и состоянием. Пароли и клиентские ключи не возвращаются.",
			InputSchema: empty(),
		},
		{
			Name:        "traffic_snapshot",
			Description: "Текущий трафик по пользователям и суммарно: байты и скорость.",
			InputSchema: empty(),
		},
	}
}

// callMCPTool выполняет read-only инструмент. Аргументов у инструментов пока нет:
// если клиент их пришлёт, они просто не используются.
func (s *Server) callMCPTool(_ context.Context, name string, _ json.RawMessage) (any, error) {
	switch name {
	case "server_status":
		return s.mcpServerStatus()
	case "users_list":
		return s.mcpUsers()
	case "traffic_snapshot":
		return s.snapshotAnswer(s.metrics.Snapshot())
	default:
		return nil, fmt.Errorf("неизвестный инструмент: %s", name)
	}
}

// mcpServerStatus собирает состояние сервера без секретов.
func (s *Server) mcpServerStatus() (mcpServerStatus, error) {
	settings, err := s.serverSettings()
	if err != nil {
		return mcpServerStatus{}, err
	}

	info := build.Current()
	status := mcpServerStatus{
		Version:     info.Version,
		Commit:      info.Commit,
		Platform:    info.Platform,
		CoreRunning: s.currentStatus().CoreRunning,
		Domain:      settings.Domain,
		Masquerade:  settings.Masquerade.Mode,
		Bandwidth:   settings.Bandwidth,
	}

	if s.certificates != nil {
		certificate := s.certificates.Status()
		status.Certificate = &mcpCertificate{
			Ready:       certificate.Ready,
			Placeholder: certificate.Placeholder,
			Names:       certificate.Names,
			NotAfter:    certificate.NotAfter,
		}
	}

	return status, nil
}

// mcpUsers перечисляет пользователей в том же виде, что и интерфейс, но без
// паролей: userAnswer их и так не содержит.
func (s *Server) mcpUsers() (usersAnswer, error) {
	list, err := s.store.Users()
	if err != nil {
		return usersAnswer{}, err
	}

	answer := usersAnswer{Users: make([]userAnswer, 0, len(list))}
	for _, user := range list {
		answer.Users = append(answer.Users, s.userAnswer(user))
	}

	return answer, nil
}
