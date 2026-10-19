package certs_test

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/certs"
)

// TestSaveLoadRoundTrip — выпущенный сертификат переживает запись и чтение:
// панель и ядро берут его из файлов, а не из памяти процесса.
func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "fullchain.pem")
	keyPath := filepath.Join(dir, "privkey.pem")

	certificate, err := certs.SelfSigned("panel.example.com", "example.com")
	if err != nil {
		t.Fatalf("SelfSigned() = %v", err)
	}

	if err := certs.Save(certPath, keyPath, certificate); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	loaded, err := certs.Load(certPath, keyPath)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	if loaded.Leaf == nil {
		t.Fatal("Load() не разобрал лист сертификата")
	}
	if string(loaded.Certificate[0]) != string(certificate.Certificate[0]) {
		t.Error("прочитан другой сертификат")
	}
	if loaded.PrivateKey == nil {
		t.Error("ключ не прочитан")
	}
}

// TestSaveUsesPrivatePermissions — сертификат и ключ не должны быть доступны
// посторонним: ключ от сертификата панели — это ключ от панели.
func TestSaveUsesPrivatePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "certs")
	certPath := filepath.Join(dir, "fullchain.pem")
	keyPath := filepath.Join(dir, "privkey.pem")

	certificate, err := certs.SelfSigned("localhost")
	if err != nil {
		t.Fatalf("SelfSigned() = %v", err)
	}
	if err := certs.Save(certPath, keyPath, certificate); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	for _, path := range []string{certPath, keyPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%q) = %v", path, err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("права %q = %o, ожидались 600", path, mode)
		}
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat(dir) = %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Errorf("права каталога = %o, ожидались 700", mode)
	}
}

// TestSaveLeavesNoTemporaryFiles — временные файлы после записи не остаются: в
// каталоге сертификатов не должно быть мусора с ключом.
func TestSaveLeavesNoTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "fullchain.pem")
	keyPath := filepath.Join(dir, "privkey.pem")

	certificate, err := certs.SelfSigned("localhost")
	if err != nil {
		t.Fatalf("SelfSigned() = %v", err)
	}
	if err := certs.Save(certPath, keyPath, certificate); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() = %v", err)
	}

	for _, entry := range entries {
		if entry.Name() != "fullchain.pem" && entry.Name() != "privkey.pem" {
			t.Errorf("в каталоге остался лишний файл %q", entry.Name())
		}
	}
}

// TestLoadRejectsMissingFiles — отсутствие файлов — понятная ошибка, а не пустой
// сертификат, который потом упадёт на рукопожатии.
func TestLoadRejectsMissingFiles(t *testing.T) {
	dir := t.TempDir()

	if _, err := certs.Load(filepath.Join(dir, "нет.pem"), filepath.Join(dir, "нет.key")); err == nil {
		t.Fatal("Load() без файлов = nil, ожидалась ошибка")
	}
}

// TestRenewAt — продление начинается заранее, но не в первый же день: у
// сертификата на 90 дней запас — треть срока.
func TestRenewAt(t *testing.T) {
	certificate, err := certs.SelfSigned("localhost")
	if err != nil {
		t.Fatalf("SelfSigned() = %v", err)
	}

	renewAt := certs.RenewAt(certificate)
	if renewAt.IsZero() {
		t.Fatal("RenewAt() = нулевое время")
	}

	lifetime := certificate.Leaf.NotAfter.Sub(certificate.Leaf.NotBefore)
	want := certificate.Leaf.NotAfter.Add(-lifetime / 3)
	if renewAt.Sub(want).Abs() > time.Minute {
		t.Errorf("RenewAt() = %v, ожидалось около %v", renewAt, want)
	}

	if certs.NeedsRenewal(certificate, time.Now()) {
		t.Error("свежий сертификат уже требует продления")
	}
	if !certs.NeedsRenewal(certificate, renewAt.Add(time.Minute)) {
		t.Error("сертификат в срок продления не требует продления")
	}
}

// TestNeedsRenewalWithoutCertificate — пустой или неразобранный сертификат
// годится только на замену.
func TestNeedsRenewalWithoutCertificate(t *testing.T) {
	if !certs.NeedsRenewal(tls.Certificate{}, time.Now()) {
		t.Error("пустой сертификат не требует продления")
	}
}

// TestManagerIsSafeForConcurrentUse — сертификат читают оба TLS-слушателя, а
// меняет фоновый выпуск; гонки здесь не должно быть.
func TestManagerIsSafeForConcurrentUse(t *testing.T) {
	certificate, err := certs.SelfSigned("localhost")
	if err != nil {
		t.Fatalf("SelfSigned() = %v", err)
	}

	manager := certs.NewManager("", "")
	manager.Set(certificate)

	var group sync.WaitGroup
	for i := 0; i < 10; i++ {
		group.Add(2)
		go func() {
			defer group.Done()
			_ = manager.Current()
			_, _ = manager.GetCertificate(nil)
		}()
		go func() {
			defer group.Done()
			manager.Set(certificate)
		}()
	}
	group.Wait()
}
