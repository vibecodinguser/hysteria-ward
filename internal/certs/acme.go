package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
)

// Mode — способ, которым ACME убеждается, что домен наш.
type Mode string

const (
	// ModeHTTP — проверка по файлу на порту 80. Не требует доступа к DNS и
	// поэтому основной.
	ModeHTTP Mode = "http"

	// ModeDNS — проверка по записи TXT. Нужна там, где порт 80 закрыт или домен
	// обслуживает чужой сервер.
	ModeDNS Mode = "dns"
)

// DNS-провайдеры, которые панель умеет использовать для DNS-01.
const (
	// DNSProviderHook — внешняя исполняемая программа. Оставлена для
	// совместимости и для провайдеров, которых нет среди встроенных.
	DNSProviderHook = "hook"

	// DNSProviderCloudflare — Cloudflare API с токеном Zone.DNS Edit.
	DNSProviderCloudflare = "cloudflare"
)

// ACMEConfig — всё, что нужно, чтобы выпустить сертификат.
type ACMEConfig struct {
	// Names — имена будущего сертификата: домен сервера и, если панель живёт
	// на отдельном имени, домен панели. Один сертификат на оба: клиенты
	// Hysteria видят сервер по домену, а администратор заходит на панель по
	// тому же сертификату.
	Names []string

	// Email — адрес учётной записи ACME.
	Email string

	// DirectoryURL — адрес ACME-каталога. Пустая строка означает Let's Encrypt.
	DirectoryURL string

	// AccountKeyPath — файл ключа учётной записи. Ключ заводится один раз и
	// переиспользуется при продлении.
	AccountKeyPath string

	// CertPath и KeyPath — куда положить выпущенное.
	CertPath string
	KeyPath  string

	// Mode — способ проверки. По умолчанию HTTP-01.
	Mode Mode

	// DNSProvider — чем заводить TXT-запись: hook или cloudflare. Пустая
	// строка для DNS-01 означает hook: так настройки старых баз продолжают
	// работать без миграции.
	DNSProvider string

	// DNSHook — программа для DNS-01: вызывается как
	// «программа present <имя> <значение>» и «программа cleanup <имя> <значение>».
	DNSHook string

	// CloudflareAPIToken — токен Cloudflare с правом Zone.DNS Edit (и
	// Zone.Zone Read, если зона ищется по имени). Это секрет: он не
	// попадает ни в журнал, ни в сообщения об ошибках.
	CloudflareAPIToken string

	// CloudflareZoneID — необязательный идентификатор зоны. Если пуст, зона
	// ищется по имени домена через API.
	CloudflareZoneID string

	// DNSWait — сколько ждать после появления TXT-записи: DNS расходится не
	// мгновенно. Ноль означает значение по умолчанию.
	DNSWait time.Duration

	// HTTPAddr — адрес проверки HTTP-01. Пустая строка означает «:80».
	HTTPAddr string

	// Logger — куда писать о выпуске.
	Logger *slog.Logger
}

// defaultDNSWait — пауза после заведения TXT-записи. Значение рассчитано на
// хук, который сам не ждёт распространения записи.
const defaultDNSWait = 30 * time.Second

// renewRetryMin и renewRetryMax — как часто повторять выпуск после неудачи:
// сначала раз в минуту, потом реже, чтобы не долбить ACME-сервер.
const (
	renewRetryMin = time.Minute
	renewRetryMax = time.Hour
)

// dnsOperationTimeout — предел на одну операцию DNS-провайдера. Без него
// зависший провайдер держал бы выпуск (а с ним и очистку записи) сколько угодно.
const dnsOperationTimeout = 2 * time.Minute

// normalized дополняет незаданное значениями по умолчанию.
func (c ACMEConfig) normalized() ACMEConfig {
	if c.Mode == "" {
		c.Mode = ModeHTTP
	}
	if c.Mode == ModeDNS && c.DNSProvider == "" {
		c.DNSProvider = DNSProviderHook
	}
	if c.DirectoryURL == "" {
		c.DirectoryURL = acme.LetsEncryptURL
	}
	if c.HTTPAddr == "" {
		c.HTTPAddr = ":80"
	}
	if c.DNSWait <= 0 {
		c.DNSWait = defaultDNSWait
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}

	return c
}

// Validate проверяет настройки, не выходя в сеть.
func (c ACMEConfig) Validate() error {
	switch {
	case len(c.Names) == 0:
		return errors.New("не заданы имена сертификата")
	case c.Email == "":
		return errors.New("не задан e-mail для ACME")
	case c.AccountKeyPath == "":
		return errors.New("не задан файл ключа учётной записи ACME")
	case c.CertPath == "" || c.KeyPath == "":
		return errors.New("не заданы пути к сертификату и ключу")
	}

	for _, name := range c.Names {
		if strings.TrimSpace(name) == "" {
			return errors.New("имя сертификата пустое")
		}
	}

	switch c.Mode {
	case ModeHTTP:
		if c.DNSProvider != "" || c.DNSHook != "" || c.CloudflareAPIToken != "" || c.CloudflareZoneID != "" {
			return errors.New("для проверки http настройки DNS не нужны")
		}
	case ModeDNS:
		provider := c.DNSProvider
		if provider == "" {
			provider = DNSProviderHook
		}

		switch provider {
		case DNSProviderHook:
			if c.DNSHook == "" {
				return errors.New("для DNS-01 с хуком нужна программа: без неё запись TXT заводить некому")
			}
			if c.CloudflareAPIToken != "" || c.CloudflareZoneID != "" {
				return errors.New("для DNS-01 с хуком не нужны настройки Cloudflare")
			}
		case DNSProviderCloudflare:
			if c.CloudflareAPIToken == "" {
				return errors.New("для DNS-01 Cloudflare нужен API-токен")
			}
			if c.DNSHook != "" {
				return errors.New("для DNS-01 Cloudflare не нужен хук")
			}
		default:
			return fmt.Errorf("неизвестный DNS-провайдер: %q", provider)
		}
	default:
		return fmt.Errorf("неизвестный способ проверки ACME: %q", c.Mode)
	}

	if c.DirectoryURL == "" {
		// Пустой адрес означает Let's Encrypt: подставлять его здесь незачем,
		// это делает normalized перед выпуском.
		return nil
	}

	target, err := url.Parse(c.DirectoryURL)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return fmt.Errorf("адрес ACME-каталога должен быть вида https://хост: %q", c.DirectoryURL)
	}

	return nil
}

// Run выпускает сертификат и продлевает его, пока не отменят контекст.
//
// Пока сертификата нет, панель работает на временном: неудача выпуска не должна
// отнимать у администратора вход. Провал попадает в состояние держателя, и
// попытка повторяется с нарастающей паузой. onRenew вызывается после выпуска,
// чтобы ядро подхватило сертификат.
func Run(ctx context.Context, manager *Manager, config ACMEConfig, onRenew func()) {
	config = config.normalized()
	manager.SetMode(string(config.Mode))

	if err := config.Validate(); err != nil {
		config.Logger.Error("выпуск сертификата не настроен", "err", err)
		manager.Failed(err, time.Time{})

		return
	}

	config.Logger.Info("выпуск сертификата: слежу за сроком",
		"names", strings.Join(config.Names, ", "), "mode", string(config.Mode),
		"dns_provider", config.DNSProvider)

	backoff := renewRetryMin

	for {
		// Домен могли поменять из интерфейса: выпускать нужно на новые имена,
		// хотя настройки выпуска собраны при запуске.
		if names := manager.DesiredNames(); len(names) != 0 {
			config.Names = names
		}

		if !needsIssue(ctx, manager, config) {
			return
		}

		manager.Attempt(time.Now())

		cert, err := config.Obtain(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}

			config.Logger.Error("сертификат не выпущен", "err", err, "retry_in", backoff)
			manager.Failed(err, time.Now().Add(backoff))

			if !sleep(ctx, manager, backoff) {
				return
			}

			if backoff < renewRetryMax {
				backoff *= 2
				if backoff > renewRetryMax {
					backoff = renewRetryMax
				}
			}

			continue
		}

		manager.Set(cert)
		manager.Succeeded(RenewAt(cert))
		backoff = renewRetryMin

		config.Logger.Info("сертификат выпущен",
			"names", strings.Join(config.Names, ", "),
			"not_after", cert.Leaf.NotAfter)

		if onRenew != nil {
			onRenew()
		}
	}
}

// needsIssue ждёт, пока текущий сертификат не пора менять, и сообщает false,
// если ждать больше нечего (контекст отменён).
func needsIssue(ctx context.Context, manager *Manager, config ACMEConfig) bool {
	cert := manager.Current()

	if len(cert.Certificate) != 0 && !manager.Placeholder() {
		// Домен могли поменять из интерфейса: сертификат на прежние имена
		// покрывает не то, что теперь нужно, и ждать продления нельзя.
		if !covers(cert, config.Names) {
			return true
		}

		renewAt := RenewAt(cert)
		if time.Now().Before(renewAt) {
			wait := time.Until(renewAt)
			manager.Scheduled(time.Now().Add(wait))

			return sleep(ctx, manager, wait)
		}
	}

	return ctx.Err() == nil
}

// covers сообщает, что сертификат выписан на все нужные имена. Лишние имена не
// мешают: старый домен в сертификате — не причина выпускать новый.
func covers(cert tls.Certificate, names []string) bool {
	if cert.Leaf == nil {
		return false
	}

	present := make(map[string]struct{}, len(cert.Leaf.DNSNames))
	for _, name := range cert.Leaf.DNSNames {
		present[strings.ToLower(name)] = struct{}{}
	}

	for _, name := range names {
		if _, ok := present[strings.ToLower(name)]; !ok {
			return false
		}
	}

	return true
}

// sleep ждёт паузу, но просыпается раньше по просьбе повторить или по остановке.
// Возвращает false, если ждать больше нечего.
func sleep(ctx context.Context, manager *Manager, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-manager.wake:
		return true
	case <-timer.C:
		return true
	}
}

// Obtain выпускает сертификат и кладёт его в файлы. Возвращённый сертификат уже
// содержит разобранный лист.
func (c ACMEConfig) Obtain(ctx context.Context) (tls.Certificate, error) {
	c = c.normalized()

	accountKey, err := accountKey(c.AccountKeyPath)
	if err != nil {
		return tls.Certificate{}, err
	}

	client := &acme.Client{Key: accountKey, DirectoryURL: c.DirectoryURL}

	if _, err := client.Register(ctx, &acme.Account{Contact: []string{"mailto:" + c.Email}}, acme.AcceptTOS); err != nil &&
		!errors.Is(err, acme.ErrAccountAlreadyExists) {
		return tls.Certificate{}, fmt.Errorf("учётная запись ACME: %w", err)
	}

	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(c.Names...))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("заявка на сертификат: %w", err)
	}

	// Адрес заявки сохраняется отдельно: ответы ACME на опрос могут не содержать
	// его в заголовке Location, а знать его нужно до конца выпуска.
	orderURL := order.URI

	challenges, err := startChallenges(client, c)
	if err != nil {
		return tls.Certificate{}, err
	}
	defer challenges.Close()

	for _, authorizationURL := range order.AuthzURLs {
		if err := challenges.fulfill(ctx, authorizationURL); err != nil {
			return tls.Certificate{}, err
		}
	}

	order, err = client.WaitOrder(ctx, orderURL)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("заявка на сертификат: %w", err)
	}

	cert, err := finalize(ctx, client, order, orderURL, c.Names)
	if err != nil {
		return tls.Certificate{}, err
	}

	if err := Save(c.CertPath, c.KeyPath, cert); err != nil {
		return tls.Certificate{}, err
	}

	return cert, nil
}

// finalize отправляет запрос на выпуск и разбирает ответ.
func finalize(ctx context.Context, client *acme.Client, order *acme.Order, orderURL string, names []string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("ключ сертификата: %w", err)
	}

	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: names[0]},
		DNSNames: names,
	}, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("запрос на сертификат: %w", err)
	}

	var chain [][]byte
	if order.Status == acme.StatusValid {
		chain, err = client.FetchCert(ctx, order.CertURL, true)
	} else {
		chain, _, err = client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
		if err != nil {
			// Некоторые ACME-серверы не сообщают адрес заявки в ответе на
			// финализацию, и тогда CreateOrderCert теряет, что опрашивать.
			// Заявка при этом уже могла быть принята: проверяем её по известному
			// адресу и забираем сертификат, если он выпущен.
			chain, err = certificateAfterFinalize(ctx, client, orderURL, err)
		}
	}
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("выпуск сертификата: %w", err)
	}

	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("разбор выпущенного сертификата: %w", err)
	}

	return tls.Certificate{Certificate: chain, PrivateKey: key, Leaf: leaf}, nil
}

// certificateAfterFinalize проверяет заявку после отказа CreateOrderCert.
//
// Ответ на финализацию не всегда содержит адрес заявки, и тогда клиент ACME не
// знает, что опрашивать. Заявка при этом могла быть принята: если она стала
// действительной, сертификат забирается по её адресу, известному с момента
// создания.
func certificateAfterFinalize(ctx context.Context, client *acme.Client, orderURL string, finalizeErr error) ([][]byte, error) {
	if orderURL == "" {
		return nil, finalizeErr
	}

	order, err := client.WaitOrder(ctx, orderURL)
	if err != nil {
		return nil, errors.Join(finalizeErr, err)
	}
	if order.Status != acme.StatusValid || order.CertURL == "" {
		return nil, finalizeErr
	}

	chain, err := client.FetchCert(ctx, order.CertURL, true)
	if err != nil {
		return nil, errors.Join(finalizeErr, err)
	}

	return chain, nil
}

// challenger отвечает на проверки ACME одного режима.
type challenger interface {
	// fulfill проходят проверки всех авторизаций одной заявки.
	fulfill(ctx context.Context, authorizationURL string) error
	Close()
}

// startChallenges готовит ответы на проверки нужного режима.
func startChallenges(client *acme.Client, config ACMEConfig) (challenger, error) {
	if config.Mode == ModeDNS {
		provider, err := newDNSProvider(config)
		if err != nil {
			return nil, err
		}

		return &dnsChallenger{client: client, config: config, provider: provider}, nil
	}

	return newHTTPChallenger(client, config)
}

// httpChallenger отвечает на HTTP-01: кладёт ответ на порт 80 и держит его до
// конца выпуска.
type httpChallenger struct {
	client  *acme.Client
	server  *http.Server
	closing sync.Once

	mu        sync.RWMutex
	responses map[string]string
}

// newHTTPChallenger занимает порт заранее: занятый порт должен быть виден до
// обращения к ACME, а не выглядеть как провал проверки.
func newHTTPChallenger(client *acme.Client, config ACMEConfig) (*httpChallenger, error) {
	listener, err := net.Listen("tcp", config.HTTPAddr)
	if err != nil {
		return nil, fmt.Errorf("порт %s для проверки ACME: %w", config.HTTPAddr, err)
	}

	challenger := &httpChallenger{
		client:    client,
		responses: make(map[string]string),
		server: &http.Server{
			Handler:           nil,
			ReadHeaderTimeout: 10 * time.Second,
		},
	}
	challenger.server.Handler = challenger

	go func() {
		// Ошибка обслуживания уже не важна: проверка либо прошла, либо выпуск
		// сорвался по другой причине.
		_ = challenger.server.Serve(listener)
	}()

	return challenger, nil
}

// ServeHTTP отдаёт ответ проверки по её пути.
func (h *httpChallenger) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	response, ok := h.responses[r.URL.Path]
	h.mu.RUnlock()

	if !ok {
		http.NotFound(w, r)

		return
	}

	w.Header().Set("Content-Type", "text/plain")
	_, _ = io.WriteString(w, response)
}

// fulfill проходит одну авторизацию по HTTP-01.
func (h *httpChallenger) fulfill(ctx context.Context, authorizationURL string) error {
	authorization, err := h.client.GetAuthorization(ctx, authorizationURL)
	if err != nil {
		return fmt.Errorf("авторизация ACME: %w", err)
	}
	if authorization.Status == acme.StatusValid {
		return nil
	}

	challenge := pickChallenge(authorization.Challenges, "http-01")
	if challenge == nil {
		return fmt.Errorf("ACME не предложил проверку http-01 для %q", authorization.Identifier.Value)
	}

	response, err := h.client.HTTP01ChallengeResponse(challenge.Token)
	if err != nil {
		return fmt.Errorf("ответ на проверку http-01: %w", err)
	}

	h.mu.Lock()
	h.responses[h.client.HTTP01ChallengePath(challenge.Token)] = response
	h.mu.Unlock()

	return accept(ctx, h.client, authorization, challenge)
}

// Close отпускает порт проверки.
func (h *httpChallenger) Close() {
	h.closing.Do(func() {
		_ = h.server.Close()
	})
}

// dnsChallenger проходит проверку DNS-01, заказывая запись TXT выбранному
// провайдеру: встроенному или внешней программе.
type dnsChallenger struct {
	client   *acme.Client
	config   ACMEConfig
	provider dnsProvider
}

// fulfill заводит запись, ждёт её распространения, проходит авторизацию и
// убирает запись.
func (d *dnsChallenger) fulfill(ctx context.Context, authorizationURL string) error {
	authorization, err := d.client.GetAuthorization(ctx, authorizationURL)
	if err != nil {
		return fmt.Errorf("авторизация ACME: %w", err)
	}
	if authorization.Status == acme.StatusValid {
		return nil
	}

	challenge := pickChallenge(authorization.Challenges, "dns-01")
	if challenge == nil {
		return fmt.Errorf("ACME не предложил проверку dns-01 для %q", authorization.Identifier.Value)
	}

	value, err := d.client.DNS01ChallengeRecord(challenge.Token)
	if err != nil {
		return fmt.Errorf("значение записи dns-01: %w", err)
	}

	name := authorization.Identifier.Value

	if err := d.callProvider(ctx, "present", name, value); err != nil {
		return err
	}

	// Запись убирается в любом случае: оставленная TXT-запись — чужой мусор в
	// зоне, а не безобидный след.
	defer func() {
		if err := d.callProvider(context.WithoutCancel(ctx), "cleanup", name, value); err != nil {
			d.config.Logger.Warn("запись dns-01 не убрана",
				"name", name, "provider", d.config.DNSProvider, "err", err)
		}
	}()

	if !wait(ctx, d.config.DNSWait) {
		return ctx.Err()
	}

	return accept(ctx, d.client, authorization, challenge)
}

// callProvider выполняет одну операцию провайдера с пределом времени: зависший
// провайдер не должен держать выпуск и остановку панели.
func (d *dnsChallenger) callProvider(ctx context.Context, action, name, value string) error {
	timeout, cancel := context.WithTimeout(ctx, dnsOperationTimeout)
	defer cancel()

	switch action {
	case "present":
		return d.provider.Present(timeout, name, value)
	case "cleanup":
		return d.provider.Cleanup(timeout, name, value)
	default:
		return fmt.Errorf("неизвестное действие DNS-провайдера: %q", action)
	}
}

// Close у DNS-проверки нечего закрывать: записи убираются в fulfill.
func (d *dnsChallenger) Close() {}

// accept сообщает ACME о готовности ответа и дожидается результата проверки.
func accept(ctx context.Context, client *acme.Client, authorization *acme.Authorization, challenge *acme.Challenge) error {
	if _, err := client.Accept(ctx, challenge); err != nil {
		return fmt.Errorf("подтверждение проверки: %w", err)
	}

	if _, err := client.WaitAuthorization(ctx, authorization.URI); err != nil {
		return fmt.Errorf("проверка %q: %w", authorization.Identifier.Value, err)
	}

	return nil
}

// pickChallenge выбирает проверку нужного типа.
func pickChallenge(challenges []*acme.Challenge, kind string) *acme.Challenge {
	for _, challenge := range challenges {
		if challenge.Type == kind {
			return challenge
		}
	}

	return nil
}

// wait ждёт паузу или отмену контекста. Возвращает false при отмене.
func wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// runHook зовёт программу, которая заводит и убирает записи DNS. Ни оболочки, ни
// разбора её команд нет: администратор сам решает, чем управлять зоной.
func runHook(ctx context.Context, hook, action, name, value string) error {
	if hook == "" {
		return errors.New("не задан хук DNS")
	}

	output, err := exec.CommandContext(ctx, hook, action, name, value).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return fmt.Errorf("хук DNS (%s %s): %w", action, name, err)
		}

		return fmt.Errorf("хук DNS (%s %s): %w: %s", action, name, err, message)
	}

	return nil
}

// accountKey читает ключ учётной записи ACME, а если его ещё нет — заводит.
// Ключ переживает продления: с новым ключом ACME завёл бы вторую учётную запись.
func accountKey(path string) (*ecdsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		return parseAccountKey(raw)
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, fmt.Errorf("ключ учётной записи ACME: %w", err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ключ учётной записи ACME: %w", err)
	}

	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("ключ учётной записи ACME: %w", err)
	}

	encoded := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := writeFile(path, encoded); err != nil {
		return nil, fmt.Errorf("ключ учётной записи ACME: %w", err)
	}

	return key, nil
}

// parseAccountKey разбирает ключ учётной записи.
func parseAccountKey(raw []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("ключ учётной записи ACME: не PEM")
	}

	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ключ учётной записи ACME: %w", err)
	}

	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("ключ учётной записи ACME: неожиданный тип %T", parsed)
	}

	return key, nil
}

// AccountKeyPath подсказывает, где держать ключ учётной записи рядом с
// сертификатом.
func AccountKeyPath(certPath string) string {
	return filepath.Join(filepath.Dir(certPath), "acme-account.key")
}
