// Пакет logging ведёт журнал панели в двух местах сразу: последние строки
// остаются в памяти, чтобы страница логов показывала их без чтения файла, а
// файл получает всё и не даёт журналу съесть диск.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// defaultMaxSize — предел файла журнала, если настройка не задана.
const defaultMaxSize = 10 << 20

// Options — куда и сколько писать.
type Options struct {
	// Path — файл журнала. Пустой — писать только в память.
	Path string

	// MaxSize — предел файла в байтах; при превышении файл уходит в <Path>.1.
	MaxSize int64

	// Lines — сколько последних строк держать в памяти.
	Lines int
}

// Log — открытый журнал.
type Log struct {
	logger *slog.Logger
	ring   *ring
	file   *rotatingFile
}

// Open открывает журнал и готовит оба приёмника.
func Open(opts Options) (*Log, error) {
	if opts.Lines <= 0 {
		return nil, fmt.Errorf("журнал: ёмкость памяти %d, нужна положительная", opts.Lines)
	}
	if opts.MaxSize <= 0 {
		opts.MaxSize = defaultMaxSize
	}

	memory := newRing(opts.Lines)
	writers := []io.Writer{memory}

	var file *rotatingFile
	if opts.Path != "" {
		f, err := openRotatingFile(opts.Path, opts.MaxSize)
		if err != nil {
			return nil, err
		}
		file = f
		writers = append(writers, file)
	}

	handler := slog.NewTextHandler(io.MultiWriter(writers...), &slog.HandlerOptions{Level: slog.LevelInfo})

	return &Log{logger: slog.New(handler), ring: memory, file: file}, nil
}

// Logger возвращает логгер, которым пишет панель.
func (l *Log) Logger() *slog.Logger {
	return l.logger
}

// Lines возвращает последние строки из памяти, самые старые — первыми.
func (l *Log) Lines() []string {
	return l.ring.Lines()
}

// Close дописывает файл на диск и закрывает его.
func (l *Log) Close() error {
	if l.file == nil {
		return nil
	}

	return l.file.Close()
}

// ring — кольцевой буфер строк фиксированной ёмкости.
type ring struct {
	mu    sync.Mutex
	lines []string
	next  int
	full  bool
}

func newRing(capacity int) *ring {
	return &ring{lines: make([]string, capacity)}
}

// Write разбирает поток на строки: slog пишет по записи за вызов, но буфер не
// должен зависеть от этого.
func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, line := range strings.Split(strings.TrimSuffix(string(p), "\n"), "\n") {
		if line == "" {
			continue
		}

		r.lines[r.next] = line
		r.next = (r.next + 1) % len(r.lines)
		if r.next == 0 {
			r.full = true
		}
	}

	return len(p), nil
}

// Lines отдаёт копию: вызывающий не должен менять буфер под рукой писателя.
func (r *ring) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.full {
		return append([]string(nil), r.lines[:r.next]...)
	}

	out := make([]string, 0, len(r.lines))
	out = append(out, r.lines[r.next:]...)
	out = append(out, r.lines[:r.next]...)

	return out
}
