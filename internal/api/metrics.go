package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/metrics"
)

const (
	// sseRetry — через сколько браузер переподключится, если поток оборвался.
	sseRetry = 3000

	// sseCheck — как часто поток проверяет, жив ли вход. Проверка не продлевает
	// сессию: открытая вкладка — не работа администратора, и держать из-за неё
	// вход открытым нельзя.
	sseCheck = 5 * time.Second
)

// trafficTotals — числа трафика в ответе: накопленное и скорость.
type trafficTotals struct {
	Rx     uint64  `json:"rx"`
	Tx     uint64  `json:"tx"`
	RxRate float64 `json:"rx_rate"`
	TxRate float64 `json:"tx_rate"`
}

// trafficUser — пользователь с текущим трафиком.
type trafficUser struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Online  bool   `json:"online"`
	trafficTotals
}

// metricsAnswer — снимок текущих значений: его же получает поток метрик.
type metricsAnswer struct {
	At    time.Time     `json:"at"`
	Users []trafficUser `json:"users"`
	Total trafficTotals `json:"total"`
}

// historyPoint — одно окно истории: сколько байт прошло за шаг.
type historyPoint struct {
	At time.Time `json:"at"`
	Rx uint64    `json:"rx"`
	Tx uint64    `json:"tx"`
}

// historyAnswer — ряд окон за отрезок.
type historyAnswer struct {
	Range  string         `json:"range"`
	Step   int            `json:"step_seconds"`
	Points []historyPoint `json:"points"`
}

// handleMetrics отдаёт текущие значения по пользователям.
//
// Интерфейс обновляет их потоком, а этот ответ нужен для первой отрисовки и для
// тех, кто читает панель не из браузера.
func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	answer, err := s.snapshotAnswer(s.metrics.Snapshot())
	if err != nil {
		s.logger.Error("снимок метрик", "err", err)
		s.answerError(w, http.StatusInternalServerError, "трафик не прочитан")

		return
	}

	s.answerJSON(w, http.StatusOK, answer)
}

// handleMetricsHistory отдаёт историю за отрезок.
func (s *Server) handleMetricsHistory(w http.ResponseWriter, r *http.Request) {
	history, err := s.metrics.History(metrics.Range(r.URL.Query().Get("range")))
	if errors.Is(err, metrics.ErrUnknownRange) {
		s.answerError(w, http.StatusBadRequest, "неизвестный отрезок истории: ожидались 1h, 3h, 24h или 7d")
		return
	}
	if err != nil {
		s.logger.Error("история трафика", "err", err)
		s.answerError(w, http.StatusInternalServerError, "история не прочитана")

		return
	}

	answer := historyAnswer{
		Range:  string(history.Range),
		Step:   int(history.Step / time.Second),
		Points: make([]historyPoint, 0, len(history.Points)),
	}

	for _, point := range history.Points {
		answer.Points = append(answer.Points, historyPoint{At: point.At, Rx: point.Rx, Tx: point.Tx})
	}

	s.answerJSON(w, http.StatusOK, answer)
}

// snapshotAnswer дополняет снимок тем, что знает интерфейс: именами, состоянием
// и тем, кто сейчас в сети.
//
// Трафик удалённых пользователей остаётся в общих числах, но в списке их нет:
// показывать строку без имени незачем.
func (s *Server) snapshotAnswer(snapshot metrics.Snapshot) (metricsAnswer, error) {
	list, err := s.store.Users()
	if err != nil {
		return metricsAnswer{}, err
	}

	traffic := make(map[string]metrics.Traffic, len(snapshot.Users))
	for _, user := range snapshot.Users {
		traffic[user.UserID] = user
	}

	answer := metricsAnswer{
		At: snapshot.At,
		Total: trafficTotals{
			Rx:     snapshot.Rx,
			Tx:     snapshot.Tx,
			RxRate: snapshot.RxRate,
			TxRate: snapshot.TxRate,
		},
		Users: make([]trafficUser, 0, len(list)),
	}

	for _, user := range list {
		current := traffic[user.ID]
		answer.Users = append(answer.Users, trafficUser{
			ID:      user.ID,
			Name:    user.Name,
			Enabled: user.Enabled,
			Online:  s.users.IsOnline(user.ID),
			trafficTotals: trafficTotals{
				Rx:     current.Rx,
				Tx:     current.Tx,
				RxRate: current.RxRate,
				TxRate: current.TxRate,
			},
		})
	}

	return answer, nil
}

// handleEvents отдаёт поток живых значений.
//
// Server-Sent Events, а не WebSocket: данные идут только в одну сторону, а
// браузеру хватает EventSource — без библиотек и без рукопожатия. Поток
// закрывается при остановке панели и тогда, когда сессия отжила.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.answerError(w, http.StatusInternalServerError, "сервер не умеет отдавать поток")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "retry: %d\n\n", sseRetry)
	flusher.Flush()

	updates, cancel := s.metrics.Subscribe()
	defer cancel()

	// Первый снимок уходит сразу: ждать секунду, пока сработают часы, незачем.
	if !s.writeSnapshot(w, flusher, s.metrics.Snapshot()) {
		return
	}

	token := s.sessionToken(r)
	check := time.NewTicker(sseCheck)
	defer check.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case <-s.stopped:
			return

		case <-check.C:
			if _, err := s.auth.Lookup(token); err != nil {
				return
			}

		case snapshot, ok := <-updates:
			if !ok {
				return
			}
			if !s.writeSnapshot(w, flusher, snapshot) {
				return
			}
		}
	}
}

// writeSnapshot отправляет очередной снимок. false означает, что писать больше
// некуда: клиент ушёл.
func (s *Server) writeSnapshot(w io.Writer, flusher http.Flusher, snapshot metrics.Snapshot) bool {
	answer, err := s.snapshotAnswer(snapshot)
	if err != nil {
		// Одна неудача не повод рвать поток: следующий снимок может пройти.
		s.logger.Error("снимок метрик", "err", err)

		return true
	}

	data, err := json.Marshal(answer)
	if err != nil {
		s.logger.Error("снимок метрик не закодирован", "err", err)

		return true
	}

	if _, err := fmt.Fprintf(w, "event: metrics\ndata: %s\n\n", data); err != nil {
		return false
	}

	flusher.Flush()

	return true
}
