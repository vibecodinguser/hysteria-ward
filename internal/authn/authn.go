// Пакет authn пускает в панель только вошедшего.
//
// Вход — единственное место, где панель принимает чужой ввод до всякой проверки,
// поэтому здесь собрано всё, что от него защищает: пароль лежит хешем, сессию
// нельзя угадать, изменяющий запрос нельзя подделать чужим сайтом, а перебор
// пароля становится всё дороже по времени.
package authn

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Умолчания сервиса.
const (
	// idle — сколько сессия живёт без обращений. Пятнадцати минут хватает с
	// запасом на паузу в работе администратора и мало для того, чтобы брошенный
	// открытым браузер стал дырой.
	idle = 15 * time.Minute

	// renewEvery — как часто продление сессии доходит до базы. Интерфейс
	// обращается к панели часто, а запись в базу — это запись на диск.
	renewEvery = time.Minute

	// freeFailures — сколько неудачных попыток подряд панель прощает: право
	// ошибиться в пароле есть у всех.
	freeFailures = 5

	// minDelay и maxDelay — во сколько обходится неудачная попытка сверх
	// прощённых. Задержка удваивается с каждой следующей и упирается в предел.
	minDelay = 250 * time.Millisecond
	maxDelay = 4 * time.Second

	// failureForget — через сколько счёт неудач забывается. Иначе редкие опечатки
	// копились бы месяцами и однажды задержали бы самого администратора.
	failureForget = 15 * time.Minute

	// tokenBytes — длина сессионного токена и csrf-токена: 32 байта случайности
	// угадать нельзя ни за какое время.
	tokenBytes = 32

	// verificationsAtOnce — сколько проверок пароля считается одновременно.
	// Argon2id намеренно требует памяти, поэтому поток неудачных попыток без
	// предела съел бы всю память сервера.
	verificationsAtOnce = 2
)

// Учётные данные, которые панель заводит себе при первом запуске.
//
// Пароль по умолчанию — зло, и оно выбрано сознательно: панель должна
// открываться сразу после установки, а не требовать настройки вслепую. Пока он не
// сменён, панель предупреждает об этом в журнале, а снаружи недоступна: по
// умолчанию она слушает петлю, и публичный доступ включают отдельно.
const (
	DefaultName     = "admin"
	DefaultPassword = "admin"
)

var (
	// ErrDenied — имя или пароль не подошли. Это единственный ответ на любую
	// неудачу входа: знать, угадано ли имя, перебирающему незачем.
	ErrDenied = errors.New("неверное имя или пароль")

	// ErrNoSession — входа нет: сессии не было, она отжила или её закрыли.
	ErrNoSession = errors.New("входа нет")
)

// Idle — сколько сессия живёт без обращений. Нужно тому, кто ставит cookie: срок
// жизни сессии считает панель, а не браузер.
const Idle = idle

// Session — один вход администратора: одно открытое окно, одна сессия.
type Session struct {
	// Token — то, что лежит в cookie. Угадать его нельзя, поэтому он же и есть
	// доказательство входа, и другого доказательства у панели нет.
	Token string

	// CSRF — то, что интерфейс обязан приложить к изменяющему запросу: чужой сайт
	// пришлёт cookie, но подставить заголовок не сможет.
	CSRF string

	// Created — когда вошли.
	Created time.Time

	// Seen — когда сессию видели в последний раз. От этого времени считается срок
	// жизни: пока администратор работает, сессия жива.
	Seen time.Time
}

// Expired сообщает, что сессия застоялась.
func (s Session) Expired(now time.Time, idle time.Duration) bool {
	return now.Sub(s.Seen) > idle
}

// ValidCSRF сверяет токен из запроса с токеном сессии.
func (s Session) ValidCSRF(token string) bool {
	return subtle.ConstantTimeCompare([]byte(s.CSRF), []byte(token)) == 1
}

// Storage — что authn нужно от базы. Интерфейс объявлен здесь, а не в store:
// проверка пароля не должна знать ни про bbolt, ни про формат записи.
type Storage interface {
	// Credential возвращает имя администратора и хеш его пароля. Последнее
	// значение ложно, если пароль ещё не задавали.
	Credential() (name, hash string, ok bool, err error)
	SaveCredential(name, hash string) error

	SaveSession(session Session) error
	Session(token string) (Session, bool, error)
	DeleteSession(token string) error

	// DeleteStaleSessions выметает сессии, которых не касались с указанного
	// времени, и возвращает, сколько вымел.
	DeleteStaleSessions(before time.Time) (int, error)
}

// Service — вход в панель: проверка пароля, сессии и задержка при переборе.
type Service struct {
	storage Storage
	logger  *slog.Logger

	idle          time.Duration
	renewEvery    time.Duration
	freeFailures  int
	minDelay      time.Duration
	maxDelay      time.Duration
	failureForget time.Duration
	now           func() time.Time

	// verifications пропускает проверки пароля по нескольку за раз.
	verifications chan struct{}

	mu       sync.Mutex
	failures map[string]failure
}

// failure — след неудачных попыток с одного адреса.
type failure struct {
	count int
	seen  time.Time
}

// New собирает сервис и выметает сессии, срок которых прошёл: перезапуск панели —
// хороший момент, чтобы не таскать за собой мусор.
func New(storage Storage, logger *slog.Logger) (*Service, error) {
	s := &Service{
		storage:       storage,
		logger:        logger,
		idle:          idle,
		renewEvery:    renewEvery,
		freeFailures:  freeFailures,
		minDelay:      minDelay,
		maxDelay:      maxDelay,
		failureForget: failureForget,
		now:           time.Now,
		verifications: make(chan struct{}, verificationsAtOnce),
		failures:      make(map[string]failure),
	}

	if _, err := storage.DeleteStaleSessions(s.now().Add(-s.idle)); err != nil {
		return nil, err
	}

	return s, nil
}

// Login проверяет имя и пароль и заводит сессию.
//
// От адреса зависит только задержка при переборе: сам адрес нигде не хранится, а
// неудачные попытки живут в памяти. Перезапуск панели счёт обнуляет — за это время
// перебрать пароль всё равно нельзя.
func (s *Service) Login(ctx context.Context, addr, name, password string) (Session, error) {
	if err := s.verify(ctx, name, password); err != nil {
		s.logger.Warn("неудачный вход", "addr", addr, "err", err)

		s.remember(addr)

		// Ответ задерживается: перебор пароля должен упираться во время, а не в
		// число попыток.
		if wait := s.delay(s.failuresOf(addr)); wait > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
			}
		}

		return Session{}, ErrDenied
	}

	s.forget(addr)

	return s.open()
}

// Check узнаёт сессию по токену и продлевает её: пока администратор работает,
// вход не заканчивается.
func (s *Service) Check(token string) (Session, error) {
	if token == "" {
		return Session{}, ErrNoSession
	}

	session, ok, err := s.storage.Session(token)
	if err != nil {
		return Session{}, err
	}
	if !ok {
		return Session{}, ErrNoSession
	}

	now := s.now()
	if session.Expired(now, s.idle) {
		// Отжившую сессию закрываем сразу: понадобиться она уже не может.
		if err := s.storage.DeleteSession(token); err != nil {
			s.logger.Error("не удалось закрыть отжившую сессию", "err", err)
		}

		return Session{}, ErrNoSession
	}

	// Продление доходит до базы не на каждом запросе, а раз в минуту: интерфейс
	// обращается часто, и запись на каждый запрос была бы записью на диск.
	if now.Sub(session.Seen) >= s.renewEvery {
		session.Seen = now
		if err := s.storage.SaveSession(session); err != nil {
			return Session{}, err
		}
	}

	return session, nil
}

// Logout закрывает сессию: вход заканчивается, предъявлять больше нечего.
func (s *Service) Logout(token string) error {
	if token == "" {
		return nil
	}

	return s.storage.DeleteSession(token)
}

// SetCredential задаёт имя администратора и хеш его пароля.
//
// Проверять пароль здесь нечем: решение о том, годится ли он, принимает тот, кто
// его спрашивает.
func (s *Service) SetCredential(name, hash string) error {
	if name == "" {
		return errors.New("не задано имя администратора")
	}

	return s.storage.SaveCredential(name, hash)
}

// Credential возвращает имя администратора и хеш его пароля. Последнее значение
// ложно, если пароль ещё не задавали.
func (s *Service) Credential() (string, string, bool, error) {
	return s.storage.Credential()
}

// AdminName возвращает имя администратора — то, которое интерфейс показывает
// вошедшему. Пока учётных данных нет, это имя по умолчанию.
func (s *Service) AdminName() (string, error) {
	name, _, ok, err := s.storage.Credential()
	if err != nil {
		return "", err
	}

	if !ok {
		return DefaultName, nil
	}

	return name, nil
}

// EnsureCredential заводит учётные данные по умолчанию, если их ещё нет, и
// сообщает, действует ли сейчас пароль по умолчанию.
//
// Возвращаемое значение — повод предупредить администратора, а не ошибка: панель
// работает и так.
func (s *Service) EnsureCredential() (bool, error) {
	name, hash, ok, err := s.storage.Credential()
	if err != nil {
		return false, err
	}

	if !ok {
		fresh, err := Hash(DefaultPassword)
		if err != nil {
			return false, err
		}

		return true, s.storage.SaveCredential(DefaultName, fresh)
	}

	matched, err := Verify(hash, DefaultPassword)
	if err != nil {
		return false, err
	}

	// Сверяем и имя: если администратор сменил и его, пароль по умолчанию уже
	// ничего не открывает.
	return matched && name == DefaultName, nil
}

// verify проверяет пару «имя и пароль».
//
// Проверка идёт до конца и при неверном имени: иначе по времени ответа было бы
// видно, угадано ли имя, и половина работы перебирающего стала бы бесплатной.
func (s *Service) verify(ctx context.Context, name, password string) error {
	storedName, hash, ok, err := s.storage.Credential()
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("пароль администратора не задан")
	}

	// Проверок пароля за раз немного: Argon2id намеренно жадный до памяти.
	select {
	case s.verifications <- struct{}{}:
		defer func() { <-s.verifications }()
	case <-ctx.Done():
		return ctx.Err()
	}

	matched, err := Verify(hash, password)
	if err != nil {
		return err
	}

	if subtle.ConstantTimeCompare([]byte(name), []byte(storedName)) != 1 || !matched {
		return errors.New("пароль не подходит")
	}

	return nil
}

// open заводит сессию.
func (s *Service) open() (Session, error) {
	token, err := newToken()
	if err != nil {
		return Session{}, err
	}

	csrf, err := newToken()
	if err != nil {
		return Session{}, err
	}

	now := s.now()
	session := Session{Token: token, CSRF: csrf, Created: now, Seen: now}
	if err := s.storage.SaveSession(session); err != nil {
		return Session{}, err
	}

	return session, nil
}

// remember запоминает неудачную попытку с адреса.
func (s *Service) remember(addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f := s.failures[addr]
	if s.now().Sub(f.seen) > s.failureForget {
		// Прежние опечатки уже не в счёт: счёт начинается заново.
		f = failure{}
	}

	f.count++
	f.seen = s.now()
	s.failures[addr] = f
}

// forget забывает неудачные попытки: вход удался.
func (s *Service) forget(addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.failures, addr)
}

// failuresOf возвращает число неудачных попыток подряд с адреса.
func (s *Service) failuresOf(addr string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.failures[addr].count
}

// delay — сколько панель ждёт перед ответом на неудачную попытку.
//
// Первые попытки не задерживаются вовсе: администратор имеет право ошибиться.
// Дальше задержка удваивается с каждой попыткой, поэтому перебор дорожает не
// вдвое, а на порядки.
func (s *Service) delay(count int) time.Duration {
	extra := count - s.freeFailures
	if extra <= 0 {
		return 0
	}

	wait := s.minDelay
	for i := 1; i < extra && wait < s.maxDelay; i++ {
		wait *= 2
	}

	if wait > s.maxDelay {
		return s.maxDelay
	}

	return wait
}

// newToken возвращает случайный токен: вся защита сессии держится на том, что его
// нельзя угадать.
func newToken() (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("случайные байты: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(raw), nil
}
