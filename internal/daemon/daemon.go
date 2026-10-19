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

	"github.com/vibecodinguser/hysteria-ward/internal/authn"
	"github.com/vibecodinguser/hysteria-ward/internal/config"
	"github.com/vibecodinguser/hysteria-ward/internal/logging"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
	"github.com/vibecodinguser/hysteria-ward/internal/users"
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

// Panel — HTTPS-сервер интерфейса панели.
type Panel interface {
	// Run обслуживает запросы до отмены контекста.
	Run(ctx context.Context) error
}

// Options — что открыть и как поднимать ядро.
type Options struct {
	// DBPath — файл базы.
	DBPath string

	// Log — куда писать журнал.
	Log logging.Options

	// StartCore поднимает ядро по конфигурации и списку пользователей. Отдельной
	// функцией, а не прямой зависимостью: панель не знает про Hysteria 2 ничего
	// лишнего, а тест подставляет ядро, которым можно управлять.
	//
	// Список пользователей передаётся ядру, а не копируется: правки из интерфейса
	// должны доходить до него на ходу, без перезапуска.
	StartCore func(config.Config, *users.Registry) (Core, error)

	// StartPanel поднимает HTTPS-сервер интерфейса. Всё, что ему нужно, приходит
	// в PanelOptions: у панели один набор зависимостей, и растёт он здесь, а не в
	// списке аргументов.
	StartPanel func(PanelOptions) (Panel, error)

	// Preflight проверяет то, что видно только перед самым запуском — например,
	// читается ли сертификат. Вызывается до остановки работающего ядра, чтобы
	// такая ошибка не унесла с собой живой сервер. Необязателен.
	Preflight func(config.Config) error
}

// PanelOptions — что нужно интерфейсу панели: конфигурация, журнал, вход,
// настройки и состояние ядра.
type PanelOptions struct {
	// Config — конфигурация панели.
	Config config.Config

	// Logger — журнал панели.
	Logger *slog.Logger

	// Auth — вход в панель.
	Auth *authn.Service

	// Store — база: кому и на каком порту слушать панель, а также остальное, что
	// понадобится следующему экрану интерфейса.
	Store *store.Store

	// Users — пользователи сервера: интерфейс заводит, правит и удаляет их, а
	// ядро тут же узнаёт об этом.
	Users *users.Registry

	// CoreRunning сообщает, работает ли ядро: интерфейс показывает это
	// администратору.
	CoreRunning func() bool
}

// Daemon — работающая панель: база, журнал и ядро под общим управлением.
type Daemon struct {
	opts   Options
	db     *store.Store
	log    *logging.Log
	logger *slog.Logger
	auth   *authn.Service
	users  *users.Registry

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
	if opts.StartPanel == nil {
		return nil, errors.New("не задан запуск интерфейса")
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

	auth, err := authn.New(db, log.Logger())
	if err != nil {
		_ = db.Close()
		_ = log.Close()
		return nil, err
	}

	userList, err := users.New(db)
	if err != nil {
		_ = db.Close()
		_ = log.Close()
		return nil, err
	}

	d := &Daemon{
		opts:     opts,
		db:       db,
		log:      log,
		logger:   log.Logger(),
		auth:     auth,
		users:    userList,
		failures: make(chan failure, 1),
	}

	if err := d.ensureCredential(); err != nil {
		_ = d.Close()
		return nil, err
	}

	return d, nil
}

// ensureCredential заводит учётные данные по умолчанию, если их ещё нет, и
// предупреждает, пока действует пароль по умолчанию.
//
// Панель должна открываться сразу после установки, а не требовать настройки
// вслепую из командной строки. Плата за это — предупреждение в журнале и то, что
// наружу панель не выставлена: по умолчанию она слушает петлю.
func (d *Daemon) ensureCredential() error {
	inUse, err := d.auth.EnsureCredential()
	if err != nil {
		return err
	}

	if inUse {
		d.logger.Warn("действует пароль по умолчанию: смените его командой hysteria-ward passwd",
			"name", authn.DefaultName)
	}

	return nil
}

// Auth отдаёт вход в панель: он нужен тому, кто собирает интерфейс, а заводится
// здесь, потому что здесь же лежит база.
func (d *Daemon) Auth() *authn.Service {
	return d.auth
}

// Store отдаёт базу: настройки, пользователи и статистика нужны интерфейсу
// панели, и заводить для них отдельные обёртки незачем.
func (d *Daemon) Store() *store.Store {
	return d.db
}

// Users отдаёт список пользователей: он нужен и интерфейсу, и тесту, который
// проверяет, что правка доходит до ядра.
func (d *Daemon) Users() *users.Registry {
	return d.users
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

// Run открывает интерфейс, применяет конфигурацию и работает до отмены контекста
// (в бою — SIGINT или SIGTERM). Штатное завершение возвращает nil.
//
// Неудачное применение Run не прерывает: причина уходит в журнал, а панель
// остаётся доступной, чтобы конфигурацию можно было починить. Недоступный
// интерфейс, наоборот, прерывает: без него администратору некуда войти, и о
// поломке лучше узнать тому, кто панель запускал.
func (d *Daemon) Run(ctx context.Context) error {
	panelCtx, stopPanel := context.WithCancel(ctx)
	defer stopPanel()

	panelDone, err := d.openPanel(panelCtx)
	if err != nil {
		return err
	}

	_ = d.Apply()

	panelErr := d.waitForStop(ctx, panelDone)

	// Остановка идёт в обратном порядке: сначала ядро, потом интерфейс — он
	// рассказывает о том, что происходило с ядром, и должен пережить его.
	stopPanel()

	coreErr := d.stopCore()

	// Интерфейс дожидается обязательно: журнал и база закрываются вызывающим, и
	// интерфейс не должен писать в уже закрытый журнал.
	if err := errors.Join(panelErr, coreErr, waitForPanel(panelDone)); err != nil {
		return err
	}

	d.logger.Info("панель остановлена")

	return nil
}

// waitForStop ждёт повода остановиться: отмены контекста или отказа интерфейса.
//
// Падение ядра поводом не считается: панель работает и без ядра, чтобы
// администратор мог починить конфигурацию. Возвращается ошибка интерфейса, если
// остановиться пришлось из-за неё; nil означает, что панель просто попросили
// закрыться.
func (d *Daemon) waitForStop(ctx context.Context, panelDone <-chan error) error {
	for {
		select {
		case <-ctx.Done():
			return nil

		case err := <-panelDone:
			// Отмена контекста закрывает и интерфейс, поэтому в этой ветке ошибки
			// может не быть — тогда это та же штатная остановка.
			if err == nil {
				return nil
			}

			return fmt.Errorf("интерфейс остановлен: %w", err)

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

// stopCore останавливает ядро, если оно поднято.
func (d *Daemon) stopCore() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.stopLocked()
}

// openPanel поднимает HTTPS-сервер интерфейса и возвращает канал с кодом его
// завершения.
//
// Конфигурация для интерфейса не проверяется на годность целиком: негодная
// конфигурация ядра — не причина оставлять администратора без входа.
func (d *Daemon) openPanel(ctx context.Context) (<-chan error, error) {
	cfg, err := d.db.Config()
	if err != nil {
		return nil, err
	}

	panel, err := d.opts.StartPanel(PanelOptions{
		Config:      cfg,
		Logger:      d.logger,
		Auth:        d.auth,
		Store:       d.db,
		Users:       d.users,
		CoreRunning: d.CoreRunning,
	})
	if err != nil {
		return nil, fmt.Errorf("интерфейс не поднялся: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- panel.Run(ctx)
		close(done)
	}()

	return done, nil
}

// waitForPanel дожидается закрытия интерфейса. Канал закрывает горутина
// интерфейса, поэтому повторное чтение не ждёт: значение могло быть разобрано
// раньше.
func waitForPanel(panelDone <-chan error) error {
	err := <-panelDone
	if err != nil {
		return fmt.Errorf("интерфейс остановлен: %w", err)
	}

	return nil
}

// Apply перечитывает конфигурацию из базы и перезапускает ядро. Активные
// соединения при этом обрываются — об этом администратора предупреждает интерфейс.
//
// Свой слушатель Apply не трогает: адрес и сертификат интерфейса берутся при
// запуске панели. Смена адреса вступает в силу после перезапуска — открытый
// канал, по которому пришла сама команда «Применить», рвать нельзя.
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
	core, err := d.opts.StartCore(cfg, d.users)
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
