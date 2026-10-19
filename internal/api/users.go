package api

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"strings"
	"time"
	"unicode"

	"rsc.io/qr"

	"github.com/vibecodinguser/hysteria-ward/internal/keys"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
	"github.com/vibecodinguser/hysteria-ward/internal/users"
)

// userAnswer — пользователь, каким его видит интерфейс.
//
// Пароля здесь нет: он уходит только вместе с ключом, а ключ спрашивают отдельно
// и по одному пользователю. Иначе один запрос списка отдавал бы все пароли сразу.
type userAnswer struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Enabled   bool      `json:"enabled"`
	Online    bool      `json:"online"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// usersAnswer — список пользователей.
type usersAnswer struct {
	Users []userAnswer `json:"users"`
}

// keyAnswer — ссылка, по которой клиент подключается.
type keyAnswer struct {
	URI string `json:"uri"`
}

// handleUsers перечисляет пользователей.
func (s *Server) handleUsers(w http.ResponseWriter, _ *http.Request) {
	list, err := s.store.Users()
	if err != nil {
		s.logger.Error("список пользователей", "err", err)
		s.answerError(w, http.StatusInternalServerError, "не удалось прочитать пользователей")

		return
	}

	answer := usersAnswer{Users: make([]userAnswer, 0, len(list))}
	for _, user := range list {
		answer.Users = append(answer.Users, s.userAnswer(user))
	}

	s.answerJSON(w, http.StatusOK, answer)
}

// handleCreateUser заводит пользователя с придуманным паролем.
//
// Пароль не принимается от администратора: панель сама знает, каким он должен
// быть, а придуманный человеком рано или поздно окажется коротким.
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name string `json:"name"`
	}

	if err := decodeBody(w, r, &request); err != nil {
		s.answerError(w, http.StatusBadRequest, "тело запроса: "+err.Error())

		return
	}

	name, err := checkUserName(request.Name)
	if err != nil {
		s.answerError(w, http.StatusBadRequest, err.Error())

		return
	}

	password, err := s.freePassword()
	if err != nil {
		s.logger.Error("пароль пользователя", "err", err)
		s.answerError(w, http.StatusInternalServerError, "не удалось придумать пароль")

		return
	}

	user, err := s.store.CreateUser(store.User{Name: name, Password: password, Enabled: true})
	if err != nil {
		s.answerStoreError(w, "создание пользователя", err)

		return
	}

	if !s.reloaded(w, "создание пользователя") {
		return
	}

	s.logger.Info("пользователь заведён", "name", user.Name)

	s.answerJSON(w, http.StatusCreated, s.userAnswer(user))
}

// handleUpdateUser переименовывает пользователя и включает или выключает его.
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	var request struct {
		// nil означает «не менять»: иначе выключенный пользователь включался бы
		// каждой правкой имени.
		Name    *string `json:"name"`
		Enabled *bool   `json:"enabled"`
	}

	if err := decodeBody(w, r, &request); err != nil {
		s.answerError(w, http.StatusBadRequest, "тело запроса: "+err.Error())

		return
	}

	user, found, err := s.store.User(r.PathValue("id"))
	if err != nil {
		s.answerStoreError(w, "чтение пользователя", err)

		return
	}
	if !found {
		s.answerError(w, http.StatusNotFound, "пользователя нет")

		return
	}

	if request.Name != nil {
		name, err := checkUserName(*request.Name)
		if err != nil {
			s.answerError(w, http.StatusBadRequest, err.Error())

			return
		}

		user.Name = name
	}
	if request.Enabled != nil {
		user.Enabled = *request.Enabled
	}

	updated, err := s.store.UpdateUser(user)
	if err != nil {
		s.answerStoreError(w, "правка пользователя", err)

		return
	}

	if !s.reloaded(w, "правка пользователя") {
		return
	}

	s.logger.Info("пользователь изменён", "name", updated.Name, "enabled", updated.Enabled)

	s.answerJSON(w, http.StatusOK, s.userAnswer(updated))
}

// handleDeleteUser удаляет пользователя. Его активные соединения рвёт ядро: учёт
// трафика видит, что пользователя больше нет, и закрывает соединение.
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	user, found, err := s.store.User(id)
	if err != nil {
		s.answerStoreError(w, "чтение пользователя", err)

		return
	}
	if !found {
		s.answerError(w, http.StatusNotFound, "пользователя нет")

		return
	}

	if err := s.store.DeleteUser(id); err != nil {
		s.answerStoreError(w, "удаление пользователя", err)

		return
	}

	if !s.reloaded(w, "удаление пользователя") {
		return
	}

	s.logger.Info("пользователь удалён", "name", user.Name)

	w.WriteHeader(http.StatusNoContent)
}

// handleNewPassword меняет пароль пользователя: старый ключ перестаёт работать, а
// выпущенный следом — работает.
func (s *Server) handleNewPassword(w http.ResponseWriter, r *http.Request) {
	user, found, err := s.store.User(r.PathValue("id"))
	if err != nil {
		s.answerStoreError(w, "чтение пользователя", err)

		return
	}
	if !found {
		s.answerError(w, http.StatusNotFound, "пользователя нет")

		return
	}

	password, err := s.freePassword()
	if err != nil {
		s.logger.Error("пароль пользователя", "err", err)
		s.answerError(w, http.StatusInternalServerError, "не удалось придумать пароль")

		return
	}

	user.Password = password

	updated, err := s.store.UpdateUser(user)
	if err != nil {
		s.answerStoreError(w, "смена пароля", err)

		return
	}

	if !s.reloaded(w, "смена пароля") {
		return
	}

	s.logger.Info("пароль пользователя сменён", "name", updated.Name)

	s.answerJSON(w, http.StatusOK, s.userAnswer(updated))
}

// handleUserKey отдаёт ссылку hysteria2:// для пользователя.
func (s *Server) handleUserKey(w http.ResponseWriter, r *http.Request) {
	user, found, err := s.store.User(r.PathValue("id"))
	if err != nil {
		s.answerStoreError(w, "чтение пользователя", err)

		return
	}
	if !found {
		s.answerError(w, http.StatusNotFound, "пользователя нет")

		return
	}

	uri, err := s.userKey(user)
	if err != nil {
		s.answerKeyError(w, err)

		return
	}

	s.answerJSON(w, http.StatusOK, keyAnswer{URI: uri})
}

// handleUserQR отдаёт ключ картинкой: клиенты на телефоне читают её камерой, и
// вручную вводить длинную ссылку им не приходится.
func (s *Server) handleUserQR(w http.ResponseWriter, r *http.Request) {
	user, found, err := s.store.User(r.PathValue("id"))
	if err != nil {
		s.answerStoreError(w, "чтение пользователя", err)

		return
	}
	if !found {
		s.answerError(w, http.StatusNotFound, "пользователя нет")

		return
	}

	uri, err := s.userKey(user)
	if err != nil {
		s.answerKeyError(w, err)

		return
	}

	picture, err := qrPNG(uri)
	if err != nil {
		s.logger.Error("QR-код", "name", user.Name, "err", err)
		s.answerError(w, http.StatusInternalServerError, "не удалось нарисовать QR-код")

		return
	}

	// Картинка не секретнее ключа, но и не общая: кэшировать её в браузере можно,
	// а промежуточным серверам — нет.
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=60")

	if _, err := w.Write(picture); err != nil {
		s.logger.Error("QR-код не отправлен", "err", err)
	}
}

// userAnswer превращает запись в то, что видит интерфейс.
func (s *Server) userAnswer(user store.User) userAnswer {
	return userAnswer{
		ID:        user.ID,
		Name:      user.Name,
		Enabled:   user.Enabled,
		Online:    s.users.IsOnline(user.ID),
		CreatedAt: user.CreatedAt,
		UpdatedAt: user.UpdatedAt,
	}
}

// userKey собирает ссылку для пользователя по текущим настройкам сервера.
//
// Настройки читаются при каждом запросе: домен и обфускацию могли поменять только
// что, и ключ, собранный по старой копии, отправил бы клиента не туда.
func (s *Server) userKey(user store.User) (string, error) {
	domain, err := s.store.Domain()
	if err != nil {
		return "", err
	}
	if domain == "" {
		return "", store.ErrDomainNotSet
	}

	cfg, err := s.store.Config()
	if err != nil {
		return "", err
	}

	return keys.URI(keys.Params{
		Domain:       domain,
		Listen:       cfg.Listen,
		ObfsPassword: cfg.Obfs.Password,
		Certificate:  s.currentCertificate(),
	}, user.Password)
}

// answerKeyError объясняет, почему ключ не собрался.
func (s *Server) answerKeyError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrDomainNotSet) {
		s.answerError(w, http.StatusConflict,
			"сначала укажите домен сервера в настройках: без него в ключе нет ни адреса, ни SNI")

		return
	}

	s.logger.Error("ключ не собрался", "err", err)
	s.answerError(w, http.StatusInternalServerError, "не удалось собрать ключ")
}

// reloaded перечитывает список пользователей в памяти после правки.
//
// Без этого ядро продолжало бы пускать удалённого и рвать оставшегося: база уже
// новая, а список, по которому оно работает, — старый.
func (s *Server) reloaded(w http.ResponseWriter, what string) bool {
	if err := s.users.Reload(); err != nil {
		s.logger.Error("список пользователей не перечитан", "after", what, "err", err)
		s.answerError(w, http.StatusInternalServerError, "правка записана, но не дошла до ядра: перезапустите панель")

		return false
	}

	return true
}

// freePassword придумывает пароль, которого нет ни у кого из пользователей.
func (s *Server) freePassword() (string, error) {
	for i := 0; i < 3; i++ {
		password, err := users.NewPassword()
		if err != nil {
			return "", err
		}

		if !s.users.PasswordInUse(password, "") {
			return password, nil
		}
	}

	return "", errors.New("три случайных пароля подряд оказались заняты")
}

// answerStoreError переводит ошибку хранилища в ответ. Ожидаемые ошибки видны
// администратору как есть, остальные — как поломка.
func (s *Server) answerStoreError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, store.ErrUserExists):
		s.answerError(w, http.StatusConflict, "пользователь с таким именем уже есть")
	case errors.Is(err, store.ErrUserMissing):
		s.answerError(w, http.StatusNotFound, "пользователя нет")
	default:
		s.logger.Error(what, "err", err)
		s.answerError(w, http.StatusInternalServerError, "не удалось сохранить изменения")
	}
}

// checkUserName проверяет имя, которое вводит администратор.
//
// Имя — не пароль и не адрес: оно нужно людям, чтобы различать ключи. Поэтому
// разрешено почти всё, кроме того, что не поместится в одну строку списка или
// сломает его вид.
func checkUserName(name string) (string, error) {
	name = strings.TrimSpace(name)

	switch {
	case name == "":
		return "", errors.New("имя не может быть пустым")
	case len(name) > 64:
		return "", errors.New("имя длиннее 64 символов")
	}

	for _, r := range name {
		if unicode.IsControl(r) {
			return "", errors.New("в имени есть управляющий символ")
		}
	}

	return name, nil
}

// isBadDomain отличает отказ в записи домена от поломки хранилища.
func isBadDomain(err error) bool {
	return errors.Is(err, store.ErrBadDomain)
}

// qrPNG рисует QR-код для ключа.
//
// Вокруг кода — белое поле в четыре модуля: без него сканеры не находят границы, и
// телефон, наведённый на экран, код не прочитает.
func qrPNG(text string) ([]byte, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil, fmt.Errorf("QR-код: %w", err)
	}

	const (
		quiet = 4
		scale = 8
	)

	side := (code.Size + 2*quiet) * scale
	picture := image.NewGray(image.Rect(0, 0, side, side))

	// Белый фон: image.NewGray заводит чёрную картинку, а код рисуется тёмным по
	// светлому.
	for i := range picture.Pix {
		picture.Pix[i] = 0xff
	}

	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; x++ {
			if !code.Black(x, y) {
				continue
			}

			for dy := 0; dy < scale; dy++ {
				row := (quiet+y)*scale + dy
				start := row*picture.Stride + (quiet+x)*scale

				for dx := 0; dx < scale; dx++ {
					picture.Pix[start+dx] = 0
				}
			}
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, picture); err != nil {
		return nil, fmt.Errorf("QR-код: %w", err)
	}

	return buf.Bytes(), nil
}
