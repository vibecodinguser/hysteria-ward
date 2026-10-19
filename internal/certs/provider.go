package certs

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"sync"
	"time"
)

// Status — что панель знает о сертификате прямо сейчас. Это ответ для интерфейса
// и журнала: администратор должен видеть, выпущен ли сертификат, когда истекает и
// почему не выпустился.
type Status struct {
	// Ready — сертификат есть и может предъявляться клиентам.
	Ready bool `json:"ready"`

	// Placeholder — предъявляется временный самоподписанный сертификат. Он нужен,
	// чтобы панель открывалась до выпуска настоящего, и браузер его не признает.
	Placeholder bool `json:"placeholder"`

	// Mode — способ проверки ACME: http или dns.
	Mode string `json:"mode"`

	// Names — имена, которые покрывает сертификат.
	Names []string `json:"names"`

	// NotAfter — когда сертификат перестанет действовать.
	NotAfter time.Time `json:"not_after"`

	// LastError — почему последняя попытка выпуска не удалась. Пустая строка
	// означает, что последняя попытка удалась или ещё не было.
	LastError string `json:"last_error"`

	// LastAttempt — когда последний раз начинали выпуск.
	LastAttempt time.Time `json:"last_attempt"`

	// NextAttempt — когда выпуск повторится сам.
	NextAttempt time.Time `json:"next_attempt"`
}

// Manager держит текущий сертификат и отдаёт его и интерфейсу, и ядру.
//
// Сертификат меняется на ходу: выпуск и продление идут в фоне, а оба
// TLS-слушателя берут его через GetCertificate, а не запоминают копию. Поэтому
// продление не требует остановки панели.
type Manager struct {
	mu sync.RWMutex

	cert        tls.Certificate
	placeholder bool
	status      Status

	// certPath и keyPath нужны, чтобы перечитать сертификат после «Применить»:
	// файлы могли заменить вручную, и отпечаток в новых ключах должен совпадать с
	// тем, что отдаёт ядро.
	certPath string
	keyPath  string

	// desired — имена, на которые сертификат должен быть выписан сейчас. Домен
	// меняется из интерфейса, поэтому список не задаётся один раз при запуске.
	desired []string

	// wake будит цикл выпуска, когда администратор просит повторить сейчас.
	wake chan struct{}
}

// NewManager заводит держатель сертификата. Пути могут быть пустыми: тогда
// Reload нечего читать, а сертификат задаётся через Set.
func NewManager(certPath, keyPath string) *Manager {
	return &Manager{
		certPath: certPath,
		keyPath:  keyPath,
		wake:     make(chan struct{}, 1),
	}
}

// Set заменяет текущий сертификат настоящим.
func (m *Manager) Set(cert tls.Certificate) {
	if cert.Leaf == nil && len(cert.Certificate) != 0 {
		if leaf, err := x509.ParseCertificate(cert.Certificate[0]); err == nil {
			cert.Leaf = leaf
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.cert = cert
	m.placeholder = false
	m.status.Ready = len(cert.Certificate) != 0
	m.status.Placeholder = false

	if cert.Leaf != nil {
		m.status.Names = namesOf(cert.Leaf)
		m.status.NotAfter = cert.Leaf.NotAfter
	}
}

// SetPlaceholder задаёт временный сертификат. Пока он стоит, цикл выпуска не
// считает, что дело сделано, и пробует получить настоящий.
func (m *Manager) SetPlaceholder(cert tls.Certificate) {
	m.Set(cert)

	m.mu.Lock()
	defer m.mu.Unlock()

	m.placeholder = true
	m.status.Placeholder = true
}

// Current отдаёт текущий сертификат. Пустой сертификат означает, что выпуск ещё
// не удался.
func (m *Manager) Current() tls.Certificate {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.cert
}

// Placeholder сообщает, предъявляется ли сейчас временный сертификат.
func (m *Manager) Placeholder() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.placeholder
}

// GetCertificate отдаёт сертификат TLS-слушателю. Подпись совпадает с той, что
// ждёт crypto/tls, поэтому один и тот же метод годится и панели, и ядру.
func (m *Manager) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	cert := m.Current()
	if len(cert.Certificate) == 0 {
		return nil, errors.New("сертификат ещё не выпущен")
	}

	return &cert, nil
}

// Reload перечитывает сертификат с диска. Нужен после «Применить»: файлы могли
// заменить, и панель должна предъявлять то же, что ядро, и считать отпечаток по
// нему.
func (m *Manager) Reload() error {
	if m.certPath == "" || m.keyPath == "" {
		return errors.New("не заданы пути к сертификату")
	}

	cert, err := Load(m.certPath, m.keyPath)
	if err != nil {
		return err
	}

	m.Set(cert)

	return nil
}

// SetNames задаёт имена, на которые должен быть выписан сертификат. Пустой
// список ничего не меняет: стёртый домен — не повод выпускать сертификат без
// имён.
func (m *Manager) SetNames(names []string) {
	if len(names) == 0 {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.desired = append([]string(nil), names...)
}

// DesiredNames отдаёт имена, на которые сертификат должен быть выписан сейчас.
func (m *Manager) DesiredNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return append([]string(nil), m.desired...)
}

// Status отдаёт снимок состояния. Список имён копируется: вызывающий не должен
// менять состояние держателя.
func (m *Manager) Status() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()

	status := m.status
	status.Names = append([]string{}, status.Names...)

	return status
}

// SetMode запоминает способ проверки ACME для отчёта.
func (m *Manager) SetMode(mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.status.Mode = mode
}

// Attempt отмечает начало попытки выпуска.
func (m *Manager) Attempt(at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.status.LastAttempt = at
	m.status.NextAttempt = time.Time{}
}

// Succeeded отмечает удачную попытку и время следующего продления.
func (m *Manager) Succeeded(next time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.status.LastError = ""
	m.status.NextAttempt = next
}

// Failed отмечает неудачную попытку и время, когда её повторят.
func (m *Manager) Failed(err error, next time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.status.LastError = err.Error()
	m.status.NextAttempt = next
}

// Scheduled ставит время следующего продления, не трогая ошибку.
func (m *Manager) Scheduled(next time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.status.NextAttempt = next
}

// Kick просит цикл выпуска попробовать снова, не дожидаясь паузы. Просьба не
// копится: если цикл уже просыпается, вторая ничего не меняет.
func (m *Manager) Kick() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// namesOf перечисляет имена сертификата. Общее имя добавляется, только если
// DNSNames пуст: в сертификате ACME оно дублирует первое имя, а в отчёте
// повторять его незачем.
func namesOf(leaf *x509.Certificate) []string {
	names := append([]string(nil), leaf.DNSNames...)

	if len(names) == 0 && leaf.Subject.CommonName != "" {
		names = []string{leaf.Subject.CommonName}
	}

	return names
}
