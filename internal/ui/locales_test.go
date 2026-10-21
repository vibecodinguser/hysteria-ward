package ui_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// messagesPath — исходные сообщения интерфейса. Проверка идёт по исходникам, а
// не по собранной сборке: пропущенный перевод должен ломать тесты ещё до того,
// как сборка уедет в релиз.
const messagesPath = "../../web/messages"

// placeholderPattern вылавливает параметры ICU-сообщений вида {name}.
var placeholderPattern = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

// readMessages читает набор сообщений одной локали.
func readMessages(t *testing.T, locale string) map[string]string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(messagesPath, locale+".json"))
	if err != nil {
		t.Fatalf("сообщения %s не прочитаны: %v", locale, err)
	}

	var messages map[string]string
	if err := json.Unmarshal(data, &messages); err != nil {
		t.Fatalf("сообщения %s не разобраны: %v", locale, err)
	}

	return messages
}

// placeholders собирает имена параметров, которые сообщение подставляет.
func placeholders(text string) []string {
	matches := placeholderPattern.FindAllStringSubmatch(text, -1)
	names := make([]string, 0, len(matches))

	for _, match := range matches {
		names = append(names, match[1])
	}

	slices.Sort(names)

	return names
}

// hasCyrillic находит русские буквы: в английском наборе их быть не должно.
func hasCyrillic(text string) bool {
	return strings.ContainsFunc(text, func(r rune) bool {
		return r >= '\u0400' && r <= '\u04FF'
	})
}

// TestTranslationsMatch — у каждой локали один и тот же набор сообщений, ни одно
// не пустое, а параметры подстановки совпадают. Иначе английская страница либо
// упадёт на генерации сообщения, либо молча потеряет имя или дату.
func TestTranslationsMatch(t *testing.T) {
	ru := readMessages(t, "ru")
	en := readMessages(t, "en")

	for key, ruText := range ru {
		enText, ok := en[key]
		if !ok {
			t.Errorf("в en.json нет ключа %q", key)

			continue
		}

		if strings.TrimSpace(enText) == "" {
			t.Errorf("en.json: ключ %q пуст", key)
		}

		if key != "$schema" && hasCyrillic(enText) {
			t.Errorf("en.json: в ключе %q остался русский текст: %q", key, enText)
		}

		ruParams := placeholders(ruText)
		enParams := placeholders(enText)
		if !slices.Equal(ruParams, enParams) {
			t.Errorf("ключ %q: параметры ru %v, en %v", key, ruParams, enParams)
		}
	}

	for key := range en {
		if _, ok := ru[key]; !ok {
			t.Errorf("в ru.json нет ключа %q", key)
		}
	}
}

// TestLocalesConfigured — Paraglide должен собирать обе локали, а русский
// остаётся языком по умолчанию.
func TestLocalesConfigured(t *testing.T) {
	data, err := os.ReadFile("../../web/project.inlang/settings.json")
	if err != nil {
		t.Fatalf("настройки Paraglide не прочитаны: %v", err)
	}

	var settings struct {
		BaseLocale string   `json:"baseLocale"`
		Locales    []string `json:"locales"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("настройки Paraglide не разобраны: %v", err)
	}

	if settings.BaseLocale != "ru" {
		t.Errorf("язык по умолчанию = %q, ожидался ru", settings.BaseLocale)
	}
	for _, locale := range []string{"ru", "en"} {
		if !slices.Contains(settings.Locales, locale) {
			t.Errorf("в настройках Paraglide нет локали %q", locale)
		}
	}
}

// TestBuiltInterfaceHasBothLocales — во вшитой сборке должны быть оба набора.
// Свежая сборка обязательна: интерфейс берётся из dist, а не собирается на
// сервере.
func TestBuiltInterfaceHasBothLocales(t *testing.T) {
	files, err := os.ReadDir("dist/assets")
	if err != nil {
		t.Fatalf("сборка интерфейса не прочитана: %v", err)
	}

	var script strings.Builder
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".js") {
			continue
		}

		data, err := os.ReadFile(filepath.Join("dist/assets", file.Name()))
		if err != nil {
			t.Fatalf("файл сборки %s не прочитан: %v", file.Name(), err)
		}
		script.Write(data)
	}

	for _, phrase := range []string{"Вход в панель", "Sign in to the panel"} {
		if !strings.Contains(script.String(), phrase) {
			t.Errorf("в собранном интерфейсе нет строки %q — запустите make ui", phrase)
		}
	}
}
