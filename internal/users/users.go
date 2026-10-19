// Пакет users ведёт пользователей сервера: кого пускать, кого нет и кто сейчас
// подключён.
//
// Пользователи живут в базе, но ядро спрашивает о них на каждое подключение и на
// каждый пакет. Поэтому здесь лежит ещё и список в памяти: он обновляется после
// каждой правки интерфейса, и ядро обходится без походов в базу на горячем пути.
package users

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
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
	r.mu.Unlock()

	return nil
}

// Auth отвечает ядру, кто стоит за строкой авторизации. Пускаются только
// включённые пользователи: выключенный — тот же чужой.
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
			return id, true
		}
	}

	return "", false
}

// Traffic сообщает ядру, продолжать ли соединение. false рвёт его — так
// выключение и удаление достают до уже подключённых: следующий пакет закрывает
// соединение.
func (r *Registry) Traffic(id string, _, _ uint64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	user, ok := r.users[id]

	return ok && user.Enabled
}

// Online запоминает, подключён ли пользователь: интерфейс показывает это в
// списке.
func (r *Registry) Online(id string, online bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !online {
		delete(r.online, id)

		return
	}

	r.online[id] = true
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

// NewPassword придумывает пароль клиента. Символы — base64url: они переживают
// копирование, вставку в ключ и URL без экранирования.
func NewPassword() (string, error) {
	raw := make([]byte, passwordBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("пароль пользователя: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(raw), nil
}
