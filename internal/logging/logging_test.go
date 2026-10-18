package logging_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/logging"
)

// TestLinesKeepsLast — в памяти остаются последние строки, а не весь журнал:
// страница логов не должна расти вместе с uptime.
func TestLinesKeepsLast(t *testing.T) {
	log, err := logging.Open(logging.Options{Lines: 3})
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer log.Close()

	for _, message := range []string{"first", "second", "third", "fourth"} {
		log.Logger().Info(message)
	}

	lines := log.Lines()
	if len(lines) != 3 {
		t.Fatalf("в памяти %d строк, ожидалось 3: %q", len(lines), lines)
	}

	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "first") {
		t.Errorf("вытесненная строка осталась в памяти: %q", joined)
	}
	for _, want := range []string{"second", "third", "fourth"} {
		if !strings.Contains(joined, want) {
			t.Errorf("в памяти нет строки %q: %q", want, joined)
		}
	}
}

// TestLinesToFile — строки уходят и в файл, причём последняя запись доходит до
// диска после Close.
func TestLinesToFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.log")

	log, err := logging.Open(logging.Options{Path: path, MaxSize: 1 << 20, Lines: 10})
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	log.Logger().Info("последняя запись")

	if err := log.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("файл журнала не прочитан: %v", err)
	}
	if !strings.Contains(string(content), "последняя запись") {
		t.Errorf("запись не дошла до файла: %q", content)
	}
}

// TestFileRotates — файл не растёт бесконечно: при превышении предела он уходит
// в <path>.1, а новый начинается с нуля.
func TestFileRotates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panel.log")

	log, err := logging.Open(logging.Options{Path: path, MaxSize: 256, Lines: 1000})
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	for i := 0; i < 100; i++ {
		log.Logger().Info("строка журнала", "номер", i)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("текущий файл не найден: %v", err)
	}
	if info.Size() > 256 {
		t.Errorf("текущий файл %d байт, предел 256", info.Size())
	}

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("ротация не оставила предыдущий файл: %v", err)
	}
}

// TestOpenRejectsEmptyMemory — нулевая ёмкость памяти делает страницу логов
// бессмысленной: это ошибка настройки, а не молчаливое «ничего не покажем».
func TestOpenRejectsEmptyMemory(t *testing.T) {
	if _, err := logging.Open(logging.Options{Lines: 0}); err == nil {
		t.Fatal("Open() с нулевой ёмкостью прошёл, ожидалась ошибка")
	}
}
