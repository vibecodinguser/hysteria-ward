package api

import (
	"net/http"
	"strings"

	"github.com/vibecodinguser/hysteria-ward/internal/certs"
	"github.com/vibecodinguser/hysteria-ward/internal/config"
)

// serverAnswer — настройки сервера, которые видит администратор и которые нужны
// клиентам.
type serverAnswer struct {
	// Domain — домен, по которому клиенты видят сервер. Он же идёт в ключи.
	Domain string `json:"domain"`

	// Masquerade — чем сервер отвечает постороннему запросу.
	Masquerade config.Masquerade `json:"masquerade"`
}

// handleServer отдаёт настройки сервера: домен и masquerade.
func (s *Server) handleServer(w http.ResponseWriter, _ *http.Request) {
	answer, err := s.serverSettings()
	if err != nil {
		s.answerStoreError(w, "чтение настроек сервера", err)

		return
	}

	s.answerJSON(w, http.StatusOK, answer)
}

// handleSetServer запоминает домен сервера. Порт и остальное ядра живут в
// конфигурации сервера: их правка применяется отдельно и перезапускает ядро.
func (s *Server) handleSetServer(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Domain string `json:"domain"`
	}

	if err := decodeBody(w, r, &request); err != nil {
		s.answerError(w, http.StatusBadRequest, "тело запроса: "+err.Error())

		return
	}

	if err := s.store.SaveDomain(request.Domain); err != nil {
		if isBadDomain(err) {
			s.answerError(w, http.StatusBadRequest, err.Error())

			return
		}

		s.answerStoreError(w, "запись домена", err)

		return
	}

	// Новый домен требует нового сертификата: прежний выписан на другие имена.
	// Имена обновляются до пробуждения выпуска, а сам выпуск идёт в фоне.
	if s.certificates != nil && request.Domain != "" {
		s.certificates.SetNames(certs.Hosts(request.Domain))
		s.certificates.Kick()
	}

	s.logger.Info("домен сервера записан", "domain", strings.TrimSpace(request.Domain))

	answer, err := s.serverSettings()
	if err != nil {
		s.answerStoreError(w, "чтение настроек сервера", err)

		return
	}

	s.answerJSON(w, http.StatusOK, answer)
}

// handleApplyServer запоминает masquerade и применяет конфигурацию: ядро
// перезапускается, активные соединения клиентов обрываются.
//
// Сначала запись, потом применение: если ядро не поднимется, правка останется в
// базе черновиком, и администратор сможет её починить. Обратный порядок оставил
// бы работающее ядро с настройкой, которой в базе нет.
func (s *Server) handleApplyServer(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Masquerade *config.Masquerade `json:"masquerade"`
	}

	if err := decodeBody(w, r, &request); err != nil {
		s.answerError(w, http.StatusBadRequest, "тело запроса: "+err.Error())

		return
	}

	if request.Masquerade == nil {
		s.answerError(w, http.StatusBadRequest, "не задан режим masquerade")

		return
	}

	if err := request.Masquerade.Validate(); err != nil {
		s.answerError(w, http.StatusBadRequest, err.Error())

		return
	}

	cfg, err := s.store.Config()
	if err != nil {
		s.answerStoreError(w, "чтение конфигурации", err)

		return
	}

	cfg.Masquerade = *request.Masquerade
	if err := s.store.SaveConfig(cfg); err != nil {
		s.answerStoreError(w, "запись конфигурации", err)

		return
	}

	if err := s.apply(); err != nil {
		// Причина называется как есть: «каталог сайта не найден» полезнее для
		// администратора, чем «не удалось применить».
		s.logger.Error("конфигурация не применена", "err", err)
		s.answerError(w, http.StatusUnprocessableEntity, err.Error())

		return
	}

	s.logger.Info("masquerade применён", "mode", string(request.Masquerade.Mode))

	answer, err := s.serverSettings()
	if err != nil {
		s.answerStoreError(w, "чтение настроек сервера", err)

		return
	}

	s.answerJSON(w, http.StatusOK, answer)
}

// serverSettings собирает текущие настройки сервера.
func (s *Server) serverSettings() (serverAnswer, error) {
	domain, err := s.store.Domain()
	if err != nil {
		return serverAnswer{}, err
	}

	cfg, err := s.store.Config()
	if err != nil {
		return serverAnswer{}, err
	}

	return serverAnswer{Domain: domain, Masquerade: cfg.Masquerade}, nil
}
