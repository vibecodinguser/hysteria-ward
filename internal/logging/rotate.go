package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// rotatingFile — файл журнала с жёстким пределом размера. При превышении
// содержимое уходит в <path>.1, а запись продолжается в новый файл: журнал не
// растёт бесконечно и вместе с ним не растёт диск VPS.
type rotatingFile struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	file    *os.File
	size    int64
}

func openRotatingFile(path string, maxSize int64) (*rotatingFile, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("каталог журнала %q: %w", dir, err)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o640)
	if err != nil {
		return nil, fmt.Errorf("файл журнала %q: %w", path, err)
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("размер журнала %q: %w", path, err)
	}

	return &rotatingFile{path: path, maxSize: maxSize, file: f, size: info.Size()}, nil
}

func (f *rotatingFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Одна запись всегда пишется целиком: обрезать строку хуже, чем превысить
	// предел на её длину.
	if f.size > 0 && f.size+int64(len(p)) > f.maxSize {
		if err := f.rotate(); err != nil {
			return 0, err
		}
	}

	n, err := f.file.Write(p)
	f.size += int64(n)
	if err != nil {
		return n, fmt.Errorf("запись журнала %q: %w", f.path, err)
	}

	return n, nil
}

func (f *rotatingFile) rotate() error {
	if err := f.file.Close(); err != nil {
		return fmt.Errorf("ротация журнала %q: %w", f.path, err)
	}

	if err := os.Rename(f.path, f.path+".1"); err != nil {
		return fmt.Errorf("ротация журнала %q: %w", f.path, err)
	}

	file, err := os.OpenFile(f.path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return fmt.Errorf("новый журнал %q: %w", f.path, err)
	}

	f.file = file
	f.size = 0

	return nil
}

func (f *rotatingFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.file.Sync(); err != nil {
		_ = f.file.Close()
		return fmt.Errorf("сброс журнала %q: %w", f.path, err)
	}

	if err := f.file.Close(); err != nil {
		return fmt.Errorf("закрытие журнала %q: %w", f.path, err)
	}

	return nil
}
