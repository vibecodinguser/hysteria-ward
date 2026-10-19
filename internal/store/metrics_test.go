package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// metricsBase — начало окна, от которого считаются времена в тестах истории.
var metricsBase = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

// TestMetricSamplesFiltersWindowAndRange — выборка отдаёт окна одного разрешения
// за запрошенный отрезок: окна разных разрешений не смешиваются, а границы
// отрезка не захватывают соседей.
func TestMetricSamplesFiltersWindowAndRange(t *testing.T) {
	s := openStore(t)

	samples := []store.MetricSample{
		{UserID: "a", Window: 5 * time.Second, Start: metricsBase, Rx: 10, Tx: 20},
		{UserID: "b", Window: 5 * time.Second, Start: metricsBase, Rx: 1, Tx: 2},
		{UserID: "a", Window: 5 * time.Second, Start: metricsBase.Add(5 * time.Second), Rx: 30, Tx: 40},
		{UserID: "a", Window: time.Minute, Start: metricsBase, Rx: 100, Tx: 200},
	}
	if err := s.SaveMetrics(samples, nil); err != nil {
		t.Fatalf("SaveMetrics() = %v", err)
	}

	// Всё, что началось в первую пятисекундную границу: два пользователя.
	found, err := s.MetricSamples(5*time.Second, metricsBase, metricsBase.Add(5*time.Second))
	if err != nil {
		t.Fatalf("MetricSamples() = %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("окон %d, ожидалось 2: %+v", len(found), found)
	}

	sum := map[string]store.MetricSample{}
	for _, sample := range found {
		sum[sample.UserID] = sample
	}
	if got := sum["a"]; got.Rx != 10 || got.Tx != 20 || !got.Start.Equal(metricsBase) {
		t.Errorf("окно a = %+v, ожидалось 10/20 от %v", got, metricsBase)
	}
	if got := sum["b"]; got.Rx != 1 || got.Tx != 2 {
		t.Errorf("окно b = %+v, ожидалось 1/2", got)
	}

	// Другое разрешение — другие окна.
	perMinute, err := s.MetricSamples(time.Minute, metricsBase, metricsBase.Add(time.Minute))
	if err != nil {
		t.Fatalf("MetricSamples() = %v", err)
	}
	if len(perMinute) != 1 || perMinute[0].Rx != 100 {
		t.Fatalf("минутные окна = %+v, ожидалось одно на 100", perMinute)
	}

	// Пустой отрезок — пусто.
	empty, err := s.MetricSamples(5*time.Second, metricsBase.Add(10*time.Second), metricsBase.Add(time.Minute))
	if err != nil {
		t.Fatalf("MetricSamples() = %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("окон %d, ожидалось 0", len(empty))
	}
}

// TestSaveMetricsOverwritesWindow — повторная запись окна заменяет прежнее
// значение, а не складывается с ним: падение между записями не должно
// задваивать трафик.
func TestSaveMetricsOverwritesWindow(t *testing.T) {
	s := openStore(t)

	write := func(rx uint64) {
		t.Helper()

		sample := store.MetricSample{UserID: "a", Window: 5 * time.Second, Start: metricsBase, Rx: rx, Tx: rx}
		if err := s.SaveMetrics([]store.MetricSample{sample}, nil); err != nil {
			t.Fatalf("SaveMetrics() = %v", err)
		}
	}

	write(10)
	write(40)

	found, err := s.MetricSamples(5*time.Second, metricsBase, metricsBase.Add(5*time.Second))
	if err != nil {
		t.Fatalf("MetricSamples() = %v", err)
	}
	if len(found) != 1 || found[0].Rx != 40 || found[0].Tx != 40 {
		t.Fatalf("окно = %+v, ожидалось 40/40", found)
	}
}

// TestMetricTotalsSurviveReopen — накопленные значения переживают перезапуск
// панели: история не начинается заново с закрытием базы.
func TestMetricTotalsSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hysteria-ward.db")

	first, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	totals := map[string]store.MetricTotals{"a": {Rx: 1_000, Tx: 2_000}}
	if err := first.SaveMetrics(nil, totals); err != nil {
		t.Fatalf("SaveMetrics() = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	second, err := store.Open(path)
	if err != nil {
		t.Fatalf("повторное Open() = %v", err)
	}
	defer second.Close()

	restored, err := second.MetricTotals()
	if err != nil {
		t.Fatalf("MetricTotals() = %v", err)
	}
	if got := restored["a"]; got.Rx != 1_000 || got.Tx != 2_000 {
		t.Errorf("итог = %+v, ожидался 1000/2000", got)
	}
}

// TestDeleteMetricsBefore — старые окна выбрасываются, а свежие и окна другого
// разрешения остаются.
func TestDeleteMetricsBefore(t *testing.T) {
	s := openStore(t)

	samples := []store.MetricSample{
		{UserID: "a", Window: 5 * time.Second, Start: metricsBase, Rx: 1},
		{UserID: "a", Window: 5 * time.Second, Start: metricsBase.Add(5 * time.Second), Rx: 2},
		{UserID: "a", Window: 5 * time.Second, Start: metricsBase.Add(10 * time.Second), Rx: 3},
		{UserID: "a", Window: time.Minute, Start: metricsBase, Rx: 4},
	}
	if err := s.SaveMetrics(samples, nil); err != nil {
		t.Fatalf("SaveMetrics() = %v", err)
	}

	if err := s.DeleteMetricsBefore(5*time.Second, metricsBase.Add(10*time.Second)); err != nil {
		t.Fatalf("DeleteMetricsBefore() = %v", err)
	}

	fresh, err := s.MetricSamples(5*time.Second, metricsBase, metricsBase.Add(time.Minute))
	if err != nil {
		t.Fatalf("MetricSamples() = %v", err)
	}
	if len(fresh) != 1 || fresh[0].Rx != 3 {
		t.Fatalf("пятисекундные окна = %+v, ожидалось одно свежее", fresh)
	}

	// Минутное окно того же времени не тронуто: у него своя граница хранения.
	perMinute, err := s.MetricSamples(time.Minute, metricsBase, metricsBase.Add(time.Minute))
	if err != nil {
		t.Fatalf("MetricSamples() = %v", err)
	}
	if len(perMinute) != 1 || perMinute[0].Rx != 4 {
		t.Fatalf("минутные окна = %+v, ожидалось одно на 4", perMinute)
	}
}

// TestSaveMetricsRejectsSampleWithoutUser — окно без пользователя испортило бы
// ключ и смешалось бы с чужими: такое лучше отвергнуть.
func TestSaveMetricsRejectsSampleWithoutUser(t *testing.T) {
	s := openStore(t)

	sample := store.MetricSample{Window: 5 * time.Second, Start: metricsBase, Rx: 1}
	if err := s.SaveMetrics([]store.MetricSample{sample}, nil); err == nil {
		t.Fatal("окно без пользователя записано, ожидалась ошибка")
	}
}
