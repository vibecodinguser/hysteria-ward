package api

import (
	"net/http"

	"github.com/vibecodinguser/hysteria-ward/internal/config"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// serverAnswer — настройки сервера, которые видит администратор и которые нужны
// клиентам.
type serverAnswer struct {
	// Domain — домен, по которому клиенты видят сервер. Он же идёт в ключи.
	Domain string `json:"domain"`

	// Masquerade — чем сервер отвечает постороннему запросу.
	Masquerade config.Masquerade `json:"masquerade"`

	// Bandwidth — глобальные пределы скорости в байтах в секунду. Ноль — без
	// ограничения; интерфейс показывает их в Мбит/с.
	Bandwidth config.Bandwidth `json:"bandwidth"`
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

	domain, err := store.NormalizeDomain(request.Domain)
	if err != nil {
		if isBadDomain(err) {
			s.answerError(w, http.StatusBadRequest, err.Error())

			return
		}

		s.answerStoreError(w, "проверка домена", err)

		return
	}

	settings, err := s.store.PanelSettings()
	if err != nil {
		s.answerStoreError(w, "чтение настроек панели", err)

		return
	}

	// Новый домен может сделать недопустимой пару «сервер — панель»: например,
	// публичная панель оказалась бы на домене сервера или явный домен панели
	// выпал бы из нового базового домена. Проверяем до записи.
	if domain != "" {
		if err := settings.ValidateFor(domain); err != nil {
			s.answerError(w, http.StatusBadRequest, err.Error())

			return
		}
	}

	if err := s.store.SaveDomain(domain); err != nil {
		if isBadDomain(err) {
			s.answerError(w, http.StatusBadRequest, err.Error())

			return
		}

		s.answerStoreError(w, "запись домена", err)

		return
	}

	// Новый домен требует нового сертификата: прежний выписан на другие имена.
	// Имена обновляются до пробуждения выпуска, а сам выпуск идёт в фоне.
	if s.certificates != nil && domain != "" {
		s.certificates.SetNames(settings.Hosts(domain))
		s.certificates.Kick()
	}

	s.logger.Info("домен сервера записан", "domain", domain)

	answer, err := s.serverSettings()
	if err != nil {
		s.answerStoreError(w, "чтение настроек сервера", err)

		return
	}

	s.answerJSON(w, http.StatusOK, answer)
}

// handleApplyServer запоминает masquerade и пределы скорости, затем применяет
// конфигурацию: ядро перезапускается, активные соединения клиентов обрываются.
//
// Сначала запись, потом применение: если ядро не поднимется, правка останется в
// базе черновиком, и администратор сможет её починить. Обратный порядок оставил
// бы работающее ядро с настройкой, которой в базе нет.
func (s *Server) handleApplyServer(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Masquerade *config.Masquerade `json:"masquerade"`
		Bandwidth  *config.Bandwidth  `json:"bandwidth"`
	}

	if err := decodeBody(w, r, &request); err != nil {
		s.answerError(w, http.StatusBadRequest, "тело запроса: "+err.Error())

		return
	}

	if request.Masquerade == nil && request.Bandwidth == nil {
		s.answerError(w, http.StatusBadRequest, "не заданы настройки для применения")

		return
	}

	if request.Masquerade != nil {
		if err := request.Masquerade.Validate(); err != nil {
			s.answerError(w, http.StatusBadRequest, err.Error())

			return
		}
	}

	if request.Bandwidth != nil {
		if err := request.Bandwidth.Validate(); err != nil {
			s.answerError(w, http.StatusBadRequest, err.Error())

			return
		}
	}

	cfg, err := s.store.Config()
	if err != nil {
		s.answerStoreError(w, "чтение конфигурации", err)

		return
	}

	if request.Masquerade != nil {
		cfg.Masquerade = *request.Masquerade
	}
	if request.Bandwidth != nil {
		cfg.Bandwidth = *request.Bandwidth
	}

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

	s.logger.Info("настройки сервера применены",
		"masquerade", string(cfg.Masquerade.Mode),
		"bandwidth_up", cfg.Bandwidth.Up,
		"bandwidth_down", cfg.Bandwidth.Down,
	)

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

	return serverAnswer{Domain: domain, Masquerade: cfg.Masquerade, Bandwidth: cfg.Bandwidth}, nil
}
