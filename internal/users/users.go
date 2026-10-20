// Пакет users ведёт пользователей сервера: кого пускать, кого нет и кто сейчас
// подключён.
//
// Пользователи живут в базе, но ядро спрашивает о них на каждое подключение и на
// каждый пакет. Поэтому здесь лежит ещё и список в памяти: он обновляется после
// каждой правки интерфейса, и ядро обходится без походов в базу на горячем пути.
package users

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// passwordBytes — сколько случайных байт в пароле клиента. 18 байт дают 24
// символа base64url: перебрать такое не по силам, а скопировать не трудно.
const passwordBytes = 18

const (
	// quotaFlushEvery — как часто расход квот доходит до базы. Горячий путь
	// копит его в памяти, а запись идёт по часам, как и история трафика.
	quotaFlushEvery = 5 * time.Second

	// quotaPeriodCheckEvery — как часто проверяется граница месяца. Квота
	// месячная, и расход должен обнулиться, даже если панель работает без
	// перезапуска.
	quotaPeriodCheckEvery = time.Minute
)

// quotaState — расход одного пользователя за текущий месяц.
type quotaState struct {
	used  uint64
	since time.Time
}

// Registry — пользователи в памяти: кто проходит авторизацию и кому можно
// отправлять трафик.
type Registry struct {
	db *store.Store

	// mu защищает users и online. Читателей много — авторизация и учёт трафика
	// идут из горутин ядра, — а пишет один: правка из интерфейса.
	mu     sync.RWMutex
	users  map[string]store.User
	online map[string]bool

	// quota — расход за текущий месяц, отдельно от записи пользователя. Правка
	// пользователя не должна обнулять счётчик, а горячий путь не должен ходить
	// в базу на каждый пакет.
	//
	// quotaMu отдельный от mu: проверка квоты меняет счётчик на каждом пакете и
	// не должна запирать чтение списка пользователей. quotaWrite сериализует
	// запись в базу: сброс квоты не должен проиграть фоновому сохранению старого
	// расхода.
	quotaMu    sync.Mutex
	quotaWrite sync.Mutex
	quota      map[string]quotaState
	dirtyQuota map[string]bool

	// now подменяется тестом, которому нужна граница месяца.
	now func() time.Time
}

// New читает пользователей и расход квот из базы.
func New(db *store.Store) (*Registry, error) {
	r := &Registry{
		db:         db,
		online:     make(map[string]bool),
		quota:      make(map[string]quotaState),
		dirtyQuota: make(map[string]bool),
		now:        time.Now,
	}
	if err := r.Reload(); err != nil {
		return nil, err
	}

	return r, nil
}

// Reload перечитывает пользователей из базы. Вызывается после каждой правки:
// удалённый пользователь должен перестать пускаться и начать рваться.
//
// Расход квот при этом не сбрасывается: он живёт отдельно от записи и меняется
// на каждом пакете. Из базы берутся только записи пользователей, которых ещё не
// было в памяти.
func (r *Registry) Reload() error {
	list, err := r.db.Users()
	if err != nil {
		return err
	}

	usages, err := r.db.QuotaUsages()
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

	now := r.clock()
	start := monthStart(now)

	r.quotaMu.Lock()
	defer r.quotaMu.Unlock()

	quota := make(map[string]quotaState, len(users))
	for id := range users {
		state, ok := r.quota[id]
		if !ok {
			if saved, found := usages[id]; found {
				state = quotaState{used: saved.Used, since: saved.Since}
			} else {
				state.since = start
			}
		}

		if state.since.IsZero() {
			state.since = start
		}
		if state.since.Before(start) {
			state.used = 0
			state.since = start
			r.dirtyQuota[id] = true
		}

		quota[id] = state
	}

	r.quota = quota

	for id := range r.dirtyQuota {
		if _, ok := users[id]; !ok {
			delete(r.dirtyQuota, id)
		}
	}

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
	var matched *store.User
	for _, user := range r.users {
		if !user.Enabled {
			continue
		}

		// Пароли сравниваются за одинаковое время: иначе по времени ответа
		// можно было бы угадывать пароль по символу.
		if subtle.ConstantTimeCompare([]byte(user.Password), []byte(password)) == 1 {
			user := user
			matched = &user

			break
		}
	}
	r.mu.RUnlock()

	if matched == nil {
		return "", false
	}

	r.quotaMu.Lock()
	defer r.quotaMu.Unlock()

	r.rollQuotaLocked(matched.ID, r.clock())
	if !r.allowQuotaLocked(matched.ID, matched.QuotaBytes, 0) {
		return "", false
	}

	return connectionID(matched.ID, matched.Password), true
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
// статистики. false рвёт соединение — так выключение, удаление, смена пароля и
// исчерпанная квота достают до уже подключённых: следующий пакет закрывает
// соединение.
//
// Расход квоты копится здесь же, в памяти: запись в базу идёт по часам, а не на
// каждый пакет.
func (r *Registry) Traffic(id string, tx, rx uint64) (string, bool) {
	r.mu.RLock()
	userID, ok := r.resolveLocked(id)
	if !ok {
		r.mu.RUnlock()
		return "", false
	}
	user := r.users[userID]
	r.mu.RUnlock()

	add := tx
	if ^uint64(0)-add < rx {
		add = ^uint64(0)
	} else {
		add += rx
	}

	r.quotaMu.Lock()
	defer r.quotaMu.Unlock()

	if !r.allowQuotaLocked(userID, user.QuotaBytes, add) {
		return "", false
	}

	return userID, true
}

// Quota сообщает расход, предел и начало текущего периода. Для пользователя без
// записи расхода возвращается пустой месяц: это нормальное состояние после
// заведения.
func (r *Registry) Quota(id string) (used, limit uint64, since time.Time) {
	r.mu.RLock()
	user, ok := r.users[id]
	r.mu.RUnlock()

	if !ok {
		return 0, 0, monthStart(r.clock())
	}

	now := r.clock()

	r.quotaMu.Lock()
	defer r.quotaMu.Unlock()

	state := r.rollQuotaLocked(id, now)
	return state.used, user.QuotaBytes, state.since
}

// ResetQuota обнуляет расход пользователя и сразу пишет это в базу: сброс — не
// горячий путь, и терять его при падении незачем.
func (r *Registry) ResetQuota(id string) error {
	r.mu.RLock()
	_, ok := r.users[id]
	r.mu.RUnlock()
	if !ok {
		return store.ErrUserMissing
	}

	since := monthStart(r.clock())

	r.quotaWrite.Lock()
	defer r.quotaWrite.Unlock()

	r.quotaMu.Lock()
	r.quota[id] = quotaState{since: since}
	delete(r.dirtyQuota, id)
	r.quotaMu.Unlock()

	return r.db.ResetQuota(id, since)
}

// FlushQuotas записывает накопленный расход в базу. Повторный вызов безопасен:
// записываются только изменившиеся записи.
func (r *Registry) FlushQuotas() error {
	r.quotaWrite.Lock()
	defer r.quotaWrite.Unlock()

	r.quotaMu.Lock()
	saved := make(map[string]quotaState, len(r.dirtyQuota))
	for id := range r.dirtyQuota {
		saved[id] = r.quota[id]
	}
	r.quotaMu.Unlock()

	if len(saved) == 0 {
		return nil
	}

	usages := make(map[string]store.QuotaUsage, len(saved))
	for id, state := range saved {
		usages[id] = store.QuotaUsage{Used: state.used, Since: state.since}
	}

	if err := r.db.SaveQuotaUsages(usages); err != nil {
		return err
	}

	// Пока шла запись, горячий путь мог прибавить ещё байты. Снимаем отметку
	// только с тех записей, которые не изменились: иначе прирост потерялся бы.
	r.quotaMu.Lock()
	for id, state := range saved {
		if r.quota[id] == state {
			delete(r.dirtyQuota, id)
		}
	}
	r.quotaMu.Unlock()

	return nil
}

// RollQuotas обнуляет расход, если начался новый месяц. Вызывается по часам и
// при чтении: панель может работать дольше месяца без перезапуска.
func (r *Registry) RollQuotas() {
	now := r.clock()

	r.quotaMu.Lock()
	defer r.quotaMu.Unlock()

	r.rollAllQuotasLocked(now)
}

// Run ведёт часы квот: пишет расход в базу и переходит на новый месяц.
func (r *Registry) Run(ctx context.Context) {
	flush := time.NewTicker(quotaFlushEvery)
	period := time.NewTicker(quotaPeriodCheckEvery)

	defer flush.Stop()
	defer period.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = r.FlushQuotas()
			return

		case <-flush.C:
			_ = r.FlushQuotas()

		case <-period.C:
			r.RollQuotas()
			_ = r.FlushQuotas()
		}
	}
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

// clock возвращает текущее время. Отдельная функция, чтобы тест мог подменить
// границу месяца, не трогая системные часы.
func (r *Registry) clock() time.Time {
	if r.now == nil {
		return time.Now()
	}

	return r.now()
}

// monthStart — начало месяца по UTC. Квота месячная, и граница не должна
// зависеть от часового пояса сервера.
func monthStart(t time.Time) time.Time {
	t = t.UTC()

	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// rollQuotaLocked переводит запись на текущий месяц, если она отстала.
func (r *Registry) rollQuotaLocked(id string, now time.Time) quotaState {
	state, ok := r.quota[id]
	if !ok {
		state.since = monthStart(now)
	}

	start := monthStart(now)
	if state.since.IsZero() {
		state.since = start
	}
	if state.since.Before(start) {
		state.used = 0
		state.since = start
		r.dirtyQuota[id] = true
	}

	r.quota[id] = state

	return state
}

// rollAllQuotasLocked переводит все записи на текущий месяц.
func (r *Registry) rollAllQuotasLocked(now time.Time) {
	for id := range r.quota {
		r.rollQuotaLocked(id, now)
	}
}

// allowQuotaLocked прибавляет add к расходу и сообщает, можно ли пропустить
// трафик. При исчерпании расход доводится до предела: остаток пакета всё равно
// не пройдёт, а показать «ровно предел» честнее, чем «предел минус пакет».
//
// Границу месяца здесь не проверяем: это делает Run раз в минуту, а на горячем
// пути лишний time.Now на каждый пакет не нужен.
func (r *Registry) allowQuotaLocked(id string, limit, add uint64) bool {
	state, ok := r.quota[id]
	if !ok {
		state.since = monthStart(r.clock())
	}

	if limit == 0 {
		if ^uint64(0)-state.used < add {
			state.used = ^uint64(0)
		} else {
			state.used += add
		}
		r.quota[id] = state
		r.dirtyQuota[id] = true

		return true
	}

	if state.used >= limit {
		return false
	}

	remaining := limit - state.used
	if add > remaining {
		state.used = limit
		r.quota[id] = state
		r.dirtyQuota[id] = true

		return false
	}

	state.used += add
	r.quota[id] = state
	if add > 0 {
		r.dirtyQuota[id] = true
	}

	return true
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
