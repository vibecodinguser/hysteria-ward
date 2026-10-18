package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"

	"github.com/vibecodinguser/hysteria-ward/internal/authn"
	"github.com/vibecodinguser/hysteria-ward/internal/build"
)

const (
	// sessionCookie — имя cookie, в котором лежит сессия.
	sessionCookie = "hysteria_ward_session"

	// csrfHeader — заголовок, которым интерфейс предъявляет csrf-токен.
	csrfHeader = "X-CSRF-Token"

	// maxLoginBody — предел на тело запроса входа: там только имя и пароль.
	maxLoginBody = 4 << 10
)

// sessionKey — ключ, под которым защищённый обработчик находит сессию.
type sessionKey struct{}

// sessionFrom достаёт сессию, которую положил protect.
func sessionFrom(ctx context.Context) authn.Session {
	session, _ := ctx.Value(sessionKey{}).(authn.Session)

	return session
}

// sessionAnswer — что интерфейс узнаёт о своём входе.
//
// Токена сессии здесь нет: он лежит в cookie, и скрипту на странице он не нужен —
// значит, и украсть его скрипту неоткуда.
type sessionAnswer struct {
	Name string `json:"name"`
	CSRF string `json:"csrf"`
}

// errorAnswer — ответ с ошибкой.
type errorAnswer struct {
	Error string `json:"error"`
}

// Status — что панель рассказывает о себе вошедшему.
type Status struct {
	// Version — версия панели.
	Version string `json:"version"`

	// CoreRunning — работает ли ядро Hysteria 2 прямо сейчас. Упавшее ядро не
	// должно выглядеть как рабочее, даже если панель работает.
	CoreRunning bool `json:"core_running"`
}

// handleLogin проверяет имя и пароль и заводит сессию.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLoginBody)).Decode(&request); err != nil {
		s.answerError(w, http.StatusBadRequest, "запрос не разобран")
		return
	}

	session, err := s.auth.Login(r.Context(), clientAddr(r), request.Name, request.Password)
	if err != nil {
		if errors.Is(err, authn.ErrDenied) {
			s.answerError(w, http.StatusUnauthorized, "неверное имя или пароль")
			return
		}

		s.logger.Error("вход не удался", "err", err)
		s.answerError(w, http.StatusInternalServerError, "вход не удался")

		return
	}

	name, err := s.auth.AdminName()
	if err != nil {
		// Сессия уже заведена, но cookie ещё не отдана: администратор войдёт
		// заново, а не останется с половиной ответа.
		s.logger.Error("имя администратора", "err", err)
		s.answerError(w, http.StatusInternalServerError, "вход не удался")

		return
	}

	s.setSessionCookie(w, session)
	s.answerJSON(w, http.StatusOK, sessionAnswer{Name: name, CSRF: session.CSRF})
}

// handleLogout закрывает сессию.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.Logout(sessionFrom(r.Context()).Token); err != nil {
		s.logger.Error("выход не удался", "err", err)
		s.answerError(w, http.StatusInternalServerError, "выход не удался")

		return
	}

	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// handleSession отвечает, кто вошёл. Интерфейс спрашивает это при каждой загрузке
// страницы: cookie лежит в браузере, а знание о входе — в панели.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	name, err := s.auth.AdminName()
	if err != nil {
		s.logger.Error("имя администратора", "err", err)
		s.answerError(w, http.StatusInternalServerError, "не удалось прочитать имя администратора")

		return
	}

	s.answerJSON(w, http.StatusOK, sessionAnswer{Name: name, CSRF: sessionFrom(r.Context()).CSRF})
}

// handleStatus рассказывает вошедшему, что происходит с панелью.
func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	s.answerJSON(w, http.StatusOK, s.currentStatus())
}

// handleUnknownAPI отвечает на незнакомый путь внутри /api/.
//
// Ошибкой, а не страницей интерфейса: иначе опечатка в запросе выглядела бы как
// рабочий ответ.
func (s *Server) handleUnknownAPI(w http.ResponseWriter, _ *http.Request) {
	s.answerError(w, http.StatusNotFound, "неизвестный запрос")
}

// protect пускает дальше только вошедшего, а изменяющие запросы — ещё и с
// csrf-токеном.
//
// Чужой сайт cookie пришлёт, а подставить свой заголовок не сможет: браузер
// отправит только те заголовки, которые разрешил скрипт того же сайта.
func (s *Server) protect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, err := s.auth.Check(s.sessionToken(r))
		if err != nil {
			if !errors.Is(err, authn.ErrNoSession) {
				s.logger.Error("проверка сессии", "err", err)
			}

			s.answerError(w, http.StatusUnauthorized, "нужен вход")
			return
		}

		if changesState(r.Method) && !session.ValidCSRF(r.Header.Get(csrfHeader)) {
			s.answerError(w, http.StatusForbidden, "нет csrf-токена")
			return
		}

		next(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, session)))
	}
}

// changesState сообщает, меняет ли запрос состояние панели. Именно такие запросы и
// требуют csrf-токен.
func changesState(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

// currentStatus собирает ответ о состоянии панели.
func (s *Server) currentStatus() Status {
	status := Status{Version: build.Current().Version}
	if s.coreRunning != nil {
		status.CoreRunning = s.coreRunning()
	}

	return status
}

// setSessionCookie кладёт токен сессии в cookie.
//
// HttpOnly обязателен: скрипт на странице до входа дотянуться не должен. Secure
// тоже: панель отдаётся только по HTTPS, и cookie туда же. Срок жизни здесь не
// ставится — cookie живёт до закрытия браузера, а настоящий срок сессии считает
// панель, и браузеру об этом знать незачем.
func (s *Server) setSessionCookie(w http.ResponseWriter, session authn.Session) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    session.Token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

// clearSessionCookie убирает cookie при выходе.
func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// sessionToken достаёт токен сессии из cookie.
func (s *Server) sessionToken(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}

	return cookie.Value
}

// answerJSON отвечает JSON-ом.
func (s *Server) answerJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)

	if err := json.NewEncoder(w).Encode(value); err != nil {
		s.logger.Error("ответ не отправлен", "err", err)
	}
}

// answerError отвечает отказом в том же виде.
func (s *Server) answerError(w http.ResponseWriter, code int, reason string) {
	s.answerJSON(w, code, errorAnswer{Error: reason})
}

// clientAddr возвращает адрес того, кто пришёл: по нему считается задержка при
// переборе пароля.
//
// Адрес берётся у соединения, а не из заголовка: заголовок подставляет кто угодно,
// а соединение — нет. Обратного прокси перед панелью нет, и это осознанно.
func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}
