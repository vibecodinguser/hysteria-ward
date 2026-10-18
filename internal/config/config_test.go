package config_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/config"
)

// TestYAMLRoundTrip — экспорт и импорт не теряют ни одного поля. YAML нужен,
// чтобы перенести настройки на другую панель, а не чтобы их растерять.
func TestYAMLRoundTrip(t *testing.T) {
	original := config.Config{
		Listen: ":8443",
		TLS: config.TLS{
			Cert: "/etc/hysteria-ward/certs/panel.crt",
			Key:  "/etc/hysteria-ward/certs/panel.key",
		},
		Obfs: config.Obfs{Password: "salamander"},
		Masq: config.Masquerade{Mode: config.MasqProxy, Proxy: "https://example.com"},
	}

	data, err := original.YAML()
	if err != nil {
		t.Fatalf("YAML() = %v", err)
	}

	restored, err := config.ParseYAML(data)
	if err != nil {
		t.Fatalf("ParseYAML() = %v", err)
	}

	if !reflect.DeepEqual(original, restored) {
		t.Errorf("после круга получилось %+v, было %+v", restored, original)
	}
}

// TestParseYAMLRejectsUnknownField — опечатка в поле при импорте должна быть
// ошибкой. Иначе настройка молча пропадёт, а панель будет считать, что всё
// применила.
func TestParseYAMLRejectsUnknownField(t *testing.T) {
	_, err := config.ParseYAML([]byte("listen: \":443\"\nobfsPasswrd: salamander\n"))
	if err == nil {
		t.Fatal("ParseYAML() с неизвестным полем прошёл, ожидалась ошибка")
	}
	if !strings.Contains(err.Error(), "obfsPasswrd") {
		t.Errorf("ошибка не называет поле: %v", err)
	}
}

// TestDefault — у конфига есть осмысленное начальное состояние: порт 443,
// masquerade выключен, обфускации нет.
func TestDefault(t *testing.T) {
	def := config.Default()

	if def.Listen != ":443" {
		t.Errorf("Listen = %q, ожидалось \":443\"", def.Listen)
	}
	if def.Masq.Mode != config.MasqOff {
		t.Errorf("Masquerade.Mode = %q, ожидался %q", def.Masq.Mode, config.MasqOff)
	}
	if err := def.Validate(); err != nil {
		t.Errorf("Validate() = %v, конфиг по умолчанию должен быть годным", err)
	}
}

// TestValidate — конфиг с непонятным режимом masquerade или без адреса ядро не
// поднимет, и об этом лучше сказать до применения.
func TestValidate(t *testing.T) {
	cases := map[string]config.Config{
		"пустой адрес":      {Masq: config.Masquerade{Mode: config.MasqOff}},
		"неизвестный режим": {Listen: ":443", Masq: config.Masquerade{Mode: "какой-то"}},
		"file без пути":     {Listen: ":443", Masq: config.Masquerade{Mode: config.MasqFile}},
		"proxy без адреса":  {Listen: ":443", Masq: config.Masquerade{Mode: config.MasqProxy}},
	}

	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if err := cfg.Validate(); err == nil {
				t.Errorf("Validate() = nil, ожидалась ошибка")
			}
		})
	}
}
