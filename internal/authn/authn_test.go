package authn

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

// memoryStorage — хранилище в памяти: сервису от базы нужны ровно эти записи,
// поэтому настоящая база в этих тестах ни к чему.
type memoryStorage struct {
	credential Credential
	ok         bool

	// err — беда хранилища: так проверяется, что она не выдаётся за неверный
	// пароль.
	err error

	sessions map[string]Session
}

func newMemoryStorage(t *testing.T) *memoryStorage {
	t.Helper()

	hash, err := Hash("пароль")
	if err != nil {
		t.Fatalf("Hash() = %v", err)
	}

	return &memoryStorage{
		credential: Credential{Name: "admin", Hash: hash},
		ok:         true,
		sessions:   make(map[string]Session),
	}
}

func (m *memoryStorage) Credential() (Credential, bool, error) {
	if m.err != nil {
		return Credential{}, false, m.err
	}

	return m.credential, m.ok, nil
}

func (m *memoryStorage) SaveCredential(credential Credential) error {
	m.credential, m.ok = credential, true

	return nil
}

func (m *memoryStorage) SaveSession(session Session) error {
	m.sessions[session.Token] = session

	return nil
}

func (m *memoryStorage) Session(token string) (Session, bool, error) {
	session, ok := m.sessions[token]

	return session, ok, nil
}

func (m *memoryStorage) DeleteSession(token string) error {
	delete(m.sessions, token)

	return nil
}

func (m *memoryStorage) DeleteStaleSessions(before time.Time) (int, error) {
	removed := 0

	for token, session := range m.sessions {
		if session.Seen.Before(before) {
			delete(m.sessions, token)
			removed++
		}
	}

	return removed, nil
}

// clock — часы теста. Сроки жизни сессий и забывание неудач проверяются сдвигом
// времени, а не ожиданием.
type clock struct{ at time.Time }

func (c *clock) now() time.Time { return c.at }

func (c *clock) advance(d time.Duration) { c.at = c.at.Add(d) }

// newService собирает сервис с часами теста.
func newService(t *testing.T, storage Storage) (*Service, *clock) {
	t.Helper()

	s, err := New(storage, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	clk := &clock{at: time.Now()}
	s.now = clk.now

	return s, clk
}

// fail подставляет счёт неудач так, будто с адреса уже столько раз не угадали
// пароль. Сервису безразлично, кто их набрал, а тесту — важно, чтобы до задержки
// не пришлось пять раз пересчитывать Argon2.
func (s *Service) fail(t *testing.T, addr string, count int) {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.failures[addr] = failure{count: count, seen: s.now()}
}

// TestFullCycle — полный круг, заявленный в задаче: вход, продление, выход и
// отказ.
func TestFullCycle(t *testing.T) {
	s, clk := newService(t, newMemoryStorage(t))

	const addr = "127.0.0.1:5000"
	ctx := context.Background()

	session, err := s.Login(ctx, addr, "admin", "пароль")
	if err != nil {
		t.Fatalf("вход: Login() = %v", err)
	}

	// Администратор работает, и сессия переживает свой срок: продление сдвигает
	// его вперёд.
	clk.advance(renewEvery)
	if _, err := s.Check(session.Token); err != nil {
		t.Fatalf("продление: Check() = %v", err)
	}

	clk.advance(idle - time.Minute)
	if _, err := s.Check(session.Token); err != nil {
		t.Fatalf("жизнь после продления: Check() = %v", err)
	}

	if err := s.Logout(session.Token); err != nil {
		t.Fatalf("выход: Logout() = %v", err)
	}

	if _, err := s.Check(session.Token); !errors.Is(err, ErrNoSession) {
		t.Errorf("после выхода: Check() = %v, ожидался отказ", err)
	}
}

// TestLoginAndLogout — вход заводит сессию, сессия узнаётся, выход её закрывает.
func TestLoginAndLogout(t *testing.T) {
	storage := newMemoryStorage(t)
	s, _ := newService(t, storage)

	session, err := s.Login(context.Background(), "127.0.0.1:5000", "admin", "пароль")
	if err != nil {
		t.Fatalf("Login() = %v", err)
	}

	if session.Token == "" || session.CSRF == "" {
		t.Fatalf("сессия без токена: %+v", session)
	}
	if session.Token == session.CSRF {
		t.Error("токен сессии и csrf-токен совпали")
	}

	if _, err := s.Check(session.Token); err != nil {
		t.Errorf("Check() = %v, ожидалась живая сессия", err)
	}

	// Сессия живёт в хранилище, а не в памяти сервиса: её узнаёт и сервис,
	// собранный заново, — так же, как после перезапуска панели.
	restarted, err := New(storage, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	if _, err := restarted.Check(session.Token); err != nil {
		t.Errorf("после перезапуска Check() = %v, ожидалась живая сессия", err)
	}

	if err := s.Logout(session.Token); err != nil {
		t.Fatalf("Logout() = %v", err)
	}

	if _, err := s.Check(session.Token); !errors.Is(err, ErrNoSession) {
		t.Errorf("после выхода Check() = %v, ожидался отказ", err)
	}
}

// TestLoginDeniesWrongCredentials — чужой пароль и чужое имя отвечают одинаково:
// знать, угадано ли имя, перебирающему незачем.
func TestLoginDeniesWrongCredentials(t *testing.T) {
	s, _ := newService(t, newMemoryStorage(t))

	for _, attempt := range []struct{ name, password string }{
		{"admin", "неверный"},
		{"другой", "пароль"},
		{"", ""},
	} {
		_, err := s.Login(context.Background(), "127.0.0.1:5000", attempt.name, attempt.password)
		if !errors.Is(err, ErrDenied) {
			t.Errorf("Login(%q, %q) = %v, ожидался отказ", attempt.name, attempt.password, err)
		}
	}
}

// TestStorageFailureIsNotDenial — беда на стороне панели не выдаётся за неверный
// пароль: администратору нужно знать, что дело в панели, а не в его памяти.
func TestStorageFailureIsNotDenial(t *testing.T) {
	storage := newMemoryStorage(t)
	storage.err = errors.New("база не открылась")

	s, _ := newService(t, storage)

	_, err := s.Login(context.Background(), "127.0.0.1:5000", "admin", "пароль")
	if err == nil {
		t.Fatal("Login() прошёл, ожидалась ошибка")
	}
	if errors.Is(err, ErrDenied) {
		t.Errorf("Login() = %v, ошибка хранилища выдана за отказ", err)
	}
}

// TestCheckForgetsStaleSession — сессия, которой не пользовались дольше срока,
// закрывается и выметается сразу, а не хранится вечно.
func TestCheckForgetsStaleSession(t *testing.T) {
	storage := newMemoryStorage(t)
	s, clk := newService(t, storage)

	session, err := s.Login(context.Background(), "127.0.0.1:5000", "admin", "пароль")
	if err != nil {
		t.Fatalf("Login() = %v", err)
	}

	clk.advance(idle + time.Second)

	if _, err := s.Check(session.Token); !errors.Is(err, ErrNoSession) {
		t.Errorf("Check() = %v, ожидался отказ", err)
	}

	if _, ok := storage.sessions[session.Token]; ok {
		t.Error("отжившая сессия осталась в хранилище")
	}
}

// TestCheckRenewsSessionOnActivity — работа продлевает вход, но продление доходит
// до базы не на каждом запросе: иначе интерфейс писал бы на диск каждым щелчком.
func TestCheckRenewsSessionOnActivity(t *testing.T) {
	storage := newMemoryStorage(t)
	s, clk := newService(t, storage)

	session, err := s.Login(context.Background(), "127.0.0.1:5000", "admin", "пароль")
	if err != nil {
		t.Fatalf("Login() = %v", err)
	}

	clk.advance(renewEvery / 2)
	if _, err := s.Check(session.Token); err != nil {
		t.Fatalf("Check() = %v", err)
	}

	if got := storage.sessions[session.Token].Seen; !got.Equal(session.Seen) {
		t.Error("сессия продлена раньше, чем нужно")
	}

	clk.advance(renewEvery)
	if _, err := s.Check(session.Token); err != nil {
		t.Fatalf("Check() = %v", err)
	}

	if got := storage.sessions[session.Token].Seen; got.Equal(session.Seen) {
		t.Error("сессия не продлена, хотя администратор работает")
	}
}

// TestDelayGrowsAfterFreeFailures — первые неудачные попытки прощаются, дальше
// задержка удваивается и упирается в предел.
func TestDelayGrowsAfterFreeFailures(t *testing.T) {
	for _, c := range []struct {
		failures int
		want     time.Duration
	}{
		{0, 0},
		{1, 0},
		{freeFailures, 0},
		{freeFailures + 1, minDelay},
		{freeFailures + 2, 2 * minDelay},
		{freeFailures + 3, 4 * minDelay},
		{freeFailures + 4, 8 * minDelay},
		{freeFailures + 5, maxDelay},
		{50, maxDelay},
	} {
		if got := delay(c.failures); got != c.want {
			t.Errorf("delay(%d) = %v, ожидалось %v", c.failures, got, c.want)
		}
	}
}

// TestLoginWaitsAfterFailures — задержка не просто считается, а откладывает ответ.
func TestLoginWaitsAfterFailures(t *testing.T) {
	s, _ := newService(t, newMemoryStorage(t))

	const addr = "127.0.0.1:5000"
	s.fail(t, addr, freeFailures)

	started := time.Now()
	if _, err := s.Login(context.Background(), addr, "admin", "неверный"); !errors.Is(err, ErrDenied) {
		t.Fatalf("Login() = %v, ожидался отказ", err)
	}

	if elapsed := time.Since(started); elapsed < minDelay {
		t.Errorf("ответ пришёл через %v, ожидалась задержка не меньше %v", elapsed, minDelay)
	}
}

// TestDelayIsNotSkippedWhenClientHangsUp — задержка не пропускается отменой
// запроса.
//
// Перебирающему ответ не нужен: он может бросить соединение и взяться за
// следующую попытку. Если бы задержка пропадала вместе с соединением, она не
// мешала бы ровно тому, против кого заведена.
func TestDelayIsNotSkippedWhenClientHangsUp(t *testing.T) {
	s, _ := newService(t, newMemoryStorage(t))

	const addr = "127.0.0.1:5000"
	s.fail(t, addr, freeFailures)

	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan time.Duration, 1)

	go func() {
		started := time.Now()
		_, _ = s.Login(ctx, addr, "admin", "неверный")
		returned <- time.Since(started)
	}()

	// Отмена ждёт, пока проверка пароля начнётся: отменённый до неё запрос
	// обрывается сразу и задержки не заслуживает.
	time.Sleep(50 * time.Millisecond)
	cancel()

	if elapsed := <-returned; elapsed < minDelay {
		t.Errorf("ответ через %v, ожидалась задержка не меньше %v", elapsed, minDelay)
	}
}

// TestDelayDoesNotHoldVerifySlot — задержка не держит слот проверки пароля.
//
// Проверка пароля — единственное, что защищает память от потока попыток, и слотов
// всего два. Если бы задержка держала слот, перебор, заняв оба слота задержками,
// закрыл бы вход самому администратору.
//
// Слот в тесте один: только так видно, занят ли он задержкой. Сама задержка взята
// подлиннее самой проверки — иначе мерка не отличила бы их друг от друга.
func TestDelayDoesNotHoldVerifySlot(t *testing.T) {
	s, _ := newService(t, newMemoryStorage(t))
	s.verifications = make(chan struct{}, 1)

	const delayed = "127.0.0.1:5000"
	wait := delay(freeFailures + 4)
	s.fail(t, delayed, freeFailures+4)

	go func() {
		_, _ = s.Login(context.Background(), delayed, "admin", "неверный")
	}()

	// Даём неудачной попытке дойти до задержки.
	time.Sleep(20 * time.Millisecond)

	started := time.Now()
	if _, err := s.Login(context.Background(), "127.0.0.2:5000", "admin", "пароль"); err != nil {
		t.Fatalf("Login() = %v", err)
	}

	if elapsed := time.Since(started); elapsed >= wait/2 {
		t.Errorf("удачный вход занял %v: слот проверки держит задержка", elapsed)
	}
}

// TestSuccessfulLoginForgetsFailures — удачный вход обнуляет счёт: задержка не
// должна висеть на администраторе после того, как он вспомнил пароль.
func TestSuccessfulLoginForgetsFailures(t *testing.T) {
	s, _ := newService(t, newMemoryStorage(t))

	const addr = "127.0.0.1:5000"
	for i := 0; i < 3; i++ {
		if _, err := s.Login(context.Background(), addr, "admin", "неверный"); !errors.Is(err, ErrDenied) {
			t.Fatalf("Login() = %v, ожидался отказ", err)
		}
	}

	if _, err := s.Login(context.Background(), addr, "admin", "пароль"); err != nil {
		t.Fatalf("Login() = %v", err)
	}

	if got := s.failuresOf(addr); got != 0 {
		t.Errorf("после удачного входа осталось %d неудач", got)
	}
}

// TestFailuresAreForgotten — редкие опечатки не копятся месяцами и не задерживают
// администратора, когда он вернулся к панели через неделю.
func TestFailuresAreForgotten(t *testing.T) {
	s, clk := newService(t, newMemoryStorage(t))

	const addr = "127.0.0.1:5000"

	s.remember(addr)
	s.remember(addr)
	if got := s.failuresOf(addr); got != 2 {
		t.Fatalf("неудач %d, ожидалось 2", got)
	}

	clk.advance(failureForget + time.Minute)
	s.remember(addr)

	if got := s.failuresOf(addr); got != 1 {
		t.Errorf("неудач %d, ожидалась 1: счёт должен был начаться заново", got)
	}
}

// TestValidCSRF — чужой сайт cookie пришлёт, а подставить верный заголовок не
// сможет.
func TestValidCSRF(t *testing.T) {
	session := Session{CSRF: "токен"}

	if !session.ValidCSRF("токен") {
		t.Error("свой csrf-токен не подошёл")
	}

	for _, token := range []string{"", "другой", "токе"} {
		if session.ValidCSRF(token) {
			t.Errorf("csrf-токен %q подошёл", token)
		}
	}
}

// TestEnsureCredentialSeedsDefault — панель при первом запуске заводит себе
// учётные данные по умолчанию и говорит об этом.
func TestEnsureCredentialSeedsDefault(t *testing.T) {
	storage := &memoryStorage{sessions: make(map[string]Session)}
	s, _ := newService(t, storage)

	inUse, err := s.EnsureCredential()
	if err != nil {
		t.Fatalf("EnsureCredential() = %v", err)
	}
	if !inUse {
		t.Error("про пароль по умолчанию не сообщили")
	}

	credential, ok, err := storage.Credential()
	if err != nil {
		t.Fatalf("Credential() = %v", err)
	}
	if !ok || credential.Name != DefaultName {
		t.Fatalf("учётные данные = %q, ожидалось имя %q", credential.Name, DefaultName)
	}

	matched, err := Verify(credential.Hash, DefaultPassword)
	if err != nil {
		t.Fatalf("Verify() = %v", err)
	}
	if !matched {
		t.Error("записанный пароль не совпал с паролем по умолчанию")
	}
}

// TestEnsureCredentialReportsChangedPassword — сменили пароль, и предупреждать
// больше не о чем.
func TestEnsureCredentialReportsChangedPassword(t *testing.T) {
	s, _ := newService(t, newMemoryStorage(t))

	inUse, err := s.EnsureCredential()
	if err != nil {
		t.Fatalf("EnsureCredential() = %v", err)
	}
	if inUse {
		t.Error("свой пароль приняли за пароль по умолчанию")
	}
}

// TestNewPrunesStaleSessions — перезапуск панели не тащит за собой брошенные
// сессии.
func TestNewPrunesStaleSessions(t *testing.T) {
	storage := newMemoryStorage(t)

	now := time.Now()
	stale := Session{Token: "отжившая", Seen: now.Add(-2 * idle), Created: now.Add(-2 * idle)}
	alive := Session{Token: "живая", Seen: now, Created: now}
	storage.sessions[stale.Token] = stale
	storage.sessions[alive.Token] = alive

	if _, err := New(storage, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("New() = %v", err)
	}

	if _, ok := storage.sessions[stale.Token]; ok {
		t.Error("отжившая сессия осталась в хранилище")
	}
	if _, ok := storage.sessions[alive.Token]; !ok {
		t.Error("живую сессию вымели")
	}
}
