package certs

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// renewShare — какую долю срока жизни сертификата прожить до продления. Треть
// срока оставляет время на несколько неудачных попыток, не подходя к самому
// истечению.
const renewShare = 3

// maxRenewBefore — предел на запас до истечения: у сертификата на 90 дней
// продление за 30 дней, у сертификата на год — всё равно за 30, а не за 120.
const maxRenewBefore = 30 * 24 * time.Hour

// minRenewBefore — нижняя граница для короткоживущих сертификатов: продлевать
// чаще раза в минуту бессмысленно, а в тестах с минутным сроком это ещё и
// оставляет время увидеть продление.
const minRenewBefore = time.Minute

// Load читает сертификат и ключ и разбирает лист: по нему считаются сроки и
// имена, а tls.LoadX509KeyPair лист не заполняет.
func Load(certPath, keyPath string) (tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("сертификат: %w", err)
	}

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("разбор сертификата: %w", err)
	}
	cert.Leaf = leaf

	return cert, nil
}

// Save записывает цепочку и ключ в отдельные файлы с правами 0600.
//
// Ключ пишется первым, сертификат — вторым: появление сертификата установщик
// считает признаком того, что настоящий сертификат готов, и ключ к этому
// моменту уже должен лежать рядом.
func Save(certPath, keyPath string, cert tls.Certificate) error {
	if len(cert.Certificate) == 0 {
		return errors.New("сертификат: нечего записывать")
	}
	if cert.PrivateKey == nil {
		return errors.New("сертификат: нет ключа")
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		return fmt.Errorf("ключ сертификата: %w", err)
	}

	var chain, key bytes.Buffer
	for _, der := range cert.Certificate {
		if err := pem.Encode(&chain, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
			return fmt.Errorf("кодирование сертификата: %w", err)
		}
	}
	if err := pem.Encode(&key, &pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}); err != nil {
		return fmt.Errorf("кодирование ключа: %w", err)
	}

	if err := writeFile(keyPath, key.Bytes()); err != nil {
		return fmt.Errorf("ключ сертификата: %w", err)
	}
	if err := writeFile(certPath, chain.Bytes()); err != nil {
		return fmt.Errorf("сертификат: %w", err)
	}

	return nil
}

// RenewAt говорит, с какого момента сертификат пора продлевать. Нулевое время
// означает, что сертификат неразбираем и годится только на замену.
func RenewAt(cert tls.Certificate) time.Time {
	if cert.Leaf == nil {
		return time.Time{}
	}

	lifetime := cert.Leaf.NotAfter.Sub(cert.Leaf.NotBefore)
	before := lifetime / renewShare

	if before > maxRenewBefore {
		before = maxRenewBefore
	}
	if before < minRenewBefore {
		before = minRenewBefore
	}

	return cert.Leaf.NotAfter.Add(-before)
}

// NeedsRenewal сообщает, пора ли менять сертификат: истёк, скоро истечёт или не
// разбирается.
func NeedsRenewal(cert tls.Certificate, now time.Time) bool {
	if len(cert.Certificate) == 0 || cert.Leaf == nil {
		return true
	}

	return !now.Before(RenewAt(cert))
}

// writeFile кладёт данные в файл атомарно: временный файл в том же каталоге, затем
// переименование. Читатель видит либо старые байты целиком, либо новые, но не
// половину.
func writeFile(path string, data []byte) error {
	dir := filepath.Dir(path)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("каталог %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("временный файл: %w", err)
	}
	tmpName := tmp.Name()

	// Временный файл не должен остаться после ошибки ни с данными, ни с широкими
	// правами.
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("права на файл: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("запись: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("закрытие: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("переименование: %w", err)
	}

	return nil
}
