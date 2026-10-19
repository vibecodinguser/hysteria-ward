package api

import (
	"net/http"

	"github.com/vibecodinguser/hysteria-ward/internal/build"
)

// Status — что панель рассказывает о себе вошедшему.
type Status struct {
	// Version — версия панели.
	Version string `json:"version"`

	// CoreRunning — работает ли ядро Hysteria 2 прямо сейчас. Упавшее ядро не
	// должно выглядеть как рабочее, даже если панель работает.
	CoreRunning bool `json:"core_running"`
}

// handleStatus рассказывает вошедшему, что происходит с панелью.
func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	s.answerJSON(w, http.StatusOK, s.currentStatus())
}

// currentStatus собирает ответ о состоянии панели.
func (s *Server) currentStatus() Status {
	status := Status{Version: build.Current().Version}
	if s.coreRunning != nil {
		status.CoreRunning = s.coreRunning()
	}

	return status
}
