package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// ErrUserExists — пользователь с таким именем уже есть: имена видны людям и
// должны быть уникальны, иначе ключ одного открывал бы статистику другого.
var ErrUserExists = errors.New("пользователь с таким именем уже есть")

// ErrUserMissing — пользователя нет: его удалили или идентификатор перепутан.
var ErrUserMissing = errors.New("пользователя нет")

// User — пользователь сервера Hysteria 2.
//
// Пароль хранится в открытом виде (ADR-0006): панель обязана выдать его повторно
// вместе с ключом, а из хеша ключ не собрать. Защита — права на файл базы и HTTPS
// интерфейса, а не хеширование.
type User struct {
	// ID — постоянный идентификатор: ключ записи и имя пользователя в статистике.
	// Имя меняется, идентификатор — нет, иначе переименование рвало бы связи и
	// начинало статистику заново.
	ID string `json:"id"`

	// Name — имя для администратора. Уникально среди пользователей.
	Name string `json:"name"`

	// Password — строка авторизации Hysteria 2.
	Password string `json:"password"`

	// Enabled — выключенный пользователь не проходит авторизацию, а активные
	// соединения рвутся на первом же пакете.
	Enabled bool `json:"enabled"`

	// CreatedAt и UpdatedAt — когда завели и когда меняли в последний раз.
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Users возвращает всех пользователей по именам.
func (s *Store) Users() ([]User, error) {
	var users []User

	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketUsers)
		if b == nil {
			return errors.New("нет бакета users")
		}

		return b.ForEach(func(_, raw []byte) error {
			user, err := decodeUser(raw)
			if err != nil {
				return err
			}

			users = append(users, user)

			return nil
		})
	})
	if err != nil {
		return nil, err
	}

	sortUsers(users)

	return users, nil
}

// User ищет пользователя по идентификатору.
func (s *Store) User(id string) (User, bool, error) {
	var (
		user  User
		found bool
	)

	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketUsers)
		if b == nil {
			return errors.New("нет бакета users")
		}

		raw := b.Get([]byte(id))
		if raw == nil {
			return nil
		}

		found = true

		var err error
		user, err = decodeUser(raw)

		return err
	})
	if err != nil {
		return User{}, false, err
	}

	return user, found, nil
}

// CreateUser заводит пользователя. Идентификатор и даты ставит хранилище: они
// принадлежат записи, а не тому, кто её создал.
func (s *Store) CreateUser(user User) (User, error) {
	id, err := newUserID()
	if err != nil {
		return User{}, err
	}

	now := time.Now().UTC()
	user.ID = id
	user.CreatedAt = now
	user.UpdatedAt = now

	err = s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketUsers)
		if b == nil {
			return errors.New("нет бакета users")
		}

		taken, err := userNamed(tx, user.Name, "")
		if err != nil {
			return err
		}
		if taken {
			return ErrUserExists
		}

		return putUser(b, user)
	})
	if err != nil {
		return User{}, err
	}

	return user, nil
}

// UpdateUser записывает изменённого пользователя по идентификатору. Имя тоже
// можно поменять: идентификатор остаётся прежним.
func (s *Store) UpdateUser(user User) (User, error) {
	var saved User

	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketUsers)
		if b == nil {
			return errors.New("нет бакета users")
		}

		raw := b.Get([]byte(user.ID))
		if raw == nil {
			return ErrUserMissing
		}

		current, err := decodeUser(raw)
		if err != nil {
			return err
		}

		taken, err := userNamed(tx, user.Name, user.ID)
		if err != nil {
			return err
		}
		if taken {
			return ErrUserExists
		}

		saved = user
		saved.CreatedAt = current.CreatedAt
		saved.UpdatedAt = time.Now().UTC()

		return putUser(b, saved)
	})
	if err != nil {
		return User{}, err
	}

	return saved, nil
}

// DeleteUser удаляет пользователя. Отсутствующего удалять нечего, и это не
// ошибка: команда уже выполнена.
func (s *Store) DeleteUser(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketUsers)
		if b == nil {
			return errors.New("нет бакета users")
		}

		return b.Delete([]byte(id))
	})
}

// sortUsers ставит пользователей по именам. Имена людские, поэтому сравнение идёт
// без учёта регистра: «Alice» и «alice» не должны стоять в разных концах списка.
func sortUsers(users []User) {
	sort.SliceStable(users, func(i, j int) bool {
		return strings.ToLower(users[i].Name) < strings.ToLower(users[j].Name)
	})
}

// sameName сравнивает имена без учёта регистра: два пользователя, различающиеся
// только регистром, — это ловушка для того, кто ищет, кому выдать ключ.
func sameName(a, b string) bool {
	return strings.EqualFold(a, b)
}

// userNamed сообщает, занято ли имя другим пользователем. except — идентификатор
// того, кто имя меняет: своё же имя ему не мешает.
func userNamed(tx *bolt.Tx, name, except string) (bool, error) {
	b := tx.Bucket(bucketUsers)

	taken := false

	err := b.ForEach(func(_, raw []byte) error {
		user, err := decodeUser(raw)
		if err != nil {
			return err
		}

		if user.ID != except && sameName(user.Name, name) {
			taken = true
		}

		return nil
	})

	return taken, err
}

// newUserID возвращает случайный идентификатор. Случайный, а не порядковый:
// порядковый выдавал бы, сколько пользователей заведено, и после удаления
// повторялся бы.
func newUserID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("идентификатор пользователя: %w", err)
	}

	return hex.EncodeToString(raw[:]), nil
}

// putUser записывает пользователя в бакет.
func putUser(b *bolt.Bucket, user User) error {
	raw, err := json.Marshal(user)
	if err != nil {
		return fmt.Errorf("пользователь %q: %w", user.Name, err)
	}

	if err := b.Put([]byte(user.ID), raw); err != nil {
		return fmt.Errorf("пользователь %q: %w", user.Name, err)
	}

	return nil
}

// decodeUser разбирает запись пользователя.
func decodeUser(raw []byte) (User, error) {
	var user User
	if err := json.Unmarshal(raw, &user); err != nil {
		return User{}, fmt.Errorf("запись пользователя испорчена: %w", err)
	}

	return user, nil
}
