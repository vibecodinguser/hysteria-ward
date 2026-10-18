package daemon_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/config"
	"github.com/vibecodinguser/hysteria-ward/internal/daemon"
	"github.com/vibecodinguser/hysteria-ward/internal/logging"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// runnable — конфигурация, годная к запуску. Файлов по этим путям нет: поддельное
// ядро их не читает.
func runnable() config.Config {
	cfg := config.Default()
	cfg.TLS = config.TLS{
		Cert: "/etc/hysteria-ward/certs/panel.crt",
		Key:  "/etc/hysteria-ward/certs/panel.key",
	}

	return cfg
}

// fakeCore — подмена ядра Hysteria 2: тесту не нужен настоящий сокет и
// сертификат, чтобы проверить жизненный цикл панели.
type fakeCore struct {
	done chan error

	// crashOnStop заставляет ядро «упасть» ровно в момент остановки: так
	// проверяется, что запоздавшее падение не сбивает панель с толку.
	crashOnStop error

	mu         sync.Mutex
	stopCalled bool
	finished   bool
}

func newFakeCore() *fakeCore {
	return &fakeCore{done: make(chan error, 1)}
}

// Stop повторяет поведение настоящего ядра: штатная остановка завершает Done без
// ошибки.
func (f *fakeCore) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.stopCalled = true
	f.finishLocked(f.crashOnStop)

	return nil
}

// crash — авария ядра: ошибка уходит в канал завершения.
func (f *fakeCore) crash(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.finishLocked(err)
}

func (f *fakeCore) Done() <-chan error {
	return f.done
}

func (f *fakeCore) isStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.stopCalled
}

// finishLocked завершает канал ровно один раз: настоящее ядро тоже закрывает его
// единожды.
func (f *fakeCore) finishLocked(err error) {
	if f.finished {
		return
	}
	f.finished = true

	if err != nil {
		f.done <- err
	}
	close(f.done)
}

// fakeFactory поднимает поддельные ядра и запоминает, с какой конфигурацией их
// поднимали: так видно, что именно попало в ядро.
type fakeFactory struct {
	mu      sync.Mutex
	configs []config.Config
	cores   []*fakeCore
}

func (f *fakeFactory) start(cfg config.Config) (daemon.Core, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	core := newFakeCore()
	f.configs = append(f.configs, cfg)
	f.cores = append(f.cores, core)

	return core, nil
}

func (f *fakeFactory) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.cores)
}

func (f *fakeFactory) lastCore() *fakeCore {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.cores[len(f.cores)-1]
}

func (f *fakeFactory) lastConfig() config.Config {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.configs[len(f.configs)-1]
}

// certCheck — предполётная проверка в тесте: отвергает конфигурацию, у которой
// сертификат помечен как нечитаемый.
type certCheck struct {
	mu      sync.Mutex
	badCert string
}

func (c *certCheck) check(cfg config.Config) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.badCert != "" && cfg.TLS.Cert == c.badCert {
		return errors.New("сертификат не читается")
	}

	return nil
}

func (c *certCheck) reject(cert string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.badCert = cert
}

// panel — запущенная панель с поддельным ядром и временными путями.
type panel struct {
	daemon  *daemon.Daemon
	fake    *fakeFactory
	check   *certCheck
	dir     string
	dbPath  string
	logPath string
	cancel  context.CancelFunc
	done    <-chan error

	closed bool
}

// startPanel готовит базу с готовой конфигурацией, поднимает панель и возвращает
// её тесту.
func startPanel(t *testing.T, cfg config.Config) *panel {
	t.Helper()

	dir := t.TempDir()
	p := &panel{
		fake:    &fakeFactory{},
		check:   &certCheck{},
		dir:     dir,
		dbPath:  filepath.Join(dir, "hysteria-ward.db"),
		logPath: filepath.Join(dir, "panel.log"),
	}

	// Конфигурация ложится в базу до запуска: панель применяет её при старте.
	seedDB(t, p.dbPath, cfg)

	d, err := daemon.New(daemon.Options{
		DBPath:    p.dbPath,
		Log:       logging.Options{Path: p.logPath},
		StartCore: p.fake.start,
		Preflight: p.check.check,
	})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	p.daemon = d
	t.Cleanup(func() { p.shutdown(t) })

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	t.Cleanup(cancel)

	errCh := make(chan error, 1)
	p.done = errCh
	go func() { errCh <- d.Run(ctx) }()

	return p
}

// seedDB кладёт конфигурацию в базу до запуска панели — так же, как это сделает
// установщик на чистом сервере.
func seedDB(t *testing.T, path string, cfg config.Config) {
	t.Helper()

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer s.Close()

	if err := s.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig() = %v", err)
	}
}

// shutdown закрывает базу и журнал панели. Повторный вызов ничего не делает:
// тест проверяет базу после остановки, а уборка идёт следом.
func (p *panel) shutdown(t *testing.T) {
	t.Helper()

	if p.closed {
		return
	}
	p.closed = true

	if err := p.daemon.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}
}

// waitForCore ждёт, пока панель поднимет ядро.
func (p *panel) waitForCore(t *testing.T) {
	t.Helper()

	waitFor(t, func() bool { return p.fake.calls() > 0 })
}

// waitForLog ждёт, пока в журнале появится подстрока.
func (p *panel) waitForLog(t *testing.T, want string) {
	t.Helper()

	waitFor(t, func() bool { return strings.Contains(p.log(t), want) })
}

// wait возвращает код завершения панели или роняет тест по таймауту.
func (p *panel) wait(t *testing.T) error {
	t.Helper()

	select {
	case err := <-p.done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("панель не завершилась")
		return nil
	}
}

// stop гасит панель и проверяет, что она завершилась штатно.
func (p *panel) stop(t *testing.T) {
	t.Helper()

	p.cancel()

	if err := p.wait(t); err != nil {
		t.Fatalf("Run() = %v, ожидалось штатное завершение", err)
	}
}

// log читает журнал панели.
func (p *panel) log(t *testing.T) string {
	t.Helper()

	content, err := os.ReadFile(p.logPath)
	if err != nil {
		t.Fatalf("журнал не прочитан: %v", err)
	}

	return string(content)
}

// TestRunStopsOnContextCancel — штатное завершение: отмена контекста (в бою —
// SIGTERM) останавливает ядро, Run возвращает nil, а лог доходит до диска.
func TestRunStopsOnContextCancel(t *testing.T) {
	p := startPanel(t, runnable())
	p.waitForCore(t)

	p.stop(t)

	if !p.fake.lastCore().isStopped() {
		t.Error("ядро не остановлено")
	}
	if p.daemon.CoreRunning() {
		t.Error("ядро считается работающим после остановки")
	}
	if log := p.log(t); !strings.Contains(log, "панель остановлена") {
		t.Errorf("в журнале нет записи об остановке: %q", log)
	}

	// База осталась целой и открывается заново.
	p.shutdown(t)

	opened, err := store.Open(p.dbPath)
	if err != nil {
		t.Fatalf("база не открылась после остановки: %v", err)
	}
	defer opened.Close()

	if _, err := opened.Config(); err != nil {
		t.Fatalf("конфигурация не читается: %v", err)
	}
}

// TestCrashKeepsPanelAlive — падение ядра попадает в журнал, но панель не
// выходит: она продолжает работать до сигнала остановки.
func TestCrashKeepsPanelAlive(t *testing.T) {
	p := startPanel(t, runnable())
	p.waitForCore(t)

	p.fake.lastCore().crash(errors.New("сокет отвалился"))

	waitFor(t, func() bool { return !p.daemon.CoreRunning() })

	select {
	case err := <-p.done:
		t.Fatalf("Run завершился при падении ядра: %v", err)
	default:
	}

	p.stop(t)

	log := p.log(t)
	if !strings.Contains(log, "ядро упало") || !strings.Contains(log, "сокет отвалился") {
		t.Errorf("падение ядра не попало в журнал: %q", log)
	}
}

// TestCoreStartErrorKeepsPanelAlive — ядро не поднялось, но панель работает:
// иначе администратор не смог бы открыть интерфейс и починить конфигурацию.
func TestCoreStartErrorKeepsPanelAlive(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "hysteria-ward.db")
	logPath := filepath.Join(dir, "panel.log")

	seedDB(t, dbPath, runnable())

	d, err := daemon.New(daemon.Options{
		DBPath: dbPath,
		Log:    logging.Options{Path: logPath},
		StartCore: func(config.Config) (daemon.Core, error) {
			return nil, errors.New("порт занят")
		},
	})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	defer d.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	waitForLogFile(t, logPath, "конфигурация не применена")

	select {
	case err := <-done:
		t.Fatalf("Run завершился с ошибкой запуска ядра: %v", err)
	default:
	}

	if d.CoreRunning() {
		t.Error("ядро считается работающим, хотя не поднялось")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() = %v, ожидалось штатное завершение", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("панель не завершилась после отмены контекста")
	}

	if content, readErr := os.ReadFile(logPath); readErr != nil || !strings.Contains(string(content), "порт занят") {
		t.Errorf("причина не попала в журнал: %q (%v)", content, readErr)
	}
}

// TestApplyRestartsCoreWithNewConfig — правка ложится в базу, но в силу входит
// только по «Применить»: до этого ядро продолжает работать по прежней
// конфигурации.
func TestApplyRestartsCoreWithNewConfig(t *testing.T) {
	p := startPanel(t, runnable())
	p.waitForCore(t)

	first := p.fake.lastCore()

	updated := runnable()
	updated.Obfs = config.Obfs{Password: "salamander"}
	if err := p.daemon.Store().SaveConfig(updated); err != nil {
		t.Fatalf("SaveConfig() = %v", err)
	}

	if calls := p.fake.calls(); calls != 1 {
		t.Fatalf("ядро перезапустилось само: запусков %d", calls)
	}

	if err := p.daemon.Apply(); err != nil {
		t.Fatalf("Apply() = %v", err)
	}

	if calls := p.fake.calls(); calls != 2 {
		t.Fatalf("после Apply запусков %d, ожидалось 2", calls)
	}
	if got := p.fake.lastConfig().Obfs.Password; got != "salamander" {
		t.Errorf("ядро поднято с obfs-паролем %q, ожидался %q", got, "salamander")
	}
	if !first.isStopped() {
		t.Error("прежнее ядро не остановлено")
	}
	if !p.daemon.CoreRunning() {
		t.Error("после Apply ядро не считается работающим")
	}

	p.stop(t)
}

// TestLateFailureOfPreviousCore — панель узнаёт о падении прежнего ядра уже после
// того, как подняла новое. Такая весть не должна погасить работающее ядро.
func TestLateFailureOfPreviousCore(t *testing.T) {
	p := startPanel(t, runnable())
	p.waitForCore(t)

	// Прежнее ядро «падает» ровно в момент остановки: его Done приходит уже тогда,
	// когда панель поднимает новое.
	p.fake.lastCore().crashOnStop = errors.New("сокет отвалился при остановке")

	if err := p.daemon.Apply(); err != nil {
		t.Fatalf("Apply() = %v", err)
	}

	p.waitForLog(t, "ядро упало")

	if !p.daemon.CoreRunning() {
		t.Error("запоздавшее падение прежнего ядра сбросило новое")
	}

	p.stop(t)
}

// TestApplyRejectsInvalidConfig — негодная правка не уносит с собой работающий
// сервер: конфигурация проверяется до остановки ядра.
func TestApplyRejectsInvalidConfig(t *testing.T) {
	p := startPanel(t, runnable())
	p.waitForCore(t)

	broken := runnable()
	broken.Masquerade = config.Masquerade{Mode: "какой-то"}

	if err := p.daemon.Store().SaveConfig(broken); err != nil {
		t.Fatalf("SaveConfig() = %v", err)
	}

	if err := p.daemon.Apply(); err == nil {
		t.Fatal("Apply() с негодной конфигурацией прошёл, ожидалась ошибка")
	}

	if calls := p.fake.calls(); calls != 1 {
		t.Errorf("ядро поднималось %d раз, ожидался только первоначальный запуск", calls)
	}
	if !p.daemon.CoreRunning() {
		t.Error("работающее ядро остановлено из-за негодной правки")
	}

	p.stop(t)
}

// TestApplyKeepsCoreWhenPreflightFails — ошибка, которую видно только при запуске
// (нечитаемый сертификат), тоже не должна уносить работающий сервер.
func TestApplyKeepsCoreWhenPreflightFails(t *testing.T) {
	p := startPanel(t, runnable())
	p.waitForCore(t)

	unreadable := runnable()
	unreadable.TLS.Cert = "/нет/такого/сертификата.pem"
	if err := p.daemon.Store().SaveConfig(unreadable); err != nil {
		t.Fatalf("SaveConfig() = %v", err)
	}
	p.check.reject(unreadable.TLS.Cert)

	if err := p.daemon.Apply(); err == nil {
		t.Fatal("Apply() с нечитаемым сертификатом прошёл, ожидалась ошибка")
	}

	if calls := p.fake.calls(); calls != 1 {
		t.Errorf("ядро поднималось %d раз, ожидался только первоначальный запуск", calls)
	}
	if !p.daemon.CoreRunning() {
		t.Error("работающее ядро остановлено из-за нечитаемого сертификата")
	}

	p.stop(t)
}

// waitFor ждёт выполнения условия и роняет тест по таймауту.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatal("условие так и не выполнилось")
}

// waitForLogFile ждёт появления подстроки в журнале по известному пути.
func waitForLogFile(t *testing.T, path, want string) {
	t.Helper()

	waitFor(t, func() bool {
		content, err := os.ReadFile(path)
		return err == nil && strings.Contains(string(content), want)
	})
}
