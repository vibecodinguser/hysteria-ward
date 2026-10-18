package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/vibecodinguser/hysteria-ward/internal/authn"
)

// Учётные данные администратора и сессии лежат в базе. Сессия обязана переживать
// перезапуск панели: обновление не должно выбрасывать администратора из панели, а
// он в этот момент может чинить именно то, ради чего перезапуск и затевался.

var (
	keyAdminName = []byte("admin_name")
	keyAdminHash = []byte("admin_hash")
)

// authBucket достаёт бакет auth. Его заводит схема, поэтому отсутствие бакета
// означает испорченную базу, а не обычное состояние.
func authBucket(tx *bolt.Tx) (*bolt.Bucket, error) {
	b := tx.Bucket(bucketAuth)
	if b == nil {
		return nil, errors.New("нет бакета auth")
	}

	return b, nil
}

// Credential возвращает имя администратора и хеш его пароля. Последнее значение
// ложно, если пароль ещё не задавали: тогда в панель не войти никому.
func (s *Store) Credential() (string, string, bool, error) {
	var (
		name string
		hash string
		ok   bool
	)

	err := s.db.View(func(tx *bolt.Tx) error {
		b, err := authBucket(tx)
		if err != nil {
			return err
		}

		rawName := b.Get(keyAdminName)
		rawHash := b.Get(keyAdminHash)
		if rawName == nil || rawHash == nil {
			return nil
		}

		name, hash, ok = string(rawName), string(rawHash), true

		return nil
	})
	if err != nil {
		return "", "", false, err
	}

	return name, hash, ok, nil
}

// SaveCredential записывает имя администратора и хеш его пароля. Пароль в
// открытом виде не сохраняется нигде и никогда.
func (s *Store) SaveCredential(name, hash string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := authBucket(tx)
		if err != nil {
			return err
		}

		if err := b.Put(keyAdminName, []byte(name)); err != nil {
			return fmt.Errorf("имя администратора: %w", err)
		}

		if err := b.Put(keyAdminHash, []byte(hash)); err != nil {
			return fmt.Errorf("хеш пароля: %w", err)
		}

		return nil
	})
}

// sessionRecord — сессия, как она лежит в базе. Это отдельная от authn.Session
// структура, потому что запись — это версия формата: меняться она может не
// заодно с кодом.
type sessionRecord struct {
	CSRF    string `json:"csrf"`
	Created int64  `json:"created_ns"`
	Seen    int64  `json:"seen_ns"`
}

// SaveSession записывает сессию целиком: и вход, и продление — одна и та же
// запись.
func (s *Store) SaveSession(session authn.Session) error {
	raw, err := json.Marshal(sessionRecord{
		CSRF:    session.CSRF,
		Created: session.Created.UnixNano(),
		Seen:    session.Seen.UnixNano(),
	})
	if err != nil {
		return fmt.Errorf("сериализация сессии: %w", err)
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := sessionsBucket(tx)
		if err != nil {
			return err
		}

		if err := b.Put([]byte(session.Token), raw); err != nil {
			return fmt.Errorf("запись сессии: %w", err)
		}

		return nil
	})
}

// Session читает сессию по токену.
func (s *Store) Session(token string) (authn.Session, bool, error) {
	var (
		session authn.Session
		ok      bool
	)

	err := s.db.View(func(tx *bolt.Tx) error {
		b, err := sessionsBucket(tx)
		if err != nil {
			return err
		}

		raw := b.Get([]byte(token))
		if raw == nil {
			return nil
		}

		var record sessionRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return fmt.Errorf("разбор сессии: %w", err)
		}

		session, ok = authn.Session{
			Token:   token,
			CSRF:    record.CSRF,
			Created: time.Unix(0, record.Created),
			Seen:    time.Unix(0, record.Seen),
		}, true

		return nil
	})
	if err != nil {
		return authn.Session{}, false, err
	}

	return session, ok, nil
}

// DeleteSession закрывает сессию. Закрытие уже закрытой — не ошибка: два окна
// могли выйти одновременно.
func (s *Store) DeleteSession(token string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := sessionsBucket(tx)
		if err != nil {
			return err
		}

		return b.Delete([]byte(token))
	})
}

// DeleteStaleSessions выметает сессии, которых не касались с указанного времени,
// и возвращает, сколько вымел.
func (s *Store) DeleteStaleSessions(before time.Time) (int, error) {
	removed := 0

	err := s.db.Update(func(tx *bolt.Tx) error {
		b, err := sessionsBucket(tx)
		if err != nil {
			return err
		}

		// Удалять в bbolt можно прямо во время обхода: курсор переживает удаление
		// текущего ключа.
		cursor := b.Cursor()
		for key, raw := cursor.First(); key != nil; key, raw = cursor.Next() {
			var record sessionRecord
			if err := json.Unmarshal(raw, &record); err != nil {
				return fmt.Errorf("разбор сессии: %w", err)
			}

			if time.Unix(0, record.Seen).Before(before) {
				if err := cursor.Delete(); err != nil {
					return fmt.Errorf("удаление сессии: %w", err)
				}

				removed++
			}
		}

		return nil
	})
	if err != nil {
		return 0, err
	}

	return removed, nil
}

// sessionsBucket достаёт бакет sessions.
func sessionsBucket(tx *bolt.Tx) (*bolt.Bucket, error) {
	b := tx.Bucket(bucketSessions)
	if b == nil {
		return nil, errors.New("нет бакета sessions")
	}

	return b, nil
}
