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

	"github.com/vibecodinguser/hysteria-ward/internal/daemon"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// fakeCore — подмена ядра Hysteria 2: тесту не нужен настоящий сокет и
// сертификат, чтобы проверить жизненный цикл панели.
type fakeCore struct {
	done    chan error
	started chan struct{}

	mu      sync.Mutex
	stopped bool
}

func newFakeCore() *fakeCore {
	return &fakeCore{done: make(chan error, 1), started: make(chan struct{})}
}

func (f *fakeCore) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.stopped = true

	return nil
}

func (f *fakeCore) Done() <-chan error {
	return f.done
}

func (f *fakeCore) isStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.stopped
}

// panel — запущенная панель с подменённым ядром и временными путями.
type panel struct {
	core    *fakeCore
	dbPath  string
	logPath string
	cancel  context.CancelFunc
	done    <-chan error
}

// startPanel поднимает панель и ждёт, пока ядро запустится.
func startPanel(t *testing.T) *panel {
	t.Helper()

	dir := t.TempDir()
	p := &panel{
		core:    newFakeCore(),
		dbPath:  filepath.Join(dir, "hysteria-ward.db"),
		logPath: filepath.Join(dir, "panel.log"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	t.Cleanup(cancel)

	errCh := make(chan error, 1)
	go func() {
		errCh <- daemon.Run(ctx, daemon.Options{
			DBPath:     p.dbPath,
			LogPath:    p.logPath,
			MaxLogSize: 1 << 20,
			LogLines:   100,
			StartCore: func() (daemon.Core, error) {
				close(p.core.started)
				return p.core, nil
			},
		})
	}()
	p.done = errCh

	select {
	case <-p.core.started:
	case <-time.After(5 * time.Second):
		t.Fatal("ядро так и не запустилось")
	}

	return p
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

// log читает журнал после остановки.
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
	p := startPanel(t)

	p.cancel()
	if err := p.wait(t); err != nil {
		t.Fatalf("Run() = %v, ожидалось штатное завершение", err)
	}

	if !p.core.isStopped() {
		t.Error("ядро не остановлено")
	}
	if log := p.log(t); !strings.Contains(log, "остановлена") {
		t.Errorf("в журнале нет записи об остановке: %q", log)
	}

	// База осталась целой и открывается заново.
	opened, err := store.Open(p.dbPath)
	if err != nil {
		t.Fatalf("база не открылась после остановки: %v", err)
	}
	defer opened.Close()

	if _, err := opened.SchemaVersion(); err != nil {
		t.Fatalf("версия схемы не читается: %v", err)
	}
}

// TestCrashKeepsPanelAlive — падение ядра попадает в журнал, но панель не
// выходит: она продолжает работать до сигнала остановки.
func TestCrashKeepsPanelAlive(t *testing.T) {
	p := startPanel(t)

	p.core.done <- errors.New("сокет отвалился")

	select {
	case err := <-p.done:
		t.Fatalf("Run завершился при падении ядра: %v", err)
	case <-time.After(200 * time.Millisecond):
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

// TestCoreStartErrorIsReported — если ядро не поднялось, панель не притворяется
// работающей: ошибка возвращается, а причина остаётся в журнале.
func TestCoreStartErrorIsReported(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "panel.log")

	err := daemon.Run(context.Background(), daemon.Options{
		DBPath:     filepath.Join(dir, "hysteria-ward.db"),
		LogPath:    logPath,
		MaxLogSize: 1 << 20,
		LogLines:   100,
		StartCore: func() (daemon.Core, error) {
			return nil, errors.New("порт занят")
		},
	})
	if err == nil {
		t.Fatal("Run() = nil, ожидалась ошибка запуска ядра")
	}

	content, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatalf("журнал не прочитан: %v", readErr)
	}
	if !strings.Contains(string(content), "порт занят") {
		t.Errorf("причина в журнале отсутствует: %q", content)
	}
}
