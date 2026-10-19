package api

import (
	"net/http"

	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// panelAnswer — что интерфейс знает о доступе к панели.
type panelAnswer struct {
	// Access — режим доступа: туннель или публичный.
	Access store.Access `json:"access"`

	// Addr — адрес, который панель слушает сейчас. По нему администратор
	// пробрасывает порт через SSH, когда панель спрятана на петле.
	Addr string `json:"addr"`
}

// handlePanel рассказывает вошедшему, кому панель показывает себя.
func (s *Server) handlePanel(w http.ResponseWriter, _ *http.Request) {
	s.answerJSON(w, http.StatusOK, panelAnswer{Access: s.Access(), Addr: s.address()})
}

// handleSetAccess включает и выключает публичный доступ.
//
// Порядок важен: сначала слушатель, потом база. Запомнить режим раньше значило бы
// пообещать после перезапуска то, чего не вышло сейчас.
func (s *Server) handleSetAccess(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Access store.Access `json:"access"`
	}

	if err := decodeBody(w, r, &request); err != nil {
		s.answerError(w, http.StatusBadRequest, "запрос не разобран")
		return
	}

	if err := request.Access.Validate(); err != nil {
		s.answerError(w, http.StatusBadRequest, "неизвестный режим доступа")
		return
	}

	// Смены идут по одной: две команды сразу перевешивали бы слушатель друг у
	// друга из-под рук, а база запомнила бы последнюю из них.
	s.switchMu.Lock()
	defer s.switchMu.Unlock()

	settings, err := s.store.PanelSettings()
	if err != nil {
		s.logger.Error("настройки панели", "err", err)
		s.answerError(w, http.StatusInternalServerError, "не удалось прочитать настройки панели")

		return
	}

	if request.Access == store.AccessPublic && !s.publicAllowed(w) {
		return
	}

	previous := settings.Access

	if err := s.SetAccess(request.Access); err != nil {
		s.logger.Error("режим доступа не сменился", "err", err)
		s.answerError(w, http.StatusInternalServerError, "не удалось сменить режим доступа")

		return
	}

	settings.Access = request.Access
	if err := s.store.SavePanelSettings(settings); err != nil {
		// Режим не сохранился — панель возвращается туда, где стояла: иначе она
		// осталась бы открытой против того, что записано в базе.
		if rollbackErr := s.SetAccess(previous); rollbackErr != nil {
			s.logger.Error("панель не вернулась в прежний режим", "err", rollbackErr)
		}

		s.logger.Error("режим доступа не сохранён", "err", err)
		s.answerError(w, http.StatusInternalServerError, "не удалось сохранить настройки панели")

		return
	}

	s.answerJSON(w, http.StatusOK, panelAnswer{Access: s.Access(), Addr: s.address()})
}

// publicAllowed проверяет, можно ли открыть панель наружу, и сам отвечает
// отказом, если нельзя.
//
// Пока действует пароль по умолчанию, открывать панель некуда: его знает кто
// угодно, он написан в документации. Смена пароля — единственный способ это
// снять, поэтому отказ говорит именно о ней.
func (s *Server) publicAllowed(w http.ResponseWriter) bool {
	inUse, err := s.auth.DefaultPasswordInUse()
	if err != nil {
		s.logger.Error("проверка пароля по умолчанию", "err", err)
		s.answerError(w, http.StatusInternalServerError, "не удалось проверить пароль администратора")

		return false
	}

	if inUse {
		s.answerError(w, http.StatusConflict,
			"сначала смените пароль по умолчанию: с ним панель откроется всякому, кто её найдёт")

		return false
	}

	return true
}
