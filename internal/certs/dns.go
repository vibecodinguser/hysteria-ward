package certs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// dnsProvider заводит и убирает TXT-запись для проверки DNS-01. Имя приходит
// как домен из авторизации ACME; провайдер сам решает, в какой зоне создать
// _acme-challenge.<домен>.
type dnsProvider interface {
	Present(ctx context.Context, domain, value string) error
	Cleanup(ctx context.Context, domain, value string) error
}

// newDNSProvider собирает провайдера по настройкам. Проверка конфигурации уже
// произошла в Validate, здесь остаются только понятные ошибки на случай прямого
// вызова.
func newDNSProvider(config ACMEConfig) (dnsProvider, error) {
	provider := config.DNSProvider
	if provider == "" {
		provider = DNSProviderHook
	}

	switch provider {
	case DNSProviderHook:
		if config.DNSHook == "" {
			return nil, errors.New("для DNS-01 с хуком не задана программа")
		}

		return &hookDNSProvider{path: config.DNSHook}, nil
	case DNSProviderCloudflare:
		if config.CloudflareAPIToken == "" {
			return nil, errors.New("для DNS-01 Cloudflare не задан API-токен")
		}

		return newCloudflareProvider(config.CloudflareAPIToken, config.CloudflareZoneID), nil
	default:
		return nil, fmt.Errorf("неизвестный DNS-провайдер: %q", provider)
	}
}

// hookDNSProvider передаёт операции внешней программе. Формат вызова остался
// прежним: hook present <имя> <значение> и hook cleanup <имя> <значение>.
type hookDNSProvider struct {
	path string
}

func (h *hookDNSProvider) Present(ctx context.Context, domain, value string) error {
	return runHook(ctx, h.path, "present", domain, value)
}

func (h *hookDNSProvider) Cleanup(ctx context.Context, domain, value string) error {
	return runHook(ctx, h.path, "cleanup", domain, value)
}

const (
	// cloudflareAPIBase — версия и корень API Cloudflare. Вынесено в константу,
	// чтобы тесты могли подменить сервер, не меняя боевой адрес.
	cloudflareAPIBase = "https://api.cloudflare.com/client/v4"

	// cloudflareHTTPTimeout ограничивает один запрос к API. Общий предел
	// операции задаёт dnsChallenger.
	cloudflareHTTPTimeout = 20 * time.Second

	// cloudflareTTL — короткий TTL для проверочной записи. Cloudflare не
	// принимает TTL меньше 60 секунд.
	cloudflareTTL = 120
)

// cloudflareProvider заводит TXT-записи через Cloudflare API. SDK не нужен:
// используются только два метода API, а лишняя зависимость противоречит
// ADR-0018.
type cloudflareProvider struct {
	token   string
	zoneID  string
	baseURL string
	client  *http.Client

	mu sync.Mutex
	// records хранит идентификаторы созданных записей, чтобы cleanup удалял
	// именно свою запись, а не первую попавшуюся с тем же именем.
	records map[string]string
	// zones кэширует найденные зоны: сертификат содержит два имени из одной
	// зоны, и второй поиск уже не нужен.
	zones map[string]string
}

func newCloudflareProvider(token, zoneID string) *cloudflareProvider {
	return newCloudflareProviderAt(token, zoneID, cloudflareAPIBase)
}

// newCloudflareProviderAt нужен тестам: адрес API подменяется на httptest.
func newCloudflareProviderAt(token, zoneID, baseURL string) *cloudflareProvider {
	return &cloudflareProvider{
		token:   strings.TrimSpace(token),
		zoneID:  strings.TrimSpace(zoneID),
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: cloudflareHTTPTimeout},
		records: make(map[string]string),
		zones:   make(map[string]string),
	}
}

// Present создаёт TXT-запись и запоминает её идентификатор для cleanup.
func (p *cloudflareProvider) Present(ctx context.Context, domain, value string) error {
	fqdn := challengeName(domain)
	zoneID, err := p.zone(ctx, fqdn)
	if err != nil {
		return err
	}

	payload := struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		Content string `json:"content"`
		TTL     int    `json:"ttl"`
	}{
		Type:    "TXT",
		Name:    fqdn,
		Content: value,
		TTL:     cloudflareTTL,
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := p.request(ctx, http.MethodPost, "/zones/"+url.PathEscape(zoneID)+"/dns_records", payload, &result); err != nil {
		return fmt.Errorf("cloudflare: создать запись %s: %w", fqdn, err)
	}
	if result.ID == "" {
		return fmt.Errorf("cloudflare: создать запись %s: API не вернул идентификатор", fqdn)
	}

	p.mu.Lock()
	p.records[recordKey(fqdn, value)] = result.ID
	p.mu.Unlock()

	return nil
}

// Cleanup удаляет созданную запись. Если идентификатор потерян, запись ищется
// по имени и значению: cleanup не должен оставлять мусор в зоне.
func (p *cloudflareProvider) Cleanup(ctx context.Context, domain, value string) error {
	fqdn := challengeName(domain)
	zoneID, err := p.zone(ctx, fqdn)
	if err != nil {
		return err
	}

	key := recordKey(fqdn, value)

	p.mu.Lock()
	recordID := p.records[key]
	p.mu.Unlock()

	if recordID != "" {
		if err := p.delete(ctx, zoneID, recordID); err != nil {
			return fmt.Errorf("cloudflare: удалить запись %s: %w", fqdn, err)
		}

		p.mu.Lock()
		delete(p.records, key)
		p.mu.Unlock()

		return nil
	}

	records, err := p.findRecords(ctx, zoneID, fqdn, value)
	if err != nil {
		return fmt.Errorf("cloudflare: найти запись %s для удаления: %w", fqdn, err)
	}

	for _, recordID := range records {
		if err := p.delete(ctx, zoneID, recordID); err != nil {
			return fmt.Errorf("cloudflare: удалить запись %s: %w", fqdn, err)
		}
	}

	return nil
}

// zone находит зону, которой принадлежит имя. Если zone ID задан явно, поиск не
// нужен. Иначе проверяются суффиксы имени от самого длинного к короткому: так
// находится и example.com, и отдельная зона panel.example.com.
func (p *cloudflareProvider) zone(ctx context.Context, fqdn string) (string, error) {
	if p.zoneID != "" {
		return p.zoneID, nil
	}

	name := strings.TrimPrefix(strings.TrimSuffix(fqdn, "."), "_acme-challenge.")
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("cloudflare: не удалось определить зону для %q", fqdn)
	}

	for i := 0; i <= len(labels)-2; i++ {
		candidate := strings.Join(labels[i:], ".")

		p.mu.Lock()
		cached := p.zones[candidate]
		p.mu.Unlock()
		if cached != "" {
			return cached, nil
		}

		zoneID, err := p.lookupZone(ctx, candidate)
		if err != nil {
			return "", err
		}
		if zoneID == "" {
			continue
		}

		p.mu.Lock()
		p.zones[candidate] = zoneID
		p.mu.Unlock()

		return zoneID, nil
	}

	return "", fmt.Errorf("cloudflare: зона для %q не найдена; проверьте домен и права токена", fqdn)
}

// lookupZone спрашивает у Cloudflare зону по точному имени. Пустой ответ — не
// ошибка: вызывающий попробует более короткий суффикс.
func (p *cloudflareProvider) lookupZone(ctx context.Context, name string) (string, error) {
	path := "/zones?name=" + url.QueryEscape(name)

	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := p.request(ctx, http.MethodGet, path, nil, &zones); err != nil {
		return "", fmt.Errorf("cloudflare: найти зону %s: %w; для поиска зоны нужен Zone.Zone Read или явный zone ID", name, err)
	}

	for _, zone := range zones {
		if strings.EqualFold(zone.Name, name) && zone.ID != "" {
			return zone.ID, nil
		}
	}

	return "", nil
}

// findRecords ищет TXT-записи по имени и значению. Это запасной путь для
// cleanup, если карта идентификаторов потеряна.
func (p *cloudflareProvider) findRecords(ctx context.Context, zoneID, fqdn, value string) ([]string, error) {
	path := "/zones/" + url.PathEscape(zoneID) + "/dns_records?type=TXT&name=" + url.QueryEscape(fqdn)

	var records []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if err := p.request(ctx, http.MethodGet, path, nil, &records); err != nil {
		return nil, err
	}

	var ids []string
	for _, record := range records {
		if record.ID == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSuffix(record.Name, "."), strings.TrimSuffix(fqdn, ".")) &&
			record.Content == value {
			ids = append(ids, record.ID)
		}
	}

	return ids, nil
}

// delete удаляет запись. 404 означает, что записи уже нет: для cleanup это
// успех, а не повод сорвать выпуск.
func (p *cloudflareProvider) delete(ctx context.Context, zoneID, recordID string) error {
	err := p.request(ctx, http.MethodDelete, "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(recordID), nil, nil)
	if err == nil {
		return nil
	}

	var apiErr *cloudflareAPIError
	if errors.As(err, &apiErr) && apiErr.status == http.StatusNotFound {
		return nil
	}

	return err
}

// request выполняет запрос к API и разбирает конверт Cloudflare. Токен здесь
// только подставляется в заголовок: ни в ошибку, ни в журнал он не попадает.
func (p *cloudflareProvider) request(ctx context.Context, method, path string, payload, result any) error {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("подготовить запрос: %w", err)
		}
		body = bytes.NewReader(raw)
	}

	request, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("подготовить запрос: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+p.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	response, err := p.client.Do(request)
	if err != nil {
		return fmt.Errorf("запрос к API: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("прочитать ответ API: %w", err)
	}

	var envelope struct {
		Success bool `json:"success"`
		Errors  []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
		Result json.RawMessage `json:"result"`
	}

	if err := json.Unmarshal(raw, &envelope); err != nil {
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return &cloudflareAPIError{status: response.StatusCode, message: http.StatusText(response.StatusCode)}
		}

		return fmt.Errorf("разобрать ответ API: %w", err)
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.Success {
		return &cloudflareAPIError{status: response.StatusCode, message: cloudflareMessages(envelope.Errors)}
	}

	if result != nil && len(envelope.Result) != 0 && string(envelope.Result) != "null" {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return fmt.Errorf("разобрать результат API: %w", err)
		}
	}

	return nil
}

// cloudflareMessages собирает сообщения об ошибках API в одну строку.
func cloudflareMessages(apiErrors []struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}) string {
	if len(apiErrors) == 0 {
		return "API вернул ошибку без описания"
	}

	parts := make([]string, 0, len(apiErrors))
	for _, item := range apiErrors {
		if item.Message == "" {
			parts = append(parts, fmt.Sprintf("код %d", item.Code))
			continue
		}
		parts = append(parts, item.Message)
	}

	return strings.Join(parts, "; ")
}

// cloudflareAPIError — ошибка API с кодом HTTP. Код нужен cleanup, чтобы
// отличить уже удалённую запись от настоящей ошибки.
type cloudflareAPIError struct {
	status  int
	message string
}

func (e *cloudflareAPIError) Error() string {
	if e.status == http.StatusUnauthorized || e.status == http.StatusForbidden {
		return fmt.Sprintf("HTTP %d, доступ запрещён: %s; проверьте право токена Zone.DNS Edit", e.status, e.message)
	}

	return fmt.Sprintf("HTTP %d: %s", e.status, e.message)
}

// challengeName превращает домен из авторизации ACME в имя TXT-записи. Если
// провайдеру уже передали полное имя, второй префикс не добавляется.
func challengeName(domain string) string {
	domain = strings.TrimSuffix(strings.TrimSpace(domain), ".")
	if strings.HasPrefix(domain, "_acme-challenge.") {
		return domain
	}

	return "_acme-challenge." + domain
}

func recordKey(fqdn, value string) string {
	return strings.ToLower(strings.TrimSuffix(fqdn, ".")) + "\x00" + value
}
