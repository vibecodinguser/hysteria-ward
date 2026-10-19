package store

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// keyACMEEmail — адрес, на который ACME заводит учётную запись и шлёт письма о
// сертификате. Он не нужен ядру, но нужен выпуску сертификата; поэтому лежит в
// appstate рядом с доменом, а не в конфигурации сервера.
var keyACMEEmail = []byte("acme_email")

// ErrBadEmail — то, что ввели, не похоже на адрес электронной почты.
var ErrBadEmail = errors.New("недопустимый e-mail")

// ACMEEmail возвращает адрес для ACME. Пустая строка означает «не задан».
func (s *Store) ACMEEmail() (string, error) {
	var email string

	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketAppState)
		if b == nil {
			return errors.New("нет бакета appstate")
		}

		email = string(b.Get(keyACMEEmail))

		return nil
	})
	if err != nil {
		return "", err
	}

	return email, nil
}

// normalizeACMEEmail приводит адрес к виду, в котором он хранится.
//
// ACME принимает обычный почтовый адрес, а не строку с именем отправителя:
// display-name в настройках был бы принят за часть адреса и сломал бы запрос.
func normalizeACMEEmail(email string) (string, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return "", nil
	}

	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email {
		return "", fmt.Errorf("%w: %q — укажите адрес вида name@example.com", ErrBadEmail, email)
	}

	// ACME требует домен, а не локальное имя: a@localhost некуда отправить.
	at := strings.LastIndex(address.Address, "@")
	if at <= 0 || !strings.Contains(address.Address[at+1:], ".") {
		return "", fmt.Errorf("%w: %q — нужен полный адрес с доменом", ErrBadEmail, email)
	}

	return address.Address, nil
}
