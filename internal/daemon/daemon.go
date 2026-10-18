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
}

// Daemon — работающая панель: база, журнал и ядро под общим управлением.
type Daemon struct {
	opts Options
	db   *store.Store
	log  *logging.Log

	// mu охраняет core. Жизненный цикл ядра целиком идёт под ним, поэтому два
	// одновременных «Применить» не подерутся за порт.
	mu   sync.Mutex
	core Core

	// crashed принимает падения ядра. Буфер на один: если панель ещё не разобрала
	// прошлое падение, новое всё равно ничего не меняет.
	crashed chan error
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
		opts:    opts,
		db:      db,
		log:     log,
		crashed: make(chan error, 1),
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
// Если ядро не поднялось, Run продолжает работать: причина уходит в журнал, а
// панель остаётся доступной, чтобы конфигурацию можно было починить.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.Apply(); err != nil {
		d.log.Logger().Error("ядро не запустилось", "err", err)
	}

	for {
		select {
		case <-ctx.Done():
			d.mu.Lock()
			err := d.stopLocked()
			d.mu.Unlock()
			if err != nil {
				return err
			}

			d.log.Logger().Info("панель остановлена")
			return nil

		case err := <-d.crashed:
			d.log.Logger().Error("ядро упало", "err", err)

			d.mu.Lock()
			d.core = nil
			d.mu.Unlock()
		}
	}
}

// Apply перечитывает конфигурацию из базы и перезапускает ядро. Активные
// соединения при этом обрываются — об этом администратора предупреждает интерфейс.
//
// Конфигурация проверяется до остановки ядра: негодная правка не должна уносить
// с собой работающий сервер.
func (d *Daemon) Apply() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	cfg, err := d.configLocked()
	if err != nil {
		return err
	}

	if err := d.stopLocked(); err != nil {
		return err
	}

	if err := d.startLocked(cfg); err != nil {
		return err
	}

	d.log.Logger().Info("конфигурация применена")

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

// startLocked поднимает ядро по готовой конфигурации. Вызывается под mu.
func (d *Daemon) startLocked(cfg config.Config) error {
	core, err := d.opts.StartCore(cfg)
	if err != nil {
		return fmt.Errorf("запуск ядра: %w", err)
	}

	d.core = core
	go d.watch(core)

	d.log.Logger().Info("ядро запущено", "listen", cfg.Listen)

	return nil
}

// stopLocked останавливает ядро и дожидается, пока оно отпустит ресурсы: его
// горутины могли ещё писать в журнал, который мы закроем позже. Вызывается под mu.
func (d *Daemon) stopLocked() error {
	core := d.core
	d.core = nil

	if core == nil {
		return nil
	}

	if err := core.Stop(); err != nil {
		return fmt.Errorf("остановка ядра: %w", err)
	}

	if !waitClosed(core.Done()) {
		d.log.Logger().Error("ядро не отпустило ресурсы вовремя")
	}

	return nil
}

// watch сообщает о падении ядра. Штатная остановка ошибки не даёт, поэтому в
// канал попадают только аварии.
func (d *Daemon) watch(core Core) {
	err := <-core.Done()
	if err == nil {
		return
	}

	select {
	case d.crashed <- err:
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
