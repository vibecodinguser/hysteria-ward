package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/api"
)

// trafficJSON — числа трафика в ответе панели.
type trafficJSON struct {
	Rx     uint64  `json:"rx"`
	Tx     uint64  `json:"tx"`
	RxRate float64 `json:"rx_rate"`
	TxRate float64 `json:"tx_rate"`
}

// trafficUserJSON — пользователь с текущим трафиком.
type trafficUserJSON struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Enabled bool    `json:"enabled"`
	Online  bool    `json:"online"`
	Rx      uint64  `json:"rx"`
	Tx      uint64  `json:"tx"`
	RxRate  float64 `json:"rx_rate"`
	TxRate  float64 `json:"tx_rate"`
}

// metricsJSON — снимок текущих значений.
type metricsJSON struct {
	At    time.Time         `json:"at"`
	Users []trafficUserJSON `json:"users"`
	Total trafficJSON       `json:"total"`
}

// historyJSON — ряд окон истории.
type historyJSON struct {
	Range  string `json:"range"`
	Step   int    `json:"step_seconds"`
	Points []struct {
		At time.Time `json:"at"`
		Rx uint64    `json:"rx"`
		Tx uint64    `json:"tx"`
	} `json:"points"`
}

// TestMetricsRequireLogin — трафик виден только вошедшему: по нему видно, кто и
// сколько гоняет через сервер.
func TestMetricsRequireLogin(t *testing.T) {
	s := server(t)

	for _, path := range []string{"/api/metrics", "/api/metrics/history?range=1h", "/api/events"} {
		resp := do(t, s, http.MethodGet, path, "", nil, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: код ответа = %d, ожидался 401", path, resp.StatusCode)
		}
	}
}

// TestMetricsReportTraffic — снимок показывает накопленное по каждому
// пользователю и общее, а пользователь без трафика — с нулями, а не пропадает.
func TestMetricsReportTraffic(t *testing.T) {
	opts := options(t)
	s := start(t, opts)
	cookie, csrf := enter(t, s, "admin", "admin")

	alice := createUser(t, s, cookie, csrf, "alice")
	createUser(t, s, cookie, csrf, "bob")

	// Ядро прибавляет трафик вызовами Add, часы сборщика снимают снимок.
	opts.Metrics.Add(alice.ID, 1_000, 2_000)
	opts.Metrics.Sample()

	resp := do(t, s, http.MethodGet, "/api/metrics", "", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("снимок: код %d, ожидался 200", resp.StatusCode)
	}

	answer := decode[metricsJSON](t, resp)
	if answer.Total.Tx != 1_000 || answer.Total.Rx != 2_000 {
		t.Errorf("общий трафик = %d/%d, ожидался 1000/2000", answer.Total.Tx, answer.Total.Rx)
	}
	if len(answer.Users) != 2 {
		t.Fatalf("пользователей %d, ожидалось 2", len(answer.Users))
	}

	byName := map[string]trafficUserJSON{}
	for _, user := range answer.Users {
		byName[user.Name] = user
	}

	if got := byName["alice"]; got.Tx != 1_000 || got.Rx != 2_000 {
		t.Errorf("трафик alice = %d/%d, ожидался 1000/2000", got.Tx, got.Rx)
	}
	if got := byName["bob"]; got.Tx != 0 || got.Rx != 0 {
		t.Errorf("трафик bob = %d/%d, ожидался нулевой", got.Tx, got.Rx)
	}
}

// TestMetricsHistoryReturnsPoints — история отдаётся окнами своего разрешения, а
// свежие секунды не ждут записи в базу.
func TestMetricsHistoryReturnsPoints(t *testing.T) {
	opts := options(t)
	s := start(t, opts)
	cookie, _ := enter(t, s, "admin", "admin")

	opts.Metrics.Add("id-1", 1_500, 2_500)
	opts.Metrics.Sample()

	resp := do(t, s, http.MethodGet, "/api/metrics/history?range=1h", "", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("история: код %d, ожидался 200", resp.StatusCode)
	}

	answer := decode[historyJSON](t, resp)
	if answer.Range != "1h" {
		t.Errorf("отрезок = %q, ожидался 1h", answer.Range)
	}
	if answer.Step != 5 {
		t.Errorf("шаг = %d с, ожидалось 5", answer.Step)
	}
	if len(answer.Points) == 0 {
		t.Fatal("ряд пуст")
	}

	var tx, rx uint64
	for _, point := range answer.Points {
		tx += point.Tx
		rx += point.Rx
	}
	if tx != 1_500 || rx != 2_500 {
		t.Errorf("ряд = %d/%d, ожидалось 1500/2500", tx, rx)
	}
}

// TestMetricsHistoryRejectsUnknownRange — интерфейс просит только четыре
// отрезка; остальное — ошибка, а не пустой график.
func TestMetricsHistoryRejectsUnknownRange(t *testing.T) {
	s := server(t)
	cookie, _ := enter(t, s, "admin", "admin")

	for _, name := range []string{"", "2h", "week"} {
		path := "/api/metrics/history"
		if name != "" {
			path += "?range=" + name
		}

		resp := do(t, s, http.MethodGet, path, "", cookie, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("отрезок %q: код %d, ожидался 400", name, resp.StatusCode)
		}
	}
}

// TestMetricsStream — интерфейс получает живые значения потоком: первый снимок
// приходит сразу, без опроса.
func TestMetricsStream(t *testing.T) {
	opts := options(t)
	s := start(t, opts)
	cookie, _ := enter(t, s, "admin", "admin")

	opts.Metrics.Add("id-1", 400, 500)
	opts.Metrics.Sample()

	req, err := http.NewRequest(http.MethodGet, "https://"+s.Addr().String()+"/api/events", nil)
	if err != nil {
		t.Fatalf("запрос потока: %v", err)
	}
	req.AddCookie(cookie)

	// Свой клиент без общего предела: поток живёт дольше обычного запроса.
	stream := &http.Client{Transport: client().Transport}
	resp, err := stream.Do(req)
	if err != nil {
		t.Fatalf("подключение к потоку: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код потока = %d, ожидался 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("тип потока = %q, ожидался text/event-stream", got)
	}

	// Первый снимок сервер отправляет сразу после подключения.
	started := time.Now()

	reader := bufio.NewReader(resp.Body)
	var data string

	for data == "" {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("чтение потока: %v", err)
		}

		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		}
	}

	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("первый снимок ждали %v, ожидалось не больше 2 с", elapsed)
	}

	var answer metricsJSON
	if err := json.Unmarshal([]byte(data), &answer); err != nil {
		t.Fatalf("снимок из потока не разобран: %v (%q)", err, data)
	}
	if answer.Total.Tx != 400 || answer.Total.Rx != 500 {
		t.Errorf("трафик в потоке = %d/%d, ожидался 400/500", answer.Total.Tx, answer.Total.Rx)
	}
}

// TestMetricsStreamDoesNotHoldStop — поток метрик не задерживает остановку
// панели: без её сигнала Shutdown ждал бы поток до конца соединения, а браузер
// держит его открытым сколько угодно.
func TestMetricsStreamDoesNotHoldStop(t *testing.T) {
	s, err := api.New(options(t))
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)

	go func() { stopped <- s.Run(ctx) }()

	cookie, _ := enter(t, s, "admin", "admin")

	req, err := http.NewRequest(http.MethodGet, "https://"+s.Addr().String()+"/api/events", nil)
	if err != nil {
		t.Fatalf("запрос потока: %v", err)
	}
	req.AddCookie(cookie)

	stream := &http.Client{Transport: client().Transport}
	resp, err := stream.Do(req)
	if err != nil {
		t.Fatalf("подключение к потоку: %v", err)
	}
	defer resp.Body.Close()

	// Первый снимок означает, что поток действительно начался.
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("чтение потока: %v", err)
		}

		if strings.HasPrefix(line, "data: ") {
			break
		}
	}

	started := time.Now()
	cancel()

	select {
	case <-stopped:
	case <-time.After(4 * time.Second):
		t.Fatal("остановка панели ждала поток метрик")
	}

	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("остановка заняла %v, ожидалось меньше 3 с", elapsed)
	}
}

// TestNewRequiresMetrics — без учёта трафика обзор показывал бы пустоту вместо
// того, что через сервер прошло.
func TestNewRequiresMetrics(t *testing.T) {
	opts := options(t)
	opts.Metrics = nil

	if _, err := api.New(opts); err == nil {
		t.Fatal("New() без учёта трафика прошёл, ожидалась ошибка")
	}
}
