// Пакет config описывает конфигурацию сервера Hysteria 2 так, как её понимает
// панель: структурой, а не текстом YAML. YAML — только формат переноса настроек
// на другую панель.
package config

import (
	"bytes"
	"fmt"

	"go.yaml.in/yaml/v3"
)

// MasqueradeMode — что показывать постороннему запросу, пришедшему не по
// протоколу Hysteria.
type MasqueradeMode string

const (
	// MasqueradeOff — не отвечать подставным содержимым.
	MasqueradeOff MasqueradeMode = "off"
	// MasqueradeFile — отдавать свой сайт.
	MasqueradeFile MasqueradeMode = "file"
	// MasqueradeProxy — служить зеркалом чужого сайта.
	MasqueradeProxy MasqueradeMode = "proxy"
)

// Config — конфигурация сервера.
//
// Имена в базе и в YAML заданы тегами, а не именами полей: переименование поля в
// Go не должно превращаться в молчаливую потерю настройки.
type Config struct {
	Listen     string     `json:"listen" yaml:"listen"`
	Panel      Panel      `json:"panel" yaml:"panel"`
	TLS        TLS        `json:"tls" yaml:"tls"`
	Obfs       Obfs       `json:"obfs" yaml:"obfs"`
	Masquerade Masquerade `json:"masquerade" yaml:"masquerade"`
}

// Panel — HTTPS-сервер самой панели. У него свой адрес, потому что ядро занято
// клиентским трафиком, и порт панели от него не зависит.
type Panel struct {
	Listen string `json:"listen" yaml:"listen"`
}

// TLS — где лежат сертификат и ключ.
type TLS struct {
	Cert string `json:"cert" yaml:"cert"`
	Key  string `json:"key" yaml:"key"`
}

// Obfs — обфускация salamander. Пустой пароль означает, что она выключена.
type Obfs struct {
	Password string `json:"password" yaml:"password"`
}

// Masquerade — подставной ответ на посторонний запрос.
type Masquerade struct {
	Mode  MasqueradeMode `json:"mode" yaml:"mode"`
	File  string         `json:"file" yaml:"file"`
	Proxy string         `json:"proxy" yaml:"proxy"`
}

// Default — начальное состояние новой панели: штатный порт Hysteria 2, панель на
// петле (снаружи её не видно, пока доступ не включат явно), без обфускации и
// masquerade. Годным к запуску его делает сертификат, который появится на шаге
// домена.
func Default() Config {
	return Config{
		Listen:     ":443",
		Panel:      Panel{Listen: "127.0.0.1:8443"},
		Masquerade: Masquerade{Mode: MasqueradeOff},
	}
}

// FillDefaults дополняет конфигурацию значениями по умолчанию.
//
// Нужно базе, записанной прежней панелью: в ней новых полей ещё нет, а пустое поле
// — это не «выключено», а «не задано». Разница принципиальная: адрес панели,
// оставленный пустым, означал бы прослушивание всех интерфейсов, то есть панель,
// выставленную наружу помимо воли администратора.
func (c Config) FillDefaults() Config {
	defaults := Default()

	if c.Listen == "" {
		c.Listen = defaults.Listen
	}

	if c.Panel.Listen == "" {
		c.Panel.Listen = defaults.Panel.Listen
	}

	if c.Masquerade.Mode == "" {
		c.Masquerade = defaults.Masquerade
	}

	return c
}

// Validate проверяет, что конфигурация годна к запуску. Проверяется только то,
// что видно без обращения к диску: содержимое сертификата читается отдельно, уже
// перед остановкой работающего ядра.
func (c Config) Validate() error {
	if c.Listen == "" {
		return fmt.Errorf("не задан адрес прослушивания")
	}

	if c.Panel.Listen == "" {
		return fmt.Errorf("не задан адрес панели")
	}

	if c.TLS.Cert == "" || c.TLS.Key == "" {
		return fmt.Errorf("tls: не заданы пути к сертификату и ключу")
	}

	switch c.Masquerade.Mode {
	case MasqueradeOff:
	case MasqueradeFile:
		if c.Masquerade.File == "" {
			return fmt.Errorf("masquerade: не задан каталог сайта")
		}
	case MasqueradeProxy:
		if c.Masquerade.Proxy == "" {
			return fmt.Errorf("masquerade: не задан адрес зеркала")
		}
	default:
		return fmt.Errorf("masquerade: неизвестный режим %q", c.Masquerade.Mode)
	}

	return nil
}

// YAML выгружает конфигурацию для переноса на другую панель.
func (c Config) YAML() ([]byte, error) {
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("выгрузка конфигурации: %w", err)
	}

	return data, nil
}

// ParseYAML разбирает конфигурацию и требует, чтобы все поля были известны:
// иначе опечатка молча пропала бы, а панель считала бы, что всё применила.
func ParseYAML(data []byte) (Config, error) {
	var c Config

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("разбор конфигурации: %w", err)
	}

	return c, nil
}
