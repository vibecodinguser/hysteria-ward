package certs_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/certs"
)

// TestManagerReportsCertificate — состояние сертификата показывает имена и срок
// действия: по нему интерфейс отвечает, что сертификат готов.
func TestManagerReportsCertificate(t *testing.T) {
	certificate, err := certs.SelfSigned("example.com", "panel.example.com")
	if err != nil {
		t.Fatalf("SelfSigned() = %v", err)
	}

	manager := certs.NewManager("", "")
	manager.Set(certificate)

	status := manager.Status()
	if !status.Ready {
		t.Error("сертификат не помечен готовым")
	}
	if status.Placeholder {
		t.Error("настоящий сертификат помечен временным")
	}
	if status.NotAfter.IsZero() {
		t.Error("не показан срок действия")
	}
	for _, name := range []string{"example.com", "panel.example.com"} {
		if !slices.Contains(status.Names, name) {
			t.Errorf("в состоянии нет имени %q: %v", name, status.Names)
		}
	}

	current, err := manager.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate() = %v", err)
	}
	if string(current.Certificate[0]) != string(certificate.Certificate[0]) {
		t.Error("GetCertificate() отдал другой сертификат")
	}
}

// TestManagerPlaceholder — временный сертификат помечается: панель должна
// объяснять администратору, почему браузер ругается.
func TestManagerPlaceholder(t *testing.T) {
	certificate, err := certs.SelfSigned("localhost")
	if err != nil {
		t.Fatalf("SelfSigned() = %v", err)
	}

	manager := certs.NewManager("", "")
	manager.SetPlaceholder(certificate)

	if !manager.Placeholder() {
		t.Error("временный сертификат не помечен")
	}
	if !manager.Status().Placeholder {
		t.Error("состояние не показывает временный сертификат")
	}
}

// TestManagerWithoutCertificate — без сертификата рукопожатие должно падать
// понятной ошибкой, а не пустым сертификатом.
func TestManagerWithoutCertificate(t *testing.T) {
	manager := certs.NewManager("", "")

	if _, err := manager.GetCertificate(nil); err == nil {
		t.Fatal("GetCertificate() без сертификата = nil, ожидалась ошибка")
	}
	if manager.Status().Ready {
		t.Error("пустой менеджер считает сертификат готовым")
	}
}

// TestManagerReload — после замены файлов сертификат перечитывается: иначе
// отпечаток в ключах остался бы от прежнего.
func TestManagerReload(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "fullchain.pem")
	keyPath := filepath.Join(dir, "privkey.pem")

	first, err := certs.SelfSigned("example.com")
	if err != nil {
		t.Fatalf("SelfSigned() = %v", err)
	}
	if err := certs.Save(certPath, keyPath, first); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	manager := certs.NewManager(certPath, keyPath)
	if err := manager.Reload(); err != nil {
		t.Fatalf("Reload() = %v", err)
	}

	second, err := certs.SelfSigned("example.com")
	if err != nil {
		t.Fatalf("SelfSigned() = %v", err)
	}
	if err := certs.Save(certPath, keyPath, second); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	if err := manager.Reload(); err != nil {
		t.Fatalf("Reload() = %v", err)
	}

	if string(manager.Current().Certificate[0]) == string(first.Certificate[0]) {
		t.Error("Reload() не заменил сертификат")
	}
}

// TestManagerReloadWithoutPaths — перечитывать нечего, если пути не заданы:
// ошибка должна быть понятной, а не паникой.
func TestManagerReloadWithoutPaths(t *testing.T) {
	if err := certs.NewManager("", "").Reload(); err == nil {
		t.Fatal("Reload() без путей = nil, ожидалась ошибка")
	}
}

// TestManagerRecordsFailures — причина отказа выпуска и время следующей попытки
// видны администратору.
func TestManagerRecordsFailures(t *testing.T) {
	manager := certs.NewManager("", "")
	manager.Failed(os.ErrPermission, time.Now().Add(time.Minute))

	status := manager.Status()
	if status.LastError == "" {
		t.Error("причина отказа не записана")
	}
}

// TestManagerNames — имена сертификата меняются на ходу: домен правят из
// интерфейса, а выпуск уже идёт со своим списком.
func TestManagerNames(t *testing.T) {
	manager := certs.NewManager("", "")

	if len(manager.DesiredNames()) != 0 {
		t.Errorf("у нового держателя уже есть имена: %v", manager.DesiredNames())
	}

	manager.SetNames([]string{"example.com", "panel.example.com"})
	manager.SetNames(nil)

	names := manager.DesiredNames()
	if len(names) != 2 || names[0] != "example.com" || names[1] != "panel.example.com" {
		t.Errorf("имена = %v, ожидались прежние: пустой список их не стирает", names)
	}
}

// TestManagerKickDoesNotBlock — повтор можно просить сколько угодно раз: лишняя
// просьба не должна вешать обработчик запроса.
func TestManagerKickDoesNotBlock(t *testing.T) {
	manager := certs.NewManager("", "")

	for i := 0; i < 3; i++ {
		manager.Kick()
	}
}
