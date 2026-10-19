package config_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/config"
)

// valid — годная конфигурация: от неё отличаются случаи в TestValidate, чтобы
// проверялось ровно одно нарушение за раз.
func valid() config.Config {
	return config.Config{
		Listen: ":443",
		TLS: config.TLS{
			Cert: "/etc/hysteria-ward/certs/panel.crt",
			Key:  "/etc/hysteria-ward/certs/panel.key",
		},
		Masquerade: config.Masquerade{Mode: config.MasqueradeOff},
	}
}

// TestYAMLRoundTrip — экспорт и импорт не теряют ни одного поля. YAML нужен,
// чтобы перенести настройки на другую панель, а не чтобы их растерять.
func TestYAMLRoundTrip(t *testing.T) {
	original := config.Config{
		Listen: ":8443",
		TLS: config.TLS{
			Cert: "/etc/hysteria-ward/certs/panel.crt",
			Key:  "/etc/hysteria-ward/certs/panel.key",
		},
		Obfs:       config.Obfs{Password: "salamander"},
		Masquerade: config.Masquerade{Mode: config.MasqueradeProxy, Proxy: "https://example.com"},
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

// TestDefault — у конфига есть осмысленное начальное состояние: штатный порт
// Hysteria 2, masquerade выключен, обфускации нет.
//
// Сертификата в нём нет и быть не может: его выдаёт шаг домена. Поэтому конфиг
// по умолчанию ещё не годен к запуску, и единственная претензия к нему — про
// сертификат.
func TestDefault(t *testing.T) {
	def := config.Default()

	if def.Listen != ":443" {
		t.Errorf("Listen = %q, ожидалось \":443\"", def.Listen)
	}
	if def.Masquerade.Mode != config.MasqueradeOff {
		t.Errorf("Masquerade.Mode = %q, ожидался %q", def.Masquerade.Mode, config.MasqueradeOff)
	}
	if def.TLS.Cert != "" || def.TLS.Key != "" {
		t.Errorf("в конфиге по умолчанию появился сертификат: %+v", def.TLS)
	}

	err := def.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, ожидалась претензия к отсутствующему сертификату")
	}
	if !strings.Contains(err.Error(), "сертификат") {
		t.Errorf("Validate() = %v, ожидалась претензия к сертификату", err)
	}
}

// TestValidate — негодную конфигурацию лучше отсечь до запуска ядра: у ядра та
// же ошибка выглядела бы падением, а не подсказкой.
func TestValidate(t *testing.T) {
	cases := map[string]config.Config{}

	listenless := valid()
	listenless.Listen = ""
	cases["пустой адрес"] = listenless

	certless := valid()
	certless.TLS = config.TLS{}
	cases["без сертификата"] = certless

	keyless := valid()
	keyless.TLS.Key = ""
	cases["без ключа"] = keyless

	unknownMode := valid()
	unknownMode.Masquerade = config.Masquerade{Mode: "какой-то"}
	cases["неизвестный режим masquerade"] = unknownMode

	fileWithoutPath := valid()
	fileWithoutPath.Masquerade = config.Masquerade{Mode: config.MasqueradeFile}
	cases["file без пути"] = fileWithoutPath

	proxyWithoutAddr := valid()
	proxyWithoutAddr.Masquerade = config.Masquerade{Mode: config.MasqueradeProxy}
	cases["proxy без адреса"] = proxyWithoutAddr

	for _, raw := range []string{"example.com", "ftp://example.com", "https://"} {
		brokenProxy := valid()
		brokenProxy.Masquerade = config.Masquerade{Mode: config.MasqueradeProxy, Proxy: raw}
		cases["proxy с адресом "+raw] = brokenProxy
	}

	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if err := cfg.Validate(); err == nil {
				t.Errorf("Validate() = nil, ожидалась ошибка")
			}
		})
	}

	t.Run("годная конфигурация", func(t *testing.T) {
		if err := valid().Validate(); err != nil {
			t.Errorf("Validate() = %v, ожидалось отсутствие ошибки", err)
		}
	})

	t.Run("годный сайт и зеркало", func(t *testing.T) {
		site := valid()
		site.Masquerade = config.Masquerade{Mode: config.MasqueradeFile, File: "/srv/www"}
		if err := site.Validate(); err != nil {
			t.Errorf("file: Validate() = %v, ожидалось отсутствие ошибки", err)
		}

		mirror := valid()
		mirror.Masquerade = config.Masquerade{Mode: config.MasqueradeProxy, Proxy: "https://example.com/news"}
		if err := mirror.Validate(); err != nil {
			t.Errorf("proxy: Validate() = %v, ожидалось отсутствие ошибки", err)
		}
	})
}
