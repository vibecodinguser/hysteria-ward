package certs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeCloudflare — минимальный Cloudflare API для проверок: зона, TXT-записи и
// журнал запросов. Настоящий API в тестах недоступен, а подменять его целиком
// не нужно: панель использует только три метода.
type fakeCloudflare struct {
	mu      sync.Mutex
	zone    string
	records map[string]fakeDNSRecord
	nextID  int
	log     []string
}

type fakeDNSRecord struct {
	name    string
	content string
}

func newFakeCloudflare(zone string) *fakeCloudflare {
	return &fakeCloudflare{zone: zone, records: make(map[string]fakeDNSRecord)}
}

func (f *fakeCloudflare) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.log = append(f.log, r.Method+" "+r.URL.RequestURI())

	if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
		writeCloudflareError(w, http.StatusForbidden, "Authentication error")
		return
	}

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/client/v4/zones":
		name := r.URL.Query().Get("name")
		if name != f.zone {
			writeCloudflareJSON(w, map[string]any{"success": true, "result": []any{}})
			return
		}
		writeCloudflareJSON(w, map[string]any{
			"success": true,
			"result":  []any{map[string]any{"id": "zone-1", "name": f.zone}},
		})
	case r.Method == http.MethodPost && r.URL.Path == "/client/v4/zones/zone-1/dns_records":
		var request struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			Content string `json:"content"`
			TTL     int    `json:"ttl"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeCloudflareError(w, http.StatusBadRequest, "bad request")
			return
		}
		if request.Type != "TXT" {
			writeCloudflareError(w, http.StatusBadRequest, "expected TXT")
			return
		}
		if request.TTL != cloudflareTTL {
			writeCloudflareError(w, http.StatusBadRequest, "unexpected TTL")
			return
		}

		f.nextID++
		id := fmt.Sprintf("record-%d", f.nextID)
		f.records[id] = fakeDNSRecord{name: request.Name, content: request.Content}

		writeCloudflareJSON(w, map[string]any{"success": true, "result": map[string]any{"id": id}})
	case r.Method == http.MethodGet && r.URL.Path == "/client/v4/zones/zone-1/dns_records":
		name := r.URL.Query().Get("name")
		result := make([]any, 0)
		for id, record := range f.records {
			if name != "" && record.name != name {
				continue
			}
			result = append(result, map[string]any{
				"id": id, "type": "TXT", "name": record.name, "content": record.content,
			})
		}
		writeCloudflareJSON(w, map[string]any{"success": true, "result": result})
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/client/v4/zones/zone-1/dns_records/"):
		id := strings.TrimPrefix(r.URL.Path, "/client/v4/zones/zone-1/dns_records/")
		if _, ok := f.records[id]; !ok {
			writeCloudflareError(w, http.StatusNotFound, "record not found")
			return
		}
		delete(f.records, id)
		writeCloudflareJSON(w, map[string]any{"success": true, "result": map[string]any{"id": id}})
	default:
		writeCloudflareError(w, http.StatusNotFound, "not found")
	}
}

func writeCloudflareJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeCloudflareError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": false,
		"errors":  []any{map[string]any{"code": 10000, "message": message}},
	})
}

// TestCloudflareProviderPresentCleanup — запись создаётся в найденной зоне и
// удаляется по сохранённому идентификатору. Токен уходит только в заголовок.
func TestCloudflareProviderPresentCleanup(t *testing.T) {
	fake := newFakeCloudflare("example.com")
	server := httptest.NewServer(fake)
	defer server.Close()

	provider := newCloudflareProviderAt("secret-token", "", server.URL+"/client/v4")
	fqdn := "_acme-challenge.panel.example.com"
	value := "challenge-value"

	if err := provider.Present(context.Background(), "panel.example.com", value); err != nil {
		t.Fatalf("Present() = %v", err)
	}

	fake.mu.Lock()
	if len(fake.records) != 1 {
		t.Fatalf("записей после Present = %d, ожидалась одна", len(fake.records))
	}
	for _, record := range fake.records {
		if record.name != fqdn || record.content != value {
			t.Errorf("запись = %+v, ожидались имя %q и значение %q", record, fqdn, value)
		}
	}
	fake.mu.Unlock()

	if err := provider.Cleanup(context.Background(), "panel.example.com", value); err != nil {
		t.Fatalf("Cleanup() = %v", err)
	}

	fake.mu.Lock()
	if len(fake.records) != 0 {
		t.Errorf("записей после Cleanup = %d, ожидалось ноль", len(fake.records))
	}
	fake.mu.Unlock()
}

// TestCloudflareProviderCleanupFindsRecord — если карта идентификаторов
// потеряна, cleanup всё равно находит запись по имени и значению.
func TestCloudflareProviderCleanupFindsRecord(t *testing.T) {
	fake := newFakeCloudflare("example.com")
	fake.records["record-1"] = fakeDNSRecord{
		name:    "_acme-challenge.example.com",
		content: "challenge-value",
	}
	server := httptest.NewServer(fake)
	defer server.Close()

	provider := newCloudflareProviderAt("secret-token", "", server.URL+"/client/v4")
	if err := provider.Cleanup(context.Background(), "example.com", "challenge-value"); err != nil {
		t.Fatalf("Cleanup() = %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.records) != 0 {
		t.Errorf("записей после Cleanup = %d, ожидалось ноль", len(fake.records))
	}
}

// TestCloudflareProviderCleanupMissingRecord — если запись уже удалена, cleanup
// не считает это ошибкой: повторная очистка должна быть безопасной.
func TestCloudflareProviderCleanupMissingRecord(t *testing.T) {
	fake := newFakeCloudflare("example.com")
	server := httptest.NewServer(fake)
	defer server.Close()

	provider := newCloudflareProviderAt("secret-token", "", server.URL+"/client/v4")
	provider.records[recordKey("_acme-challenge.example.com", "challenge-value")] = "record-missing"

	if err := provider.Cleanup(context.Background(), "example.com", "challenge-value"); err != nil {
		t.Fatalf("Cleanup() = %v", err)
	}
}

// TestCloudflareProviderErrorHasNoToken — ошибка API объясняет причину, но не
// раскрывает токен: текст ошибки попадает в журнал и интерфейс.
func TestCloudflareProviderErrorHasNoToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeCloudflareError(w, http.StatusForbidden, "Authentication error")
	}))
	defer server.Close()

	provider := newCloudflareProviderAt("secret-token", "", server.URL+"/client/v4")
	err := provider.Present(context.Background(), "example.com", "challenge-value")
	if err == nil {
		t.Fatal("Present() = nil, ожидалась ошибка")
	}
	if !strings.Contains(err.Error(), "Authentication error") {
		t.Errorf("в ошибке нет причины от API: %v", err)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Errorf("в ошибке оказался токен: %v", err)
	}
}

// TestCloudflareProviderUsesConfiguredZone — явный zone ID избавляет от поиска
// зоны: это нужно токенам, которым запрещено перечислять зоны.
func TestCloudflareProviderUsesConfiguredZone(t *testing.T) {
	fake := newFakeCloudflare("example.com")
	server := httptest.NewServer(fake)
	defer server.Close()

	provider := newCloudflareProviderAt("secret-token", "zone-1", server.URL+"/client/v4")
	if err := provider.Present(context.Background(), "example.com", "challenge-value"); err != nil {
		t.Fatalf("Present() = %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, request := range fake.log {
		if strings.HasPrefix(request, "GET /client/v4/zones?") {
			t.Errorf("при заданном zone ID был поиск зоны: %s", request)
		}
	}
}
