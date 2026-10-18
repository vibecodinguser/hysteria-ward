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
	name string
	hash string
	ok   bool

	sessions map[string]Session
}

func newMemoryStorage(t *testing.T) *memoryStorage {
	t.Helper()

	hash, err := Hash("пароль")
	if err != nil {
		t.Fatalf("Hash() = %v", err)
	}

	return &memoryStorage{name: "admin", hash: hash, ok: true, sessions: make(map[string]Session)}
}

func (m *memoryStorage) Credential() (string, string, bool, error) {
	return m.name, m.hash, m.ok, nil
}

func (m *memoryStorage) SaveCredential(name, hash string) error {
	m.name, m.hash, m.ok = name, hash, true

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

// clock — часы теста. Сроки жизни сессий и счёт неудач проверяются сдвигом
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

// TestLoginAndLogout — полный круг: вход заводит сессию, сессия узнаётся, выход её
// закрывает.
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

// TestCheckForgetsStaleSession — сессия, которой не пользовались дольше срока,
// закрывается и выметается сразу, а не хранится вечно.
func TestCheckForgetsStaleSession(t *testing.T) {
	storage := newMemoryStorage(t)
	s, clk := newService(t, storage)

	session, err := s.Login(context.Background(), "127.0.0.1:5000", "admin", "пароль")
	if err != nil {
		t.Fatalf("Login() = %v", err)
	}

	clk.advance(s.idle + time.Second)

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

	clk.advance(s.renewEvery / 2)
	if _, err := s.Check(session.Token); err != nil {
		t.Fatalf("Check() = %v", err)
	}

	if got := storage.sessions[session.Token].Seen; !got.Equal(session.Seen) {
		t.Error("сессия продлена раньше, чем нужно")
	}

	clk.advance(s.renewEvery)
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
	s, _ := newService(t, newMemoryStorage(t))
	s.freeFailures = 2
	s.minDelay = 2 * time.Millisecond
	s.maxDelay = 5 * time.Millisecond

	for _, c := range []struct {
		failures int
		want     time.Duration
	}{
		{0, 0},
		{1, 0},
		{2, 0},
		{3, 2 * time.Millisecond},
		{4, 4 * time.Millisecond},
		{5, 5 * time.Millisecond},
		{50, 5 * time.Millisecond},
	} {
		if got := s.delay(c.failures); got != c.want {
			t.Errorf("delay(%d) = %v, ожидалось %v", c.failures, got, c.want)
		}
	}
}

// TestLoginWaitsAfterFailures — задержка не просто считается, а откладывает ответ.
func TestLoginWaitsAfterFailures(t *testing.T) {
	s, _ := newService(t, newMemoryStorage(t))
	s.freeFailures = 1
	s.minDelay = 300 * time.Millisecond
	s.maxDelay = time.Second

	ctx := context.Background()
	const addr = "127.0.0.1:5000"

	// Первая неудача прощается и не задерживается.
	if _, err := s.Login(ctx, addr, "admin", "неверный"); !errors.Is(err, ErrDenied) {
		t.Fatalf("Login() = %v, ожидался отказ", err)
	}

	started := time.Now()
	if _, err := s.Login(ctx, addr, "admin", "неверный"); !errors.Is(err, ErrDenied) {
		t.Fatalf("Login() = %v, ожидался отказ", err)
	}

	if elapsed := time.Since(started); elapsed < s.minDelay {
		t.Errorf("ответ пришёл через %v, ожидалась задержка не меньше %v", elapsed, s.minDelay)
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
	s.failureForget = time.Minute

	const addr = "127.0.0.1:5000"

	s.remember(addr)
	s.remember(addr)
	if got := s.failuresOf(addr); got != 2 {
		t.Fatalf("неудач %d, ожидалось 2", got)
	}

	clk.advance(2 * time.Minute)
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

	name, hash, ok, err := storage.Credential()
	if err != nil {
		t.Fatalf("Credential() = %v", err)
	}
	if !ok || name != DefaultName {
		t.Fatalf("учётные данные = %q, ожидалось имя %q", name, DefaultName)
	}

	matched, err := Verify(hash, DefaultPassword)
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
