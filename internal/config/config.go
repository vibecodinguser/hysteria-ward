// Пакет config описывает конфигурацию сервера Hysteria 2 так, как её понимает
// панель: структурой, а не текстом YAML. YAML — только формат переноса настроек
// на другую панель.
package config

import (
	"bytes"
	"fmt"
	"net/url"

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
	TLS        TLS        `json:"tls" yaml:"tls"`
	Obfs       Obfs       `json:"obfs" yaml:"obfs"`
	Masquerade Masquerade `json:"masquerade" yaml:"masquerade"`
	Bandwidth  Bandwidth  `json:"bandwidth" yaml:"bandwidth"`
}

// MinBandwidth — минимальный ненулевой предел скорости, который принимает ядро
// Hysteria 2: 64 КиБ/с. Меньшее значение ядро отвергает при запуске.
const MinBandwidth uint64 = 65536

// Bandwidth — глобальные пределы скорости сервера в байтах в секунду. Ноль
// означает «без ограничения».
//
// Hysteria 2 core v2.13 умеет ограничивать скорость только целиком для сервера:
// персональных пределов в его Authenticator нет. Поэтому это настройка сервера,
// а не пользователя.
type Bandwidth struct {
	Up   uint64 `json:"up" yaml:"up"`
	Down uint64 `json:"down" yaml:"down"`
}

// Validate проверяет пределы скорости. Ядро принимает либо ноль, либо не меньше
// 64 КиБ/с; проверка здесь ловит опечатку до остановки работающего сервера.
func (b Bandwidth) Validate() error {
	if b.Up != 0 && b.Up < MinBandwidth {
		return fmt.Errorf("bandwidth: отдача должна быть 0 или не меньше %d байт/с", MinBandwidth)
	}
	if b.Down != 0 && b.Down < MinBandwidth {
		return fmt.Errorf("bandwidth: приём должен быть 0 или не меньше %d байт/с", MinBandwidth)
	}

	return nil
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

// Validate проверяет, что режим masquerade задан осмысленно.
//
// Существование каталога здесь не проверяется: конфигурация может быть черновиком,
// а диск читает тот, кто применяет настройку. Проверка без диска ловит опечатку в
// режиме и адресе зеркала ещё до того, как правка уйдёт в базу.
func (m Masquerade) Validate() error {
	switch m.Mode {
	case MasqueradeOff:
	case MasqueradeFile:
		if m.File == "" {
			return fmt.Errorf("masquerade: не задан каталог сайта")
		}
	case MasqueradeProxy:
		if m.Proxy == "" {
			return fmt.Errorf("masquerade: не задан адрес зеркала")
		}

		target, err := url.Parse(m.Proxy)
		if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
			return fmt.Errorf("masquerade: адрес зеркала должен быть вида http(s)://хост")
		}
	default:
		return fmt.Errorf("masquerade: неизвестный режим %q", m.Mode)
	}

	return nil
}

// Default — начальное состояние новой панели: штатный порт Hysteria 2, без
// обфускации и masquerade. Годным к запуску его делает сертификат, который
// появится на шаге домена.
//
// Самой панели в этой конфигурации нет: кому и на каком порту она слушает — её
// собственные настройки (store), а не настройки сервера Hysteria 2.
func Default() Config {
	return Config{
		Listen:     ":443",
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

	if c.TLS.Cert == "" || c.TLS.Key == "" {
		return fmt.Errorf("tls: не заданы пути к сертификату и ключу")
	}

	if err := c.Masquerade.Validate(); err != nil {
		return err
	}

	if err := c.Bandwidth.Validate(); err != nil {
		return err
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
