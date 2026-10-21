package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

// API-ключи — отдельный вход для автоматизации и MCP. Они не заменяют вход
// администратора: ключ не даёт cookie-сессию и не открывает /api/. В базе лежит
// только хеш токена, поэтому потерянная база не отдаёт действующие ключи.

const (
	// apiKeyPrefix помогает отличить ключ панели от чужого токена в логах и в
	// копипасте.
	apiKeyPrefix = "hwmcp_"

	// apiKeyBytes — длина случайной части токена. 32 байта угадать нельзя.
	apiKeyBytes = 32

	// maxAPIKeyName — предел имени: имя нужно человеку, а не протоколу.
	maxAPIKeyName = 64
)

var (
	// ErrAPIKeyExists — ключ с таким именем уже есть.
	ErrAPIKeyExists = errors.New("API-ключ с таким именем уже есть")

	// ErrAPIKeyName — имя ключа пустое или слишком длинное.
	ErrAPIKeyName = errors.New("недопустимое имя API-ключа")

	// ErrAPIKeyMissing — ключа нет: его отозвали или идентификатор перепутан.
	ErrAPIKeyMissing = errors.New("API-ключа нет")
)

// APIKey — ключ, каким его видит интерфейс. Токен и его хеш сюда не входят:
// первый показывают один раз при выпуске, второй не нужен никому, кроме проверки.
type APIKey struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// apiKeyRecord — то, что лежит в базе. Отдельная структура, потому что это формат
// хранения, а не ответ интерфейсу.
type apiKeyRecord struct {
	Name      string `json:"name"`
	Hash      string `json:"hash"`
	CreatedAt int64  `json:"created_at_ns"`
}

// apiKeysBucket достаёт бакет apikeys. Его заводит схема, поэтому отсутствие
// бакета означает испорченную базу.
func apiKeysBucket(tx *bolt.Tx) (*bolt.Bucket, error) {
	b := tx.Bucket(bucketAPIKeys)
	if b == nil {
		return nil, errors.New("нет бакета apikeys")
	}

	return b, nil
}

// CreateAPIKey выпускает ключ и возвращает его токен. Токен показывается один
// раз: в базе остаётся только хеш, восстановить его нельзя.
func (s *Store) CreateAPIKey(name string) (APIKey, string, error) {
	name, err := checkAPIKeyName(name)
	if err != nil {
		return APIKey{}, "", err
	}

	token, err := newAPIKeyToken()
	if err != nil {
		return APIKey{}, "", err
	}

	id, err := newAPIKeyID()
	if err != nil {
		return APIKey{}, "", err
	}

	now := time.Now().UTC()
	record := apiKeyRecord{
		Name:      name,
		Hash:      hashAPIKey(token),
		CreatedAt: now.UnixNano(),
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return APIKey{}, "", fmt.Errorf("сериализация API-ключа: %w", err)
	}

	err = s.db.Update(func(tx *bolt.Tx) error {
		b, err := apiKeysBucket(tx)
		if err != nil {
			return err
		}

		taken, err := apiKeyNamed(tx, name, "")
		if err != nil {
			return err
		}
		if taken {
			return ErrAPIKeyExists
		}

		if err := b.Put([]byte(id), raw); err != nil {
			return fmt.Errorf("запись API-ключа: %w", err)
		}

		return nil
	})
	if err != nil {
		return APIKey{}, "", err
	}

	return APIKey{ID: id, Name: name, CreatedAt: now}, token, nil
}

// APIKeys перечисляет ключи без токенов и хешей.
func (s *Store) APIKeys() ([]APIKey, error) {
	var keys []APIKey

	err := s.db.View(func(tx *bolt.Tx) error {
		b, err := apiKeysBucket(tx)
		if err != nil {
			return err
		}

		return b.ForEach(func(key, raw []byte) error {
			var record apiKeyRecord
			if err := json.Unmarshal(raw, &record); err != nil {
				return fmt.Errorf("разбор API-ключа: %w", err)
			}

			keys = append(keys, APIKey{
				ID:        string(key),
				Name:      record.Name,
				CreatedAt: time.Unix(0, record.CreatedAt).UTC(),
			})

			return nil
		})
	})
	if err != nil {
		return nil, err
	}

	sort.SliceStable(keys, func(i, j int) bool {
		if !keys[i].CreatedAt.Equal(keys[j].CreatedAt) {
			return keys[i].CreatedAt.Before(keys[j].CreatedAt)
		}

		return keys[i].Name < keys[j].Name
	})

	return keys, nil
}

// AuthenticateAPIKey ищет ключ по токену. Сравнение хешей идёт постоянным
// временем: по времени ответа нельзя понять, сколько байт угадано.
func (s *Store) AuthenticateAPIKey(token string) (APIKey, bool, error) {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 256 || !strings.HasPrefix(token, apiKeyPrefix) {
		return APIKey{}, false, nil
	}

	provided := sha256.Sum256([]byte(token))

	var (
		key   APIKey
		found bool
	)

	err := s.db.View(func(tx *bolt.Tx) error {
		b, err := apiKeysBucket(tx)
		if err != nil {
			return err
		}

		return b.ForEach(func(id, raw []byte) error {
			if found {
				return nil
			}

			var record apiKeyRecord
			if err := json.Unmarshal(raw, &record); err != nil {
				return fmt.Errorf("разбор API-ключа: %w", err)
			}

			want, err := hex.DecodeString(record.Hash)
			if err != nil {
				return fmt.Errorf("хеш API-ключа: %w", err)
			}
			if len(want) != len(provided) {
				return nil
			}

			if subtle.ConstantTimeCompare(want, provided[:]) == 1 {
				key = APIKey{
					ID:        string(id),
					Name:      record.Name,
					CreatedAt: time.Unix(0, record.CreatedAt).UTC(),
				}
				found = true
			}

			return nil
		})
	})
	if err != nil {
		return APIKey{}, false, err
	}

	return key, found, nil
}

// DeleteAPIKey отзывает ключ. Уже отозванный ключ — не ошибка, но API хочет
// знать, что удалять было нечего, поэтому отсутствие возвращается отдельно.
func (s *Store) DeleteAPIKey(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := apiKeysBucket(tx)
		if err != nil {
			return err
		}

		if b.Get([]byte(id)) == nil {
			return ErrAPIKeyMissing
		}

		if err := b.Delete([]byte(id)); err != nil {
			return fmt.Errorf("удаление API-ключа: %w", err)
		}

		return nil
	})
}

// checkAPIKeyName проверяет имя ключа: оно видно человеку и должно быть непустым.
func checkAPIKeyName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%w: имя пустое", ErrAPIKeyName)
	}
	if utf8.RuneCountInString(name) > maxAPIKeyName {
		return "", fmt.Errorf("%w: длиннее %d символов", ErrAPIKeyName, maxAPIKeyName)
	}
	if strings.ContainsAny(name, "\x00\r\n\t") {
		return "", fmt.Errorf("%w: управляющие символы", ErrAPIKeyName)
	}

	return name, nil
}

// apiKeyNamed сообщает, занято ли имя. Сравнение без учёта регистра: два ключа
// «MCP» и «mcp» человек всё равно не различит.
func apiKeyNamed(tx *bolt.Tx, name, exceptID string) (bool, error) {
	b, err := apiKeysBucket(tx)
	if err != nil {
		return false, err
	}

	taken := false
	err = b.ForEach(func(id, raw []byte) error {
		if string(id) == exceptID {
			return nil
		}

		var record apiKeyRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return fmt.Errorf("разбор API-ключа: %w", err)
		}

		if strings.EqualFold(record.Name, name) {
			taken = true
		}

		return nil
	})

	return taken, err
}

// newAPIKeyToken собирает токен: префикс плюс случайные байты в base64url.
func newAPIKeyToken() (string, error) {
	raw := make([]byte, apiKeyBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("случайный API-ключ: %w", err)
	}

	return apiKeyPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// newAPIKeyID собирает идентификатор записи. Он не секрет: по нему ключ
// отзывают.
func newAPIKeyID() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("идентификатор API-ключа: %w", err)
	}

	return hex.EncodeToString(raw), nil
}

// hashAPIKey считает хеш токена. Для 256-битного случайного токена SHA-256
// достаточно: перебирать его бессмысленно, а медленный Argon2id на каждый запрос
// MCP только мешал бы.
func hashAPIKey(token string) string {
	sum := sha256.Sum256([]byte(token))

	return hex.EncodeToString(sum[:])
}
