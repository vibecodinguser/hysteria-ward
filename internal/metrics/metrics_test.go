package metrics

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// base — начало окна, от которого считаются времена в тестах.
var base = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

// fakeClock — часы теста: время двигается вручную, а не ожиданием.
type fakeClock struct {
	at time.Time
}

func (c *fakeClock) now() time.Time { return c.at }

func (c *fakeClock) advance(d time.Duration) { c.at = c.at.Add(d) }

// openDB открывает пустую базу во временном каталоге и отдаёт её путь: тесту
// перезапуска нужен тот же файл.
func openDB(t *testing.T) (*store.Store, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "hysteria-ward.db")

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	return db, path
}

// newCollector собирает сборщик с подменяемыми часами.
func newCollector(t *testing.T, db *store.Store) (*Collector, *fakeClock) {
	t.Helper()

	c, err := New(db, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	clock := &fakeClock{at: base}
	c.now = clock.now

	return c, clock
}

// TestSampleCountsTrafficAndRates — снимок отдаёт накопленное и скорость за
// прошедший интервал.
func TestSampleCountsTrafficAndRates(t *testing.T) {
	db, _ := openDB(t)
	c, clock := newCollector(t, db)

	// Первый снимок задаёт точку отсчёта: скорости пока не за что считать.
	c.Sample()

	c.Add("a", 600, 300) // 600 отдано, 300 принято
	clock.advance(3 * time.Second)

	snapshot := c.Sample()
	if snapshot.Tx != 600 || snapshot.Rx != 300 {
		t.Fatalf("итоги = %d/%d, ожидались 600/300", snapshot.Tx, snapshot.Rx)
	}
	if len(snapshot.Users) != 1 {
		t.Fatalf("пользователей %d, ожидался 1", len(snapshot.Users))
	}

	user := snapshot.Users[0]
	if user.UserID != "a" {
		t.Errorf("пользователь = %q, ожидался a", user.UserID)
	}
	if user.TxRate < 199.9 || user.TxRate > 200.1 {
		t.Errorf("скорость отдачи = %v, ожидалось 200 Б/с", user.TxRate)
	}
	if user.RxRate < 99.9 || user.RxRate > 100.1 {
		t.Errorf("скорость приёма = %v, ожидалось 100 Б/с", user.RxRate)
	}

	// Скорость считается по приросту, а не по накопленному: второй интервал
	// такой же длины даёт те же 200 Б/с при вдвое большем итоге.
	c.Add("a", 200, 100)
	clock.advance(time.Second)

	snapshot = c.Sample()
	if got := snapshot.Users[0]; got.Tx != 800 || got.Rx != 400 {
		t.Errorf("итоги = %d/%d, ожидались 800/400", got.Tx, got.Rx)
	}
	if got := snapshot.Users[0].TxRate; got < 199.9 || got > 200.1 {
		t.Errorf("скорость отдачи = %v, ожидалось 200 Б/с", got)
	}
}

// TestSampleDoesNotMeasureTwiceInARow — частый запрос интерфейса не снимает
// второй снимок: иначе короткий интервал превратил бы случайные байты в
// огромную скорость, а следующий снимок — в маленькую.
func TestSampleDoesNotMeasureTwiceInARow(t *testing.T) {
	db, _ := openDB(t)
	c, clock := newCollector(t, db)

	c.Sample()
	c.Add("a", 100, 100)
	clock.advance(time.Second)

	first := c.Sample()

	clock.advance(50 * time.Millisecond)
	c.Add("a", 10, 10)

	second := c.Sample()
	if !second.At.Equal(first.At) {
		t.Errorf("снимок снят повторно: %v после %v", second.At, first.At)
	}
	if second.Tx != first.Tx {
		t.Errorf("итоги в повторном снимке = %d, ожидались %d", second.Tx, first.Tx)
	}
}

// TestHistoryKeepsRecentTraffic — последние секунды попадают в график, хотя до
// базы они ещё не дошли.
func TestHistoryKeepsRecentTraffic(t *testing.T) {
	db, _ := openDB(t)
	c, clock := newCollector(t, db)

	c.Sample()
	c.Add("a", 300, 500)
	clock.advance(time.Second)
	c.Sample()

	history, err := c.History(Hour)
	if err != nil {
		t.Fatalf("History() = %v", err)
	}
	if history.Step != 5*time.Second {
		t.Errorf("шаг = %v, ожидался 5s", history.Step)
	}

	point := pointAt(t, history, base)
	if point.Tx != 300 || point.Rx != 500 {
		t.Errorf("окно = %d/%d, ожидались 300/500", point.Tx, point.Rx)
	}
}

// TestHistoryAggregatesUsers — график показывает весь трафик: окна разных
// пользователей складываются.
func TestHistoryAggregatesUsers(t *testing.T) {
	db, _ := openDB(t)
	c, clock := newCollector(t, db)

	c.Sample()
	c.Add("a", 100, 100)
	c.Add("b", 200, 300)
	clock.advance(time.Second)
	c.Sample()

	history, err := c.History(Hour)
	if err != nil {
		t.Fatalf("History() = %v", err)
	}

	point := pointAt(t, history, base)
	if point.Tx != 300 || point.Rx != 400 {
		t.Errorf("окно = %d/%d, ожидались 300/400", point.Tx, point.Rx)
	}
}

// TestHistorySurvivesRestart — история переживает перезапуск панели: окно
// записано до остановки и читается новым сборщиком.
func TestHistorySurvivesRestart(t *testing.T) {
	db, path := openDB(t)

	first, clock := newCollector(t, db)
	first.Sample()
	first.Add("a", 700, 900)
	clock.advance(time.Second)
	first.Sample()

	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("закрытие базы: %v", err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("повторное Open() = %v", err)
	}
	defer reopened.Close()

	second, err := New(reopened, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	second.now = func() time.Time { return base.Add(2 * time.Second) }

	history, err := second.History(Hour)
	if err != nil {
		t.Fatalf("History() = %v", err)
	}

	point := pointAt(t, history, base)
	if point.Tx != 700 || point.Rx != 900 {
		t.Errorf("окно после перезапуска = %d/%d, ожидались 700/900", point.Tx, point.Rx)
	}
}

// TestNewRestoresTotals — накопленный трафик продолжается после перезапуска, а
// не начинается с нуля.
func TestNewRestoresTotals(t *testing.T) {
	db, path := openDB(t)

	first, clock := newCollector(t, db)
	first.Sample()
	first.Add("a", 500, 500)
	clock.advance(time.Second)
	first.Sample()

	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("закрытие базы: %v", err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("повторное Open() = %v", err)
	}
	defer reopened.Close()

	second, err := New(reopened, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	second.now = func() time.Time { return base.Add(2 * time.Second) }

	snapshot := second.Sample()
	if snapshot.Tx != 500 || snapshot.Rx != 500 {
		t.Fatalf("итоги = %d/%d, ожидались 500/500", snapshot.Tx, snapshot.Rx)
	}
	if got := snapshot.Users[0].TxRate; got != 0 {
		t.Errorf("скорость сразу после перезапуска = %v, ожидался 0", got)
	}
}

// TestHistoryStepMatchesRange — каждому отрезку соответствует своё разрешение из
// ТЗ.
func TestHistoryStepMatchesRange(t *testing.T) {
	db, _ := openDB(t)
	c, _ := newCollector(t, db)

	cases := []struct {
		r    Range
		step time.Duration
	}{
		{Hour, 5 * time.Second},
		{ThreeHours, 15 * time.Second},
		{Day, time.Minute},
		{Week, 5 * time.Minute},
	}

	for _, tc := range cases {
		history, err := c.History(tc.r)
		if err != nil {
			t.Fatalf("History(%q) = %v", tc.r, err)
		}
		if history.Step != tc.step {
			t.Errorf("шаг %q = %v, ожидался %v", tc.r, history.Step, tc.step)
		}
		if len(history.Points) == 0 {
			t.Errorf("ряд %q пуст", tc.r)
		}
	}

	if _, err := c.History("2h"); err == nil {
		t.Error("неизвестный отрезок принят, ожидалась ошибка")
	}
}

// TestPurgeDropsOldWindows — окна, которых уже не спрашивают, выбрасываются.
func TestPurgeDropsOldWindows(t *testing.T) {
	db, _ := openDB(t)
	c, clock := newCollector(t, db)

	c.Sample()
	c.Add("a", 100, 100)
	clock.advance(time.Second)
	c.Sample()

	if err := c.flush(true); err != nil {
		t.Fatalf("flush() = %v", err)
	}

	// Через восемь дней это окно не нужно ни одному отрезку.
	clock.advance(8 * 24 * time.Hour)
	if err := c.purge(); err != nil {
		t.Fatalf("purge() = %v", err)
	}

	samples, err := db.MetricSamples(5*time.Second, base, base.Add(5*time.Second))
	if err != nil {
		t.Fatalf("MetricSamples() = %v", err)
	}
	if len(samples) != 0 {
		t.Errorf("старых окон %d, ожидалось 0", len(samples))
	}

	// Свежие окна на месте: сборщик продолжил писать.
	c.Add("a", 50, 50)
	clock.advance(time.Second)
	c.Sample()

	history, err := c.History(Hour)
	if err != nil {
		t.Fatalf("History() = %v", err)
	}

	last := history.Points[len(history.Points)-1]
	if last.Tx != 50 || last.Rx != 50 {
		t.Errorf("свежее окно = %d/%d, ожидались 50/50", last.Tx, last.Rx)
	}
}

// TestSubscribeReceivesSnapshots — подписчик получает снимок, а отказ от
// подписки закрывает канал.
func TestSubscribeReceivesSnapshots(t *testing.T) {
	db, _ := openDB(t)
	c, clock := newCollector(t, db)

	c.Sample()

	updates, cancel := c.Subscribe()

	c.Add("a", 10, 10)
	clock.advance(time.Second)
	c.Sample()

	select {
	case snapshot := <-updates:
		if snapshot.Tx != 10 {
			t.Errorf("снимок = %d, ожидалось 10", snapshot.Tx)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("снимок не пришёл подписчику")
	}

	cancel()

	// В канале мог остаться последний снимок; после него канал закрыт.
	for range updates {
	}
}

// pointAt ищет окно по его началу.
func pointAt(t *testing.T, history History, at time.Time) Point {
	t.Helper()

	for _, point := range history.Points {
		if point.At.Equal(at) {
			return point
		}
	}

	t.Fatalf("окно %v не найдено в ряду из %d точек", at, len(history.Points))

	return Point{}
}
