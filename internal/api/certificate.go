package api

import (
	"net/http"
	"time"
)

// certificateAnswer — состояние сертификата, каким его видит администратор.
//
// Ошибка выпуска показывается здесь, а не только в журнале: панель с временным
// сертификатом должна объяснять, почему браузер всё ещё ругается и что делать.
type certificateAnswer struct {
	// Ready — настоящий сертификат выпущен и предъявляется.
	Ready bool `json:"ready"`

	// Placeholder — сейчас предъявляется временный самоподписанный.
	Placeholder bool `json:"placeholder"`

	// Managed — панель сама выпускает и продлевает сертификат.
	Managed bool `json:"managed"`

	// Mode — способ проверки ACME: http или dns.
	Mode string `json:"mode"`

	// Names — имена, которые покрывает сертификат.
	Names []string `json:"names"`

	// NotAfter — когда сертификат перестанет действовать.
	NotAfter time.Time `json:"not_after"`

	// Directory — адрес ACME-каталога.
	Directory string `json:"directory"`

	// LastError — почему последняя попытка выпуска не удалась.
	LastError string `json:"last_error"`

	// LastAttempt — когда последний раз начинали выпуск.
	LastAttempt time.Time `json:"last_attempt"`

	// NextAttempt — когда выпуск повторится сам.
	NextAttempt time.Time `json:"next_attempt"`
}

// handleCertificate рассказывает, что с сертификатом.
func (s *Server) handleCertificate(w http.ResponseWriter, _ *http.Request) {
	answer, ok := s.certificateStatus(w)
	if !ok {
		return
	}

	s.answerJSON(w, http.StatusOK, answer)
}

// handleIssueCertificate просит выпустить сертификат сейчас, не дожидаясь
// следующей автоматической попытки. Это путь повторить после ошибки.
func (s *Server) handleIssueCertificate(w http.ResponseWriter, _ *http.Request) {
	if s.certificates == nil || s.background == nil {
		s.answerError(w, http.StatusConflict,
			"выпуск сертификата не настроен: задайте домен и e-mail при установке или запустите setup")

		return
	}

	// Ошибка настроек не лечится повтором: выпуск ещё ни разу не начинался, а
	// способ проверки и хук меняются только установщиком. Повторять имеет смысл
	// сетевые и ACME-ошибки, после которых попытка уже была.
	status := s.certificates.Status()
	if status.LastAttempt.IsZero() && status.LastError != "" {
		s.answerError(w, http.StatusConflict, status.LastError)

		return
	}

	s.certificates.Kick()
	s.logger.Info("запрошен выпуск сертификата")

	answer, ok := s.certificateStatus(w)
	if !ok {
		return
	}

	s.answerJSON(w, http.StatusAccepted, answer)
}

// certificateStatus собирает ответ о сертификате. Если сертификатом управляет не
// панель, это не ошибка: статический запуск тоже имеет право рассказать, что
// предъявляет.
func (s *Server) certificateStatus(w http.ResponseWriter) (certificateAnswer, bool) {
	if s.certificates == nil {
		s.answerError(w, http.StatusConflict, "панель не управляет сертификатом")

		return certificateAnswer{}, false
	}

	settings, err := s.store.ACMESettings()
	if err != nil {
		s.logger.Error("настройки ACME", "err", err)
		s.answerError(w, http.StatusInternalServerError, "не удалось прочитать настройки ACME")

		return certificateAnswer{}, false
	}

	status := s.certificates.Status()
	mode := status.Mode
	if mode == "" {
		mode = settings.Mode
	}

	return certificateAnswer{
		Ready:       status.Ready && !status.Placeholder,
		Placeholder: status.Placeholder,
		Managed:     s.background != nil,
		Mode:        mode,
		Names:       status.Names,
		NotAfter:    status.NotAfter,
		Directory:   settings.Directory,
		LastError:   status.LastError,
		LastAttempt: status.LastAttempt,
		NextAttempt: status.NextAttempt,
	}, true
}
