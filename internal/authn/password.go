package authn

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Параметры Argon2id. Хеш считается на каждую попытку входа, поэтому выбран
// размер, при котором подбор пароля стоит дорого, а ожидание администратора —
// незаметно: памяти 64 МиБ, один проход, два потока.
const (
	argonMemory  = 64 * 1024
	argonTime    = 1
	argonThreads = 2

	argonKeyLen  = 32
	argonSaltLen = 16

	// generatedPasswordBytes — сколько случайных байт в пароле, который панель
	// придумывает сама. 18 байт дают 24 символа base64url: перебрать такое не по
	// силам, а переписать с экрана — не трудно.
	generatedPasswordBytes = 18
)

// GeneratePassword придумывает пароль администратора.
//
// Символы — base64url: они переживают копирование, не требуют экранирования и не
// содержат похожих друг на друга пар. Пароль возвращается в открытом виде только
// тому, кто его печатает один раз; в базе остаётся Argon2id-хеш.
func GeneratePassword() (string, error) {
	raw := make([]byte, generatedPasswordBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("пароль: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Hash считает Argon2id-хеш пароля.
//
// Формат строки — тот же, что у всех Argon2: `$argon2id$v=19$m=...,t=...,p=...$`.
// Параметры и соль лежат в самом хеше, поэтому смена параметров не ломает уже
// записанные пароли: каждый читается со своими.
func Hash(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("соль: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// Verify сверяет пароль с хешем.
//
// Неподходящий пароль и испорченный хеш — разные вещи: первое случается постоянно
// и о нём молчат, второе означает поломку базы, и о ней нужно сказать.
func Verify(encoded, password string) (bool, error) {
	params, salt, key, err := parse(encoded)
	if err != nil {
		return false, err
	}

	got := argon2.IDKey([]byte(password), salt, params.time, params.memory, params.threads, uint32(len(key)))

	return subtle.ConstantTimeCompare(got, key) == 1, nil
}

// argonParams — параметры, с которыми считался хеш.
type argonParams struct {
	memory  uint32
	time    uint32
	threads uint8
}

// parse разбирает строку хеша.
func parse(encoded string) (argonParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return argonParams{}, nil, nil, errors.New("хеш не в формате argon2id")
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return argonParams{}, nil, nil, fmt.Errorf("версия argon2: %w", err)
	}

	if version != argon2.Version {
		return argonParams{}, nil, nil, fmt.Errorf("версия argon2 %d не поддерживается", version)
	}

	var params argonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &params.memory, &params.time, &params.threads); err != nil {
		return argonParams{}, nil, nil, fmt.Errorf("параметры argon2: %w", err)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return argonParams{}, nil, nil, fmt.Errorf("соль: %w", err)
	}

	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return argonParams{}, nil, nil, fmt.Errorf("ключ: %w", err)
	}

	if len(salt) == 0 || len(key) == 0 {
		return argonParams{}, nil, nil, errors.New("хеш без соли или ключа")
	}

	return params, salt, key, nil
}
