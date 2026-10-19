package store

import (
	"errors"
	"fmt"

	bolt "go.etcd.io/bbolt"

	"github.com/vibecodinguser/hysteria-ward/internal/authn"
)

// Учётные данные администратора лежат в базе: панель читает их при каждом входе, а
// не держит в памяти, и перезапуск их не трогает.

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

// Credential возвращает учётные данные администратора. Последнее значение ложно,
// если пароль ещё не задавали: тогда в панель не войти никому.
func (s *Store) Credential() (authn.Credential, bool, error) {
	var (
		credential authn.Credential
		ok         bool
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

		credential = authn.Credential{Name: string(rawName), Hash: string(rawHash)}
		ok = true

		return nil
	})
	if err != nil {
		return authn.Credential{}, false, err
	}

	return credential, ok, nil
}

// SaveCredential записывает учётные данные администратора. Пароль в открытом виде
// не сохраняется нигде и никогда: на диске лежит только его хеш.
func (s *Store) SaveCredential(credential authn.Credential) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := authBucket(tx)
		if err != nil {
			return err
		}

		if err := b.Put(keyAdminName, []byte(credential.Name)); err != nil {
			return fmt.Errorf("имя администратора: %w", err)
		}

		if err := b.Put(keyAdminHash, []byte(credential.Hash)); err != nil {
			return fmt.Errorf("хеш пароля: %w", err)
		}

		return nil
	})
}
