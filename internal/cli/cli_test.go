package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/build"
	"github.com/vibecodinguser/hysteria-ward/internal/cli"
)

// TestRunVersion проверяет, что --version печатает метаданные сборки
// и завершается успешно.
//
// Сам формат строки зафиксирован литералами в тестах пакета build;
// здесь проверяется проводка: точка входа печатает именно его.
func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := cli.Run([]string{"--version"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("Run(--version) вернул код %d, ожидался 0; stderr: %q", code, stderr.String())
	}

	want := build.Current().String() + "\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout = %q, ожидалось %q", got, want)
	}

	if strings.Count(stdout.String(), "\n") != 1 {
		t.Errorf("ожидалась ровно одна строка, получено %q", stdout.String())
	}
}

// TestRunHelp проверяет, что --help печатает справку в stdout и завершается успешно.
func TestRunHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := cli.Run([]string{"--help"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("Run(--help) вернул код %d, ожидался 0; stderr: %q", code, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "Использование:") || !strings.Contains(out, build.Name) {
		t.Errorf("справка не похожа на справку: %q", out)
	}
	if stderr.Len() != 0 {
		t.Errorf("справка ушла в stderr: %q", stderr.String())
	}
}

// TestRunUnknownArgument проверяет, что неизвестный аргумент — это ошибка,
// а не молчаливое согласие: сообщение уходит в stderr и указывает на --help.
func TestRunUnknownArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := cli.Run([]string{"--такого-нет"}, &stdout, &stderr)

	if code == 0 {
		t.Errorf("Run(неизвестный аргумент) вернул 0, ожидался ненулевой код")
	}
	if !strings.Contains(stderr.String(), "--такого-нет") {
		t.Errorf("в stderr нет имени неизвестного аргумента: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "--help") {
		t.Errorf("в stderr нет подсказки про --help: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("в stdout что-то попало: %q", stdout.String())
	}
}

// TestRunWithoutArguments проверяет поведение без аргументов. Сейчас запускать
// нечего, поэтому это справка и ненулевой код; с появлением жизненного цикла
// панели поведение изменится на запуск (задача про жизненный цикл).
func TestRunWithoutArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := cli.Run(nil, &stdout, &stderr)

	if code == 0 {
		t.Errorf("Run() без аргументов вернул 0, ожидался ненулевой код")
	}
	if !strings.Contains(stderr.String(), "Использование:") {
		t.Errorf("в stderr нет справки: %q", stderr.String())
	}
}
