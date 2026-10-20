// Пакет users ведёт пользователей сервера: кого пускать, кого нет и кто сейчас
// подключён.
//
// Пользователи живут в базе, но ядро спрашивает о них на каждое подключение и на
// каждый пакет. Поэтому здесь лежит ещё и список в памяти: он обновляется после
// каждой правки интерфейса, и ядро обходится без походов в базу на горячем пути.
package users

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// passwordBytes — сколько случайных байт в пароле клиента. 18 байт дают 24
// символа base64url: перебрать такое не по силам, а скопировать не трудно.
const passwordBytes = 18

// Registry — пользователи в памяти: кто проходит авторизацию и кому можно
// отправлять трафик.
type Registry struct {
	db *store.Store

	// mu защищает users и online. Читателей много — авторизация и учёт трафика
	// идут из горутин ядра, — а пишет один: правка из интерфейса.
	mu     sync.RWMutex
	users  map[string]store.User
	online map[string]bool
}

// New читает пользователей из базы.
func New(db *store.Store) (*Registry, error) {
	r := &Registry{db: db, online: make(map[string]bool)}
	if err := r.Reload(); err != nil {
		return nil, err
	}

	return r, nil
}

// Reload перечитывает пользователей из базы. Вызывается после каждой правки:
// удалённый пользователь должен перестать пускаться и начать рваться.
func (r *Registry) Reload() error {
	list, err := r.db.Users()
	if err != nil {
		return err
	}

	users := make(map[string]store.User, len(list))
	for _, user := range list {
		users[user.ID] = user
	}

	r.mu.Lock()
	r.users = users

	// Подключённых удалённых тоже забываем: в списке их уже нет, а запись о них
	// осталась бы до перезапуска панели.
	for id := range r.online {
		if _, ok := users[id]; !ok {
			delete(r.online, id)
		}
	}
	r.mu.Unlock()

	return nil
}

// authSeparator разделяет в строке авторизации идентификатор пользователя и
// версию его пароля. Двоеточие не встречается ни в том, ни в другом.
const authSeparator = ":"

// Auth отвечает ядру, кто стоит за строкой авторизации. Пускаются только
// включённые пользователи: выключенный — тот же чужой.
//
// Возвращается не идентификатор пользователя, а строка соединения: идентификатор
// и версия пароля. Ядро передаёт её в учёт трафика на каждом пакете, поэтому по
// ней видно, с каким паролем соединение открылось. Смена пароля меняет версию, и
// старое соединение перестаёт проходить проверку — его рвёт следующий пакет.
func (r *Registry) Auth(password string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for id, user := range r.users {
		if !user.Enabled {
			continue
		}

		// Пароли сравниваются за одинаковое время: иначе по времени ответа
		// можно было бы угадывать пароль по символу.
		if subtle.ConstantTimeCompare([]byte(user.Password), []byte(password)) == 1 {
			return connectionID(id, user.Password), true
		}
	}

	return "", false
}

// Resolve называет пользователя по строке соединения. Строка годится, только пока
// пароль, с которым соединение открылось, — тот же, что у пользователя сейчас.
func (r *Registry) Resolve(id string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.resolveLocked(id)
}

// resolveLocked — Resolve под уже взятым mu.
func (r *Registry) resolveLocked(id string) (string, bool) {
	userID, version, ok := strings.Cut(id, authSeparator)
	if !ok {
		return "", false
	}

	user, ok := r.users[userID]
	if !ok || !user.Enabled {
		return "", false
	}

	if subtle.ConstantTimeCompare([]byte(version), []byte(passwordVersion(user.Password))) != 1 {
		return "", false
	}

	return userID, true
}

// Traffic сообщает ядру, продолжать ли соединение, и называет пользователя для
// статистики. false рвёт соединение — так выключение, удаление и смена пароля
// достают до уже подключённых: следующий пакет закрывает соединение.
func (r *Registry) Traffic(id string, _, _ uint64) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.resolveLocked(id)
}

// Online запоминает, подключён ли пользователь: интерфейс показывает это в
// списке.
//
// Соединение со старым паролем уже не считается: его пользователь либо
// переподключился с новым, либо сейчас получит отказ на следующем пакете.
func (r *Registry) Online(id string, online bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !online {
		// Отключение старого соединения должно снять отметку, даже если пароль
		// уже сменился и строка перестала проходить проверку: иначе пользователь
		// остался бы «в сети» без единого соединения.
		if userID, _, ok := strings.Cut(id, authSeparator); ok {
			delete(r.online, userID)
		}

		return
	}

	userID, ok := r.resolveLocked(id)
	if !ok {
		return
	}

	r.online[userID] = true
}

// IsOnline сообщает, подключён ли пользователь сейчас.
func (r *Registry) IsOnline(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.online[id]
}

// PasswordInUse сообщает, занят ли пароль другим пользователем. Пароль — это
// единственное, чем клиент называет себя серверу, поэтому два одинаковых пароля
// означали бы, что авторизация не говорит, кто именно подключился.
func (r *Registry) PasswordInUse(password, exceptID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for id, user := range r.users {
		if id != exceptID && user.Password == password {
			return true
		}
	}

	return false
}

// connectionID собирает строку соединения из пользователя и его пароля.
func connectionID(userID, password string) string {
	return userID + authSeparator + passwordVersion(password)
}

// passwordVersion — короткий отпечаток пароля. Сам пароль в строку соединения не
// попадает: она уходит в учёт трафика и может оказаться в журнале.
func passwordVersion(password string) string {
	sum := sha256.Sum256([]byte(password))

	return hex.EncodeToString(sum[:8])
}

// NewPassword придумывает пароль клиента. Символы — base64url: они переживают
// копирование, вставку в ключ и URL без экранирования.
func NewPassword() (string, error) {
	raw := make([]byte, passwordBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("пароль пользователя: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(raw), nil
}
