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

// fakeCore — подмена ядра Hysteria 2: тесту не нужен настоящий сокет и
// сертификат, чтобы проверить жизненный цикл панели.
type fakeCore struct {
	done chan error

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
	f.finishLocked(nil)

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
	err     error
}

func (f *fakeFactory) start(cfg config.Config) (daemon.Core, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.err != nil {
		return nil, f.err
	}

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

// panel — запущенная панель с поддельным ядром и временными путями.
type panel struct {
	daemon  *daemon.Daemon
	fake    *fakeFactory
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
		dir:     dir,
		dbPath:  filepath.Join(dir, "hysteria-ward.db"),
		logPath: filepath.Join(dir, "panel.log"),
	}

	// Конфигурация ложится в базу до запуска: панель применяет её при старте.
	seed, err := store.Open(p.dbPath)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	if err := seed.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig() = %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	d, err := daemon.New(daemon.Options{
		DBPath:    p.dbPath,
		Log:       logging.Options{Path: p.logPath, MaxSize: 1 << 20, Lines: 100},
		StartCore: p.fake.start,
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

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p.fake.calls() > 0 {
			return
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatal("ядро так и не запустилось")
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
	p := startPanel(t, config.Default())
	p.waitForCore(t)

	p.cancel()
	if err := p.wait(t); err != nil {
		t.Fatalf("Run() = %v, ожидалось штатное завершение", err)
	}

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
	p := startPanel(t, config.Default())
	p.waitForCore(t)

	p.fake.lastCore().crash(errors.New("сокет отвалился"))

	waitFor(t, func() bool { return !p.daemon.CoreRunning() })

	select {
	case err := <-p.done:
		t.Fatalf("Run завершился при падении ядра: %v", err)
	default:
	}

	p.cancel()
	if err := p.wait(t); err != nil {
		t.Fatalf("Run() = %v после падения ядра, ожидалось штатное завершение", err)
	}

	log := p.log(t)
	if !strings.Contains(log, "ядро упало") || !strings.Contains(log, "сокет отвалился") {
		t.Errorf("падение ядра не попало в журнал: %q", log)
	}
}

// TestCoreStartErrorKeepsPanelAlive — ядро не поднялось, но панель работает:
// иначе администратор не смог бы открыть интерфейс и починить конфигурацию.
func TestCoreStartErrorKeepsPanelAlive(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "panel.log")

	d, err := daemon.New(daemon.Options{
		DBPath: filepath.Join(dir, "hysteria-ward.db"),
		Log:    logging.Options{Path: logPath, MaxSize: 1 << 20, Lines: 100},
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

	waitFor(t, func() bool {
		content, readErr := os.ReadFile(logPath)
		return readErr == nil && strings.Contains(string(content), "ядро не запустилось")
	})

	select {
	case err := <-done:
		t.Fatalf("Run завершился с ошибкой запуска ядра: %v", err)
	default:
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
	p := startPanel(t, config.Default())
	p.waitForCore(t)

	first := p.fake.lastCore()

	updated := config.Default()
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

	p.cancel()
	if err := p.wait(t); err != nil {
		t.Fatalf("Run() = %v, ожидалось штатное завершение", err)
	}
}

// TestApplyRejectsInvalidConfig — негодная правка не уносит с собой работающий
// сервер: конфигурация проверяется до остановки ядра.
func TestApplyRejectsInvalidConfig(t *testing.T) {
	p := startPanel(t, config.Default())
	p.waitForCore(t)

	broken := config.Default()
	broken.Masq = config.Masquerade{Mode: "какой-то"}

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

	p.cancel()
	if err := p.wait(t); err != nil {
		t.Fatalf("Run() = %v, ожидалось штатное завершение", err)
	}
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
