// Пакет daemon ведёт жизненный цикл панели: открывает базу и журнал, держит ядро
// Hysteria 2, применяет конфигурацию и аккуратно всё закрывает.
//
// Ядро — подчинённый. Панель работает и тогда, когда ядро не поднялось или упало:
// иначе администратор не смог бы открыть интерфейс и починить конфигурацию.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vibecodinguser/hysteria-ward/internal/config"
	"github.com/vibecodinguser/hysteria-ward/internal/logging"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// stopTimeout — сколько панель ждёт, пока ядро отпустит ресурсы. Без предела
// остановка могла бы зависнуть навсегда.
const stopTimeout = 5 * time.Second

// Core — запущенное ядро Hysteria 2. Наружу от него нужны только остановка и
// признак того, что оно перестало обслуживать.
type Core interface {
	Stop() error
	Done() <-chan error
}

// Options — что открыть и как поднимать ядро.
type Options struct {
	// DBPath — файл базы.
	DBPath string

	// Log — куда писать журнал.
	Log logging.Options

	// StartCore поднимает ядро по конфигурации. Отдельной функцией, а не прямой
	// зависимостью: панель не знает про Hysteria 2 ничего лишнего, а тест
	// подставляет ядро, которым можно управлять.
	StartCore func(config.Config) (Core, error)

	// Preflight проверяет то, что видно только перед самым запуском — например,
	// читается ли сертификат. Вызывается до остановки работающего ядра, чтобы
	// такая ошибка не унесла с собой живой сервер. Необязателен.
	Preflight func(config.Config) error
}

// Daemon — работающая панель: база, журнал и ядро под общим управлением.
type Daemon struct {
	opts   Options
	db     *store.Store
	log    *logging.Log
	logger *slog.Logger

	// mu охраняет core. Жизненный цикл ядра целиком идёт под ним, поэтому два
	// одновременных «Применить» не подерутся за порт.
	mu   sync.Mutex
	core Core

	// failures принимает падения ядра. Буфер на один: если панель ещё не разобрала
	// прошлое падение, новое всё равно ничего не меняет.
	failures chan failure
}

// failure — падение ядра: какое именно ядро и почему. Ядро запоминается, чтобы
// запоздавшее падение прежнего ядра не сбило панель после успешного «Применить».
type failure struct {
	core Core
	err  error
}

// New открывает базу и журнал. Панель ещё не работает: за работу отвечает Run.
func New(opts Options) (*Daemon, error) {
	if opts.StartCore == nil {
		return nil, errors.New("не задан запуск ядра")
	}

	log, err := logging.Open(opts.Log)
	if err != nil {
		return nil, err
	}

	db, err := store.Open(opts.DBPath)
	if err != nil {
		_ = log.Close()
		return nil, err
	}

	return &Daemon{
		opts:     opts,
		db:       db,
		log:      log,
		logger:   log.Logger(),
		failures: make(chan failure, 1),
	}, nil
}

// Store отдаёт базу: настройки, пользователи и статистика нужны интерфейсу
// панели, и заводить для них отдельные обёртки незачем.
func (d *Daemon) Store() *store.Store {
	return d.db
}

// Log отдаёт журнал: страница логов читает последние строки из памяти.
func (d *Daemon) Log() *logging.Log {
	return d.log
}

// CoreRunning сообщает, работает ли ядро прямо сейчас. Панель показывает это
// администратору: упавшее ядро не должно выглядеть как рабочее.
func (d *Daemon) CoreRunning() bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.core != nil
}

// Close закрывает базу и журнал.
func (d *Daemon) Close() error {
	return errors.Join(d.db.Close(), d.log.Close())
}

// Run применяет конфигурацию и работает до отмены контекста (в бою — SIGINT или
// SIGTERM). Штатное завершение возвращает nil.
//
// Неудачное применение Run не прерывает: причина уходит в журнал, а панель
// остаётся доступной, чтобы конфигурацию можно было починить.
func (d *Daemon) Run(ctx context.Context) error {
	_ = d.Apply()

	for {
		select {
		case <-ctx.Done():
			d.mu.Lock()
			err := d.stopLocked()
			d.mu.Unlock()
			if err != nil {
				return err
			}

			d.logger.Info("панель остановлена")
			return nil

		case f := <-d.failures:
			d.logger.Error("ядро упало", "err", f.err)

			d.mu.Lock()
			if d.core == f.core {
				d.core = nil
			}
			d.mu.Unlock()
		}
	}
}

// Apply перечитывает конфигурацию из базы и перезапускает ядро. Активные
// соединения при этом обрываются — об этом администратора предупреждает интерфейс.
//
// Всё, что можно проверить до остановки ядра, проверяется до неё: негодная правка
// не должна уносить с собой работающий сервер. Проверить заранее получается не всё
// — занятый порт или нечитаемый сертификат вскрываются только на запуске, и тогда
// сервер остаётся без ядра, пока правку не починят.
func (d *Daemon) Apply() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	err := d.applyLocked()
	if err != nil {
		d.logger.Error("конфигурация не применена", "err", err, "ядро работает", d.core != nil)
		return err
	}

	return nil
}

// applyLocked — работа Apply под уже взятым mu.
func (d *Daemon) applyLocked() error {
	cfg, err := d.configLocked()
	if err != nil {
		return err
	}

	if err := d.preflightLocked(cfg); err != nil {
		return fmt.Errorf("конфигурация негодна: %w", err)
	}

	if err := d.stopLocked(); err != nil {
		return err
	}

	if err := d.startLocked(cfg); err != nil {
		return err
	}

	d.logger.Info("конфигурация применена")

	return nil
}

// configLocked читает и проверяет конфигурацию из базы. Вызывается под mu.
func (d *Daemon) configLocked() (config.Config, error) {
	cfg, err := d.db.Config()
	if err != nil {
		return config.Config{}, err
	}

	if err := cfg.Validate(); err != nil {
		return config.Config{}, fmt.Errorf("конфигурация негодна: %w", err)
	}

	return cfg, nil
}

// preflightLocked прогоняет конфигурацию через проверку, которую умеет только тот,
// кто будет поднимать ядро: панель сертификат не читает. Вызывается под mu.
func (d *Daemon) preflightLocked(cfg config.Config) error {
	if d.opts.Preflight == nil {
		return nil
	}

	return d.opts.Preflight(cfg)
}

// startLocked поднимает ядро по готовой конфигурации. Вызывается под mu.
func (d *Daemon) startLocked(cfg config.Config) error {
	core, err := d.opts.StartCore(cfg)
	if err != nil {
		return fmt.Errorf("запуск ядра: %w", err)
	}

	d.core = core
	go d.watch(core)

	d.logger.Info("ядро запущено", "listen", cfg.Listen)

	return nil
}

// stopLocked останавливает ядро и дожидается, пока оно отпустит ресурсы: его
// горутины могли ещё писать в журнал, который мы закроем позже. Вызывается под mu.
func (d *Daemon) stopLocked() error {
	core := d.core
	if core == nil {
		return nil
	}

	if err := core.Stop(); err != nil {
		return fmt.Errorf("остановка ядра: %w", err)
	}

	if !waitClosed(core.Done()) {
		d.logger.Error("ядро не отпустило ресурсы вовремя")
	}

	d.core = nil

	return nil
}

// watch сообщает о падении ядра. Штатная остановка ошибки не даёт, поэтому в канал
// попадают только аварии.
func (d *Daemon) watch(core Core) {
	err := <-core.Done()
	if err == nil {
		return
	}

	select {
	case d.failures <- failure{core: core, err: err}:
	default:
	}
}

// waitClosed ждёт закрытия канала завершения и сообщает, дождался ли. Закрытие
// означает, что горутина ядра действительно закончила работу, а не просто
// сообщила об ошибке.
func waitClosed(done <-chan error) bool {
	timeout := time.After(stopTimeout)

	for {
		select {
		case _, ok := <-done:
			if !ok {
				return true
			}
		case <-timeout:
			return false
		}
	}
}
