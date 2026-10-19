package store

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// История трафика лежит в бакете metrics двумя вложенными бакетами:
// series — окна по пользователям, totals — накопленные значения.
//
// Вложенные бакеты заводятся при первой записи: схема верхнего уровня от этого
// не меняется, а база, где истории ещё не было, читается как пустая.

var (
	bucketMetricSeries = []byte("series")
	bucketMetricTotals = []byte("totals")
)

// metricKeyLen — сколько байт занимает разрешение и начало окна в ключе.
const metricKeyLen = 16

// MetricSample — трафик одного пользователя за одно окно истории.
//
// Окно — отрезок фиксированной длины, выровненный по границе суток: 5 секунд,
// 15 секунд, минута, 5 минут. Start — его начало.
type MetricSample struct {
	UserID string
	Window time.Duration
	Start  time.Time
	Rx     uint64
	Tx     uint64
}

// MetricTotals — сколько трафика прошло через пользователя за всё время.
type MetricTotals struct {
	Rx uint64
	Tx uint64
}

// MetricSamples возвращает окна за отрезок [from, to) с заданным разрешением, по
// всем пользователям сразу. Складывать по пользователям — дело metrics: база
// отдаёт то, что в ней лежит.
func (s *Store) MetricSamples(window time.Duration, from, to time.Time) ([]MetricSample, error) {
	var samples []MetricSample

	err := s.db.View(func(tx *bolt.Tx) error {
		b, err := metricBucket(tx)
		if err != nil {
			return err
		}

		series := b.Bucket(bucketMetricSeries)
		if series == nil {
			return nil
		}

		prefix := metricWindowPrefix(window)
		stop := metricKey(window, to, "")

		c := series.Cursor()
		for key, value := c.Seek(metricKey(window, from, "")); key != nil; key, value = c.Next() {
			if !bytes.HasPrefix(key, prefix) || bytes.Compare(key, stop) >= 0 {
				break
			}

			start, err := metricKeyStart(key)
			if err != nil {
				return err
			}

			totals, err := decodeMetricTotals(value)
			if err != nil {
				return err
			}

			samples = append(samples, MetricSample{
				UserID: string(key[metricKeyLen:]),
				Window: window,
				Start:  start,
				Rx:     totals.Rx,
				Tx:     totals.Tx,
			})
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return samples, nil
}

// MetricTotals возвращает накопленный трафик по пользователям. Пользователя,
// которого уже удалили, здесь тоже может быть: его трафик был частью серверного,
// и стирать историю из-за удаления записи незачем.
func (s *Store) MetricTotals() (map[string]MetricTotals, error) {
	totals := make(map[string]MetricTotals)

	err := s.db.View(func(tx *bolt.Tx) error {
		b, err := metricBucket(tx)
		if err != nil {
			return err
		}

		stored := b.Bucket(bucketMetricTotals)
		if stored == nil {
			return nil
		}

		return stored.ForEach(func(key, value []byte) error {
			decoded, err := decodeMetricTotals(value)
			if err != nil {
				return fmt.Errorf("итог %q: %w", key, err)
			}

			totals[string(key)] = decoded

			return nil
		})
	})
	if err != nil {
		return nil, err
	}

	return totals, nil
}

// SaveMetrics записывает окна и накопленные итоги одной транзакцией.
//
// Окно записывается целиком, а не приращением: вызывающий присылает текущее
// значение окна, и повторная запись просто заменяет прежнее. Так падение между
// записями не задваивает трафик.
func (s *Store) SaveMetrics(samples []MetricSample, totals map[string]MetricTotals) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := metricBucket(tx)
		if err != nil {
			return err
		}

		if err := saveSamples(b, samples); err != nil {
			return err
		}

		return saveTotals(b, totals)
	})
}

// saveSamples записывает окна в series.
func saveSamples(b *bolt.Bucket, samples []MetricSample) error {
	if len(samples) == 0 {
		return nil
	}

	series, err := b.CreateBucketIfNotExists(bucketMetricSeries)
	if err != nil {
		return fmt.Errorf("бакет series: %w", err)
	}

	for _, sample := range samples {
		if sample.UserID == "" {
			return errors.New("окно без пользователя")
		}
		if sample.Window <= 0 {
			return fmt.Errorf("окно пользователя %q: длина %v", sample.UserID, sample.Window)
		}

		key := metricKey(sample.Window, sample.Start, sample.UserID)
		if err := series.Put(key, encodeMetricTotals(MetricTotals{Rx: sample.Rx, Tx: sample.Tx})); err != nil {
			return fmt.Errorf("окно пользователя %q: %w", sample.UserID, err)
		}
	}

	return nil
}

// saveTotals записывает накопленные итоги в totals.
func saveTotals(b *bolt.Bucket, totals map[string]MetricTotals) error {
	if len(totals) == 0 {
		return nil
	}

	stored, err := b.CreateBucketIfNotExists(bucketMetricTotals)
	if err != nil {
		return fmt.Errorf("бакет totals: %w", err)
	}

	for userID, value := range totals {
		if err := stored.Put([]byte(userID), encodeMetricTotals(value)); err != nil {
			return fmt.Errorf("итог %q: %w", userID, err)
		}
	}

	return nil
}

// DeleteMetricsBefore выбрасывает окна с заданным разрешением, начавшиеся раньше
// before: хранить историю дольше, чем её спрашивают, незачем.
func (s *Store) DeleteMetricsBefore(window time.Duration, before time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := metricBucket(tx)
		if err != nil {
			return err
		}

		series := b.Bucket(bucketMetricSeries)
		if series == nil {
			return nil
		}

		prefix := metricWindowPrefix(window)
		stop := metricKey(window, before, "")

		// Сначала собираем ключи, потом удаляем: удалять под курсором, который в
		// это же время двигается, — лишний повод ошибиться.
		var old [][]byte

		c := series.Cursor()
		for key, _ := c.Seek(prefix); key != nil; key, _ = c.Next() {
			if !bytes.HasPrefix(key, prefix) || bytes.Compare(key, stop) >= 0 {
				break
			}

			old = append(old, append([]byte(nil), key...))
		}

		for _, key := range old {
			if err := series.Delete(key); err != nil {
				return fmt.Errorf("старое окно: %w", err)
			}
		}

		return nil
	})
}

// metricBucket достаёт бакет metrics. Его заводит схема, поэтому отсутствие
// бакета означает испорченную базу, а не обычное состояние.
func metricBucket(tx *bolt.Tx) (*bolt.Bucket, error) {
	b := tx.Bucket(bucketMetrics)
	if b == nil {
		return nil, errors.New("нет бакета metrics")
	}

	return b, nil
}

// metricKey собирает ключ окна: разрешение, начало окна и пользователь.
//
// Разрешение идёт первым, начало — вторым: тогда выборка за отрезок — это один
// проход курсором по соседним ключам, без разбора всей истории.
func metricKey(window time.Duration, start time.Time, userID string) []byte {
	key := make([]byte, 0, metricKeyLen+len(userID))
	key = binary.BigEndian.AppendUint64(key, uint64(window))
	key = binary.BigEndian.AppendUint64(key, uint64(start.Unix()))

	return append(key, userID...)
}

// metricWindowPrefix — ключ, с которого начинаются все окна одного разрешения.
func metricWindowPrefix(window time.Duration) []byte {
	return binary.BigEndian.AppendUint64(nil, uint64(window))
}

// metricKeyStart достаёт начало окна из ключа.
func metricKeyStart(key []byte) (time.Time, error) {
	if len(key) < metricKeyLen {
		return time.Time{}, fmt.Errorf("ключ окна испорчен: %d байт", len(key))
	}

	return time.Unix(int64(binary.BigEndian.Uint64(key[8:metricKeyLen])), 0).UTC(), nil
}

// encodeMetricTotals кодирует два счётчика: восемь байт принятого, восемь
// отданного.
func encodeMetricTotals(totals MetricTotals) []byte {
	raw := make([]byte, 16)
	binary.BigEndian.PutUint64(raw[0:8], totals.Rx)
	binary.BigEndian.PutUint64(raw[8:16], totals.Tx)

	return raw
}

func decodeMetricTotals(raw []byte) (MetricTotals, error) {
	if len(raw) != 16 {
		return MetricTotals{}, fmt.Errorf("значение окна испорчено: %d байт", len(raw))
	}

	return MetricTotals{
		Rx: binary.BigEndian.Uint64(raw[0:8]),
		Tx: binary.BigEndian.Uint64(raw[8:16]),
	}, nil
}
