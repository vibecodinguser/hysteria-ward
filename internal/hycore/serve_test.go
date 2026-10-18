package hycore

import (
	"errors"
	"strings"
	"testing"
)

// panickingCore — ядро, которое паникует вместо нормальной работы.
type panickingCore struct{}

func (panickingCore) Serve() error { panic("соединение отвалилось") }
func (panickingCore) Close() error { return nil }

// failingCore — ядро, которое завершается обычной ошибкой.
type failingCore struct{ err error }

func (f failingCore) Serve() error { return f.err }
func (f failingCore) Close() error { return nil }

// TestServeOnceRecoversPanic — паника ядра не должна уносить панель: watchdog
// появится позже, а Supervisord над панелью нет вовсе, поэтому необработанная
// паника убила бы весь процесс вместе с интерфейсом.
func TestServeOnceRecoversPanic(t *testing.T) {
	err := serveOnce(panickingCore{})
	if err == nil {
		t.Fatal("паника проглочена, ожидалась ошибка")
	}
	if !strings.Contains(err.Error(), "паника ядра") || !strings.Contains(err.Error(), "соединение отвалилось") {
		t.Errorf("ошибка не описывает панику: %v", err)
	}
}

// TestServeOnceReturnsError — обычная ошибка Serve проходит как есть: панель
// должна видеть настоящую причину, а не обёртку.
func TestServeOnceReturnsError(t *testing.T) {
	want := errors.New("сокет закрыт")

	err := serveOnce(failingCore{err: want})
	if !errors.Is(err, want) {
		t.Errorf("serveOnce() = %v, ожидалось %v", err, want)
	}
}
