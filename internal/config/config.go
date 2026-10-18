// Пакет config описывает конфигурацию сервера Hysteria 2 так, как её понимает
// панель: структурой, а не текстом YAML. YAML — только формат переноса настроек
// на другую панель.
package config

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// MasqMode — что показывать постороннему запросу, пришедшему не по протоколу
// Hysteria.
type MasqMode string

const (
	// MasqOff — не отвечать подставным содержимым.
	MasqOff MasqMode = "off"
	// MasqFile — отдавать свой сайт.
	MasqFile MasqMode = "file"
	// MasqProxy — служить зеркалом чужого сайта.
	MasqProxy MasqMode = "proxy"
)

// Config — конфигурация сервера.
type Config struct {
	Listen string     `yaml:"listen"`
	TLS    TLS        `yaml:"tls"`
	Obfs   Obfs       `yaml:"obfs"`
	Masq   Masquerade `yaml:"masquerade"`
}

// TLS — где лежат сертификат и ключ.
type TLS struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

// Obfs — обфускация salamander. Пустой пароль означает, что она выключена.
type Obfs struct {
	Password string `yaml:"password"`
}

// Masquerade — подставной ответ на посторонний запрос.
type Masquerade struct {
	Mode  MasqMode `yaml:"mode"`
	File  string   `yaml:"file"`
	Proxy string   `yaml:"proxy"`
}

// Default — конфигурация нового сервера: штатный порт Hysteria 2, без
// обфускации и masquerade.
func Default() Config {
	return Config{
		Listen: ":443",
		Masq:   Masquerade{Mode: MasqOff},
	}
}

// Validate проверяет, что конфигурация годна к запуску.
func (c Config) Validate() error {
	if c.Listen == "" {
		return fmt.Errorf("не задан адрес прослушивания")
	}

	switch c.Masq.Mode {
	case MasqOff:
	case MasqFile:
		if c.Masq.File == "" {
			return fmt.Errorf("masquerade: не задан каталог сайта")
		}
	case MasqProxy:
		if c.Masq.Proxy == "" {
			return fmt.Errorf("masquerade: не задан адрес зеркала")
		}
	default:
		return fmt.Errorf("masquerade: неизвестный режим %q", c.Masq.Mode)
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
