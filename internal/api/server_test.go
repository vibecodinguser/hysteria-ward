package api_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/vibecodinguser/hysteria-ward/internal/api"
	"github.com/vibecodinguser/hysteria-ward/internal/config"
)

// serverOf читает настройки сервера.
func serverOf(t *testing.T, s *api.Server, cookie *http.Cookie) serverJSON {
	t.Helper()

	resp := do(t, s, http.MethodGet, "/api/server", "", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("настройки сервера: код %d, ожидался 200", resp.StatusCode)
	}

	return decode[serverJSON](t, resp)
}

// TestMasqueradeIsSavedAndApplied — режим меняется из интерфейса и вступает в
// силу только по «Применить»: запись домена ядро не трогает, а применение
// перезапускает его.
func TestMasqueradeIsSavedAndApplied(t *testing.T) {
	opts := options(t)

	applied := 0
	var applyErr error
	opts.Apply = func() error {
		applied++

		return applyErr
	}

	s := start(t, opts)
	cookie, csrf := enter(t, s, "admin", "admin")

	if got := serverOf(t, s, cookie); got.Masquerade.Mode != config.MasqueradeOff {
		t.Fatalf("начальный режим = %q, ожидался %q", got.Masquerade.Mode, config.MasqueradeOff)
	}

	// Запись домена — не повод перезапускать ядро.
	resp := do(t, s, http.MethodPut, "/api/server", `{"domain":"example.com"}`, cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("запись домена: код %d, ожидался 200", resp.StatusCode)
	}
	if applied != 0 {
		t.Fatalf("домен применился через ядро %d раз, ожидалось 0", applied)
	}

	// Свой сайт: панель запоминает правку и применяет её.
	resp = do(t, s, http.MethodPost, "/api/server/apply",
		`{"masquerade":{"mode":"file","file":"/srv/www"}}`, cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("применение сайта: код %d, ожидался 200", resp.StatusCode)
	}
	if applied != 1 {
		t.Fatalf("ядро перезапущено %d раз, ожидался 1", applied)
	}

	answer := decode[serverJSON](t, resp)
	if answer.Masquerade.Mode != config.MasqueradeFile || answer.Masquerade.File != "/srv/www" {
		t.Errorf("ответ после применения = %+v, ожидался режим file с каталогом /srv/www", answer.Masquerade)
	}
	if answer.Domain != "example.com" {
		t.Errorf("домен = %q, ожидался example.com", answer.Domain)
	}

	if got := serverOf(t, s, cookie); got.Masquerade != answer.Masquerade {
		t.Errorf("после перечитывания masquerade = %+v, ожидался %+v", got.Masquerade, answer.Masquerade)
	}

	// Ошибка применения не отменяет запись: правка осталась черновиком, и её
	// можно починить, не вводя заново.
	applyErr = errors.New("masquerade: каталог сайта: нет такого каталога")

	resp = do(t, s, http.MethodPost, "/api/server/apply",
		`{"masquerade":{"mode":"proxy","proxy":"https://example.com"}}`, cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("неудачное применение: код %d, ожидался 422", resp.StatusCode)
	}
	if body := readBody(t, resp); !strings.Contains(body, "нет такого каталога") {
		t.Errorf("ответ = %q, ожидалась причина отказа", body)
	}
	if got := serverOf(t, s, cookie); got.Masquerade.Mode != config.MasqueradeProxy {
		t.Errorf("после отказа режим = %q, ожидался черновик proxy", got.Masquerade.Mode)
	}

	// Выключить masquerade тоже можно: это такая же правка, как и любая другая.
	applyErr = nil

	resp = do(t, s, http.MethodPost, "/api/server/apply",
		`{"masquerade":{"mode":"off"}}`, cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("выключение masquerade: код %d, ожидался 200", resp.StatusCode)
	}
	if got := serverOf(t, s, cookie); got.Masquerade.Mode != config.MasqueradeOff {
		t.Errorf("после выключения режим = %q, ожидался off", got.Masquerade.Mode)
	}
	if applied != 3 {
		t.Errorf("ядро перезапущено %d раз, ожидалось 3", applied)
	}
}

// TestBandwidthIsSavedAndApplied — глобальные пределы сохраняются и доходят до
// ядра тем же применением, что и masquerade.
func TestBandwidthIsSavedAndApplied(t *testing.T) {
	opts := options(t)

	applied := 0
	opts.Apply = func() error {
		applied++

		return nil
	}

	s := start(t, opts)
	cookie, csrf := enter(t, s, "admin", "admin")

	resp := do(t, s, http.MethodPost, "/api/server/apply",
		`{"bandwidth":{"up":65536,"down":131072}}`, cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("применение скорости: код %d, ожидался 200", resp.StatusCode)
	}

	answer := decode[serverJSON](t, resp)
	if answer.Bandwidth.Up != 65536 || answer.Bandwidth.Down != 131072 {
		t.Errorf("скорость = %+v, ожидались 65536/131072", answer.Bandwidth)
	}
	if applied != 1 {
		t.Errorf("ядро перезапущено %d раз, ожидался 1", applied)
	}

	if got := serverOf(t, s, cookie); got.Bandwidth != answer.Bandwidth {
		t.Errorf("после перечитывания скорость = %+v, ожидалась %+v", got.Bandwidth, answer.Bandwidth)
	}

	// Слишком маленький ненулевой предел ядро отвергнет; панель ловит это до
	// базы и до остановки работающего сервера.
	resp = do(t, s, http.MethodPost, "/api/server/apply",
		`{"bandwidth":{"up":1}}`, cookie,
		map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("слишком маленькая скорость: код %d, ожидался 400", resp.StatusCode)
	}
	if applied != 1 {
		t.Errorf("негодная скорость дошла до ядра: применений %d, ожидалось 1", applied)
	}
}

// TestMasqueradeIsValidatedBeforeApply — негодный режим не доходит ни до базы,
// ни до ядра: администратор видит причину сразу.
func TestMasqueradeIsValidatedBeforeApply(t *testing.T) {
	opts := options(t)

	applied := 0
	opts.Apply = func() error {
		applied++

		return nil
	}

	s := start(t, opts)
	cookie, csrf := enter(t, s, "admin", "admin")

	cases := map[string]string{
		"без режима":       `{}`,
		"неизвестный":      `{"masquerade":{"mode":"какой-то"}}`,
		"file без пути":    `{"masquerade":{"mode":"file"}}`,
		"proxy без адреса": `{"masquerade":{"mode":"proxy"}}`,
		"proxy без схемы":  `{"masquerade":{"mode":"proxy","proxy":"example.com"}}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			resp := do(t, s, http.MethodPost, "/api/server/apply", body, cookie,
				map[string]string{"X-CSRF-Token": csrf})
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("код %d, ожидался 400", resp.StatusCode)
			}
		})
	}

	if applied != 0 {
		t.Errorf("негодная правка дошла до ядра %d раз, ожидалось 0", applied)
	}

	// Без CSRF-токена менять настройки сервера нельзя.
	resp := do(t, s, http.MethodPost, "/api/server/apply",
		`{"masquerade":{"mode":"off"}}`, cookie, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("без CSRF: код %d, ожидался 403", resp.StatusCode)
	}
}
