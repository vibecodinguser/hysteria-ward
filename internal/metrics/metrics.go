// Пакет metrics считает трафик пользователей: текущие значения и историю.
//
// Ядро сообщает о каждом пакете, поэтому горячий путь здесь — сложить байты в
// счётчик. Всё остальное (приросты, окна истории, скорости, запись в базу) идёт
// по часам: раз в секунду снимается снимок, раз в пять секунд он доходит до базы.
//
// Это единственное место, знающее, как история складывается в окна: база хранит
// готовые окна, а не сырые пакеты.
package metrics

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// Умолчания сборщика.
const (
	// sampleEvery — как часто снимаются текущие значения. Требование интерфейса —
	// раз в две секунды, но запас в секунду стоит дёшево и переживает задержки.
	sampleEvery = time.Second

	// minSampleGap — ближе этого снимки не снимаются: обработчик запроса не
	// должен устраивать лишний замер, из-за которого следующий интервал станет
	// короче, а скорость — выше настоящей.
	minSampleGap = 250 * time.Millisecond

	// flushEvery — как часто снимок доходит до базы. Это же худшая потеря истории
	// при аварийной остановке панели.
	flushEvery = 5 * time.Second

	// purgeEvery — как часто выбрасываются окна, которых уже не спрашивают.
	purgeEvery = time.Minute
)

// window — разрешение истории: длина окна и сколько его хранить.
type window struct {
	size time.Duration
	keep time.Duration
}

// windows — разрешения истории. Ровно те, что требует ТЗ, и в порядке роста:
// час по 5 секунд, три часа по 15 секунд, сутки по минуте, неделя по 5 минут.
var windows = []window{
	{5 * time.Second, time.Hour},
	{15 * time.Second, 3 * time.Hour},
	{time.Minute, 24 * time.Hour},
	{5 * time.Minute, 7 * 24 * time.Hour},
}

// Range — отрезок истории, который показывает интерфейс.
type Range string

const (
	// Hour — последний час.
	Hour Range = "1h"

	// ThreeHours — последние три часа.
	ThreeHours Range = "3h"

	// Day — последние сутки.
	Day Range = "24h"

	// Week — последняя неделя.
	Week Range = "7d"
)

// ErrUnknownRange — такого отрезка истории панель не умеет: интерфейс просит
// только те, что перечислены выше.
var ErrUnknownRange = errors.New("неизвестный отрезок истории")

// ranges — сколько длится каждый отрезок. Длина отрезка — она же срок хранения
// его окон: хранить дольше, чем спрашивают, незачем.
var ranges = map[Range]time.Duration{
	Hour:       time.Hour,
	ThreeHours: 3 * time.Hour,
	Day:        24 * time.Hour,
	Week:       7 * 24 * time.Hour,
}

// windowFor подбирает разрешение для отрезка.
func windowFor(r Range) (window, bool) {
	duration, ok := ranges[r]
	if !ok {
		return window{}, false
	}

	for _, w := range windows {
		if w.keep == duration {
			return w, true
		}
	}

	return window{}, false
}

// Traffic — сколько прошло через одного пользователя.
type Traffic struct {
	UserID string
	Rx     uint64
	Tx     uint64
	RxRate float64
	TxRate float64
}

// Snapshot — текущие значения на момент замера: накопленное и скорость.
type Snapshot struct {
	At     time.Time
	Users  []Traffic
	Rx     uint64
	Tx     uint64
	RxRate float64
	TxRate float64
}

// Point — одно окно истории: сколько байт прошло за него.
type Point struct {
	At time.Time
	Rx uint64
	Tx uint64
}

// History — ряд окон за отрезок. Шаг — длина одного окна.
type History struct {
	Range  Range
	Step   time.Duration
	Points []Point
}

// Collector — счётчики трафика и история.
type Collector struct {
	db     *store.Store
	logger *slog.Logger

	// now — часы сборщика. Отдельным полем: окна истории и скорости проверяются
	// сдвигом времени, а не ожиданием.
	now func() time.Time

	// mu защищает всё, что ниже. Счётчики в нём — атомарные: горячий путь только
	// прибавляет к ним, а снимок читает их под замком.
	mu          sync.Mutex
	counters    map[string]*counter
	current     map[userWindow]bucket
	pending     []bucket
	lastSample  time.Time
	lastFlush   time.Time
	lastPurge   time.Time
	dirty       bool
	snapshot    Snapshot
	subscribers map[chan Snapshot]struct{}
}

// counter — счётчики одного пользователя: сколько прошло за всё время и сколько
// было на прошлом снимке. Последнее нужно, чтобы получить прирост.
type counter struct {
	rx, tx         atomic.Uint64
	lastRx, lastTx uint64
}

// userWindow — окно, которое сейчас копится у пользователя.
type userWindow struct {
	userID string
	size   time.Duration
}

// bucket — накопленный трафик за конкретное окно.
type bucket struct {
	userID string
	size   time.Duration
	start  time.Time
	rx, tx uint64
}

// New открывает сборщик и подхватывает накопленные итоги: после перезапуска
// трафик продолжается, а не начинается с нуля.
func New(db *store.Store, logger *slog.Logger) (*Collector, error) {
	if db == nil {
		return nil, errors.New("не задана база метрик")
	}
	if logger == nil {
		return nil, errors.New("не задан журнал метрик")
	}

	totals, err := db.MetricTotals()
	if err != nil {
		return nil, err
	}

	c := &Collector{
		db:          db,
		logger:      logger,
		now:         time.Now,
		counters:    make(map[string]*counter, len(totals)),
		current:     make(map[userWindow]bucket),
		subscribers: make(map[chan Snapshot]struct{}),
	}

	for userID, value := range totals {
		cnt := &counter{}
		cnt.rx.Store(value.Rx)
		cnt.tx.Store(value.Tx)
		cnt.lastRx = value.Rx
		cnt.lastTx = value.Tx

		c.counters[userID] = cnt
	}

	return c, nil
}

// Add прибавляет трафик пользователя. Вызывается ядром на каждый пакет, поэтому
// здесь только счётчик: ни базы, ни окон, ни замка на всё.
func (c *Collector) Add(userID string, tx, rx uint64) {
	if userID == "" || (tx == 0 && rx == 0) {
		return
	}

	c.mu.Lock()
	cnt := c.counters[userID]
	if cnt == nil {
		cnt = &counter{}
		c.counters[userID] = cnt
	}
	c.mu.Unlock()

	cnt.rx.Add(rx)
	cnt.tx.Add(tx)
}

// Sample снимает текущие значения: считает приросты, раскладывает их по окнам
// истории и рассылает снимок подписчикам.
//
// Снимает только сборщик по своим часам. Обработчикам запросов нужен
// Snapshot: если бы каждый снимал свой, интервалы между замерами были бы
// неровными, и скорость скакала бы на ровном месте.
func (c *Collector) Sample() Snapshot {
	now := c.now()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Ближе minSampleGap снимок не снимается: иначе короткий интервал превратил
	// бы случайные байты в большую скорость, а следующий снимок — в маленькую.
	if !c.lastSample.IsZero() && now.Sub(c.lastSample) < minSampleGap {
		return c.snapshot
	}

	elapsed := time.Duration(0)
	if !c.lastSample.IsZero() {
		elapsed = now.Sub(c.lastSample)
	}

	snapshot := Snapshot{At: now, Users: make([]Traffic, 0, len(c.counters))}

	for userID, cnt := range c.counters {
		rx, tx := cnt.rx.Load(), cnt.tx.Load()
		deltaRx, deltaTx := rx-cnt.lastRx, tx-cnt.lastTx

		traffic := Traffic{UserID: userID, Rx: rx, Tx: tx}
		if elapsed > 0 {
			traffic.RxRate = rate(deltaRx, elapsed)
			traffic.TxRate = rate(deltaTx, elapsed)
		}

		c.accumulate(now, userID, deltaRx, deltaTx)
		cnt.lastRx, cnt.lastTx = rx, tx

		snapshot.Users = append(snapshot.Users, traffic)
		snapshot.Rx += rx
		snapshot.Tx += tx
		snapshot.RxRate += traffic.RxRate
		snapshot.TxRate += traffic.TxRate
	}

	sort.Slice(snapshot.Users, func(i, j int) bool {
		return snapshot.Users[i].UserID < snapshot.Users[j].UserID
	})

	c.lastSample = now
	c.snapshot = snapshot

	for subscriber := range c.subscribers {
		// Медленный подписчик пропускает снимок, а не задерживает рассылку:
		// прежнее значение выбрасывается, в канале всегда самое свежее.
		select {
		case <-subscriber:
		default:
		}

		select {
		case subscriber <- snapshot:
		default:
		}
	}

	return snapshot
}

// accumulate раскладывает прирост по окнам истории. Когда окно кончается, оно
// откладывается до записи в базу и начинается новое.
func (c *Collector) accumulate(now time.Time, userID string, rx, tx uint64) {
	if rx == 0 && tx == 0 {
		return
	}

	c.dirty = true

	for _, w := range windows {
		start := now.Truncate(w.size)
		key := userWindow{userID: userID, size: w.size}

		if current, ok := c.current[key]; ok && !current.start.Equal(start) {
			c.pending = append(c.pending, current)
			delete(c.current, key)
		}

		current, ok := c.current[key]
		if !ok {
			current = bucket{userID: userID, size: w.size, start: start}
		}

		current.rx += rx
		current.tx += tx
		c.current[key] = current
	}
}

// rate переводит прирост в байты в секунду.
func rate(delta uint64, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}

	return float64(delta) / elapsed.Seconds()
}

// Snapshot отдаёт последний снятый снимок. Он не старше одного шага часов —
// интерфейсу этого хватает, а ровный шаг важнее сиюминутности.
func (c *Collector) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.snapshot
}

// Subscribe отдаёт канал снимков и функцию отказа от подписки. Канал не
// блокирует рассылку: в нём всегда самое свежее значение, а не очередь.
func (c *Collector) Subscribe() (<-chan Snapshot, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()

	updates := make(chan Snapshot, 1)
	c.subscribers[updates] = struct{}{}

	var once sync.Once

	return updates, func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()

			delete(c.subscribers, updates)
			close(updates)
		})
	}
}

// History собирает ряд окон за отрезок.
//
// К уже записанным окнам подмешиваются те, что ещё в памяти: последние секунды
// истории не должны пропадать из-за того, что запись бывает раз в пять секунд.
// Окно из памяти заменяет записанное, а не прибавляется к нему: в базе лежит
// то же окно, каким оно было на прошлой записи.
func (c *Collector) History(r Range) (History, error) {
	w, ok := windowFor(r)
	if !ok {
		return History{}, ErrUnknownRange
	}

	now := c.now()
	from := now.Add(-ranges[r]).Truncate(w.size)

	samples, err := c.db.MetricSamples(w.size, from, now)
	if err != nil {
		return History{}, err
	}

	stored := make(map[seriesKey]store.MetricTotals, len(samples))
	for _, sample := range samples {
		stored[seriesKey{start: sample.Start.Unix(), userID: sample.UserID}] = store.MetricTotals{
			Rx: sample.Rx,
			Tx: sample.Tx,
		}
	}

	c.mu.Lock()
	for _, b := range c.pending {
		if b.size == w.size && !b.start.Before(from) {
			stored[seriesKey{start: b.start.Unix(), userID: b.userID}] = store.MetricTotals{Rx: b.rx, Tx: b.tx}
		}
	}
	for _, b := range c.current {
		if b.size == w.size && !b.start.Before(from) {
			stored[seriesKey{start: b.start.Unix(), userID: b.userID}] = store.MetricTotals{Rx: b.rx, Tx: b.tx}
		}
	}
	c.mu.Unlock()

	// Ряд сервера — сумма по пользователям: график показывает весь трафик, а не
	// одного выбранного.
	sums := make(map[int64]store.MetricTotals)
	for key, value := range stored {
		sum := sums[key.start]
		sum.Rx += value.Rx
		sum.Tx += value.Tx
		sums[key.start] = sum
	}

	points := make([]Point, 0, int(ranges[r]/w.size)+1)
	for start := from; !start.After(now); start = start.Add(w.size) {
		sum := sums[start.Unix()]
		points = append(points, Point{At: start, Rx: sum.Rx, Tx: sum.Tx})
	}

	return History{Range: r, Step: w.size, Points: points}, nil
}

// seriesKey — окно одного пользователя: по нему записанное в базе заменяется
// тем, что ещё в памяти.
type seriesKey struct {
	start  int64
	userID string
}

// Run снимает значения и записывает историю, пока не отменят контекст.
func (c *Collector) Run(ctx context.Context) {
	ticker := time.NewTicker(sampleEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.sampleAndFlush()
			return

		case <-ticker.C:
			c.sampleAndFlush()
		}
	}
}

// sampleAndFlush — один шаг часов: снимок, запись, чистка старого.
func (c *Collector) sampleAndFlush() {
	c.Sample()

	if err := c.flush(false); err != nil {
		c.logger.Error("история трафика не записана", "err", err)
	}

	if err := c.purge(); err != nil {
		c.logger.Error("старая история не убрана", "err", err)
	}
}

// Close записывает всё, что ещё не записано.
func (c *Collector) Close() error {
	return c.flush(true)
}

// flush записывает текущие окна и итоги. force снимает ограничение «не чаще
// flushEvery»: при остановке терять последние секунды незачем.
func (c *Collector) flush(force bool) error {
	now := c.now()

	c.mu.Lock()
	if !c.dirty && !force {
		c.mu.Unlock()
		return nil
	}
	if !force && now.Sub(c.lastFlush) < flushEvery {
		c.mu.Unlock()
		return nil
	}

	// Копия откладывается до записи: если база откажет, окна останутся в памяти
	// и уйдут со следующей попыткой. Текущие окна при этом пишутся всегда
	// целиком, поэтому повторная запись ничего не задваивает.
	pending := c.pending
	samples := make([]store.MetricSample, 0, len(pending)+len(c.current))
	for _, b := range pending {
		samples = append(samples, b.sample())
	}
	for _, b := range c.current {
		samples = append(samples, b.sample())
	}

	totals := make(map[string]store.MetricTotals, len(c.counters))
	for userID, cnt := range c.counters {
		totals[userID] = store.MetricTotals{Rx: cnt.rx.Load(), Tx: cnt.tx.Load()}
	}

	c.mu.Unlock()

	if err := c.db.SaveMetrics(samples, totals); err != nil {
		return err
	}

	c.mu.Lock()
	// Отложенные окна убираются только после удачной записи. Прибавленные за
	// время записи остаются: они в хвосте.
	if len(c.pending) >= len(pending) {
		c.pending = c.pending[len(pending):]
	}
	c.lastFlush = now
	c.dirty = false
	c.mu.Unlock()

	return nil
}

// sample превращает окно в запись для базы.
func (b bucket) sample() store.MetricSample {
	return store.MetricSample{
		UserID: b.userID,
		Window: b.size,
		Start:  b.start,
		Rx:     b.rx,
		Tx:     b.tx,
	}
}

// purge выбрасывает окна, которых уже не спрашивают.
func (c *Collector) purge() error {
	now := c.now()

	c.mu.Lock()
	if now.Sub(c.lastPurge) < purgeEvery {
		c.mu.Unlock()
		return nil
	}
	c.lastPurge = now
	c.mu.Unlock()

	for _, w := range windows {
		if err := c.db.DeleteMetricsBefore(w.size, now.Add(-w.keep)); err != nil {
			return err
		}
	}

	return nil
}
