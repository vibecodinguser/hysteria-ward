// Package cli собирает аргументы командной строки и превращает их в действия.
package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"

	"github.com/vibecodinguser/hysteria-ward/internal/api"
	"github.com/vibecodinguser/hysteria-ward/internal/build"
	"github.com/vibecodinguser/hysteria-ward/internal/certs"
	"github.com/vibecodinguser/hysteria-ward/internal/config"
	"github.com/vibecodinguser/hysteria-ward/internal/daemon"
	"github.com/vibecodinguser/hysteria-ward/internal/hycore"
	"github.com/vibecodinguser/hysteria-ward/internal/logging"
	"github.com/vibecodinguser/hysteria-ward/internal/metrics"
	"github.com/vibecodinguser/hysteria-ward/internal/ui"
	"github.com/vibecodinguser/hysteria-ward/internal/users"
)

// usage — справка. Имя программы подставляется из build.Name, чтобы оно
// не разъезжалось с именем бинаря.
var usage = fmt.Sprintf(`%[1]s — панель управления сервером Hysteria 2 на VPS.

Использование:
  %[1]s serve [флаги]
  %[1]s passwd [флаги]
  %[1]s setup [флаги]
  %[1]s acme [флаги]
  %[1]s config [флаги]
  %[1]s [флаг]

Флаги:
  --version  показать версию, коммит и целевую платформу
  --help     показать эту справку

Флаги serve:
  --db     файл базы (по умолчанию %[2]s)
  --log    файл журнала (по умолчанию %[3]s)
  --debug  отладочный запуск: если сертификата ещё нет, выписать самоподписанный

Флаги passwd:
  --db     файл базы (по умолчанию %[2]s)
  --name   имя администратора (по умолчанию текущее)

Флаги setup:
  --db                файл базы (по умолчанию %[2]s)
  --domain            домен сервера
  --email             e-mail для ACME
  --access            tunnel или public
  --masquerade        off, file или proxy
  --masquerade-file   каталог сайта для file
  --masquerade-proxy  адрес зеркала для proxy
  --acme-mode         способ проверки ACME: http или dns (по умолчанию http)
  --acme-directory    адрес ACME-каталога (по умолчанию Let's Encrypt)
  --acme-dns-provider DNS-провайдер для dns: hook или cloudflare
  --acme-dns-hook     программа, которая заводит записи TXT для dns
  --acme-dns-token-file файл с API-токеном Cloudflare (Zone.DNS Edit;
                       для поиска зоны ещё Zone.Zone Read)
  --acme-dns-zone-id  идентификатор зоны Cloudflare (необязательно)
  --password-stdin    прочитать пароль администратора из stdin
  --generate-password сгенерировать пароль и показать его один раз
  --check             только проверить ответы, ничего не записывать
  --cert, --key       пути сертификата и ключа

Флаги acme:
  --db         файл базы (по умолчанию %[2]s)
  --mode       способ проверки: http или dns
  --directory  адрес ACME-каталога
  --provider   DNS-провайдер: hook или cloudflare
  --hook       программа для записей TXT
  --token-file файл с API-токеном Cloudflare
  --zone-id    идентификатор зоны Cloudflare
  без флагов — показать текущие настройки

Флаги config:
  --db      файл базы (по умолчанию %[2]s)
  --check   только проверить настройки, ничего не записывать (по умолчанию)
  --repair  дополнить недостающие настройки умолчаниями и записать их

Пароль панель спрашивает у того, кто её ставит: с терминала — дважды и не показывая
набранное, из трубы — одной строкой. Панель перед сменой пароля нужно остановить:
пока она работает, база занята.

setup — команда установщика, а не второй интерфейс настройки: она записывает ответы
конфигуратора до первого запуска службы и не переписывает уже настроенную базу.
Домен, доступ и masquerade дальше меняются из интерфейса панели.

acme меняет способ проверки и DNS-провайдера на уже настроенной базе. Панель перед
этим нужно остановить; после записи — запустить снова.
`, build.Name, defaultDBPath, defaultLogPath)

// Где панель хранит базу и журнал по умолчанию. Пути в духе FHS: настройки и
// состояние — отдельно от программы, чтобы обновление не задевало данные.
const (
	defaultDBPath  = "/var/lib/hysteria-ward/hysteria-ward.db"
	defaultLogPath = "/var/log/hysteria-ward/hysteria-ward.log"
)

// Run выполняет команду и возвращает код возврата.
//
// Run намеренно не вызывает os.Exit и не пишет в глобальные потоки: это
// позволяет проверять поведение бинаря внутри процесса, без порождения
// дочерних процессов. Контекст приходит снаружи: в бою его отменяют SIGINT и
// SIGTERM, в тесте — явный вызов cancel. Потоки ввода-вывода тоже приходят
// снаружи: команда, спрашивающая пароль, иначе читала бы терминал мимо теста.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 1
	}

	switch args[0] {
	case "serve":
		return runServe(ctx, args[1:], stderr)

	case "passwd":
		return runPasswd(args[1:], stdin, stdout, stderr)

	case "setup":
		return runSetup(args[1:], stdin, stdout, stderr)

	case "acme":
		return runACME(args[1:], stdout, stderr)

	case "config":
		return runConfig(args[1:], stdout, stderr)

	case "--version":
		fmt.Fprintln(stdout, build.Current().String())
		return 0

	case "--help":
		fmt.Fprint(stdout, usage)
		return 0

	default:
		fmt.Fprintf(stderr, "неизвестный аргумент: %s\n", args[0])
		fmt.Fprint(stderr, "подсказка: --help\n")
		return 1
	}
}

// runServe поднимает панель и работает до отмены контекста.
func runServe(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)

	dbPath := fs.String("db", defaultDBPath, "файл базы")
	logPath := fs.String("log", defaultLogPath, "файл журнала")
	debug := fs.Bool("debug", false, "отладочный запуск: самоподписанный сертификат, пока нет настоящего")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	panel, err := daemon.New(daemon.Options{
		DBPath:    *dbPath,
		Log:       logging.Options{Path: *logPath},
		StartCore: startCore,
		Preflight: checkCoreConfig,
		StartPanel: func(opts daemon.PanelOptions) (daemon.Panel, error) {
			return startPanel(opts, *debug)
		},
	})
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	defer func() { _ = panel.Close() }()

	if err := panel.Run(ctx); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	return 0
}

// startPanel собирает HTTPS-сервер интерфейса.
//
// Сертификат берётся из настроек — тот же, что и у ядра. Пока настоящего нет,
// панель открывается с самоподписанным: иначе при провале ACME администратор
// остался бы без входа и не смог бы ничего исправить. Настоящий сертификат
// выпускается и продлевается в фоне, а оба слушателя берут его на ходу.
func startPanel(opts daemon.PanelOptions, debug bool) (daemon.Panel, error) {
	manager := certs.NewManager(opts.Config.TLS.Cert, opts.Config.TLS.Key)

	domain, err := opts.Store.Domain()
	if err != nil {
		return nil, err
	}

	settings, err := opts.Store.PanelSettings()
	if err != nil {
		return nil, err
	}
	if names := settings.Hosts(domain); len(names) != 0 {
		manager.SetNames(names)
	}

	certificate, err := certs.Load(opts.Config.TLS.Cert, opts.Config.TLS.Key)
	if err != nil {
		email, emailErr := opts.Store.ACMEEmail()
		if emailErr != nil {
			return nil, emailErr
		}

		acmeConfigured := domain != "" && email != ""

		if !debug && !acmeConfigured {
			return nil, err
		}

		opts.Logger.Warn("настоящий сертификат не читается, беру самоподписанный",
			"err", err, "acme", acmeConfigured)

		hosts := panelHosts(settings.Addr())
		hosts = append(hosts, settings.Hosts(domain)...)

		certificate, err = certs.SelfSigned(hosts...)
		if err != nil {
			return nil, err
		}

		manager.SetPlaceholder(certificate)
	} else {
		manager.Set(certificate)
	}

	// После «Применить» сертификат перечитывается с диска: файлы могли заменить,
	// и панель должна отдавать тот же сертификат, что ядро, и считать отпечаток
	// для новых ключей по нему.
	apply := func() error {
		err := opts.Apply()

		// Сертификат перечитывается и при отказе ядра: файл уже мог замениться, и
		// панель должна показывать то же, что лежит на диске.
		switch reloadErr := manager.Reload(); {
		case reloadErr == nil:
		case errors.Is(reloadErr, fs.ErrNotExist):
			// Сертификата ещё нет — это обычное состояние до выпуска, а не поломка.
			opts.Logger.Debug("сертификат ещё не выпущен")
		default:
			opts.Logger.Error("сертификат не перечитан", "err", reloadErr)
		}

		return err
	}

	background := acmeBackground(opts, manager, apply)

	return api.New(api.Options{
		Certificate:  certificate,
		Certificates: manager,
		Background:   background,
		Handler:      ui.Handler(),
		Auth:         opts.Auth,
		Store:        opts.Store,
		Users:        opts.Users,
		Metrics:      opts.Metrics,
		CoreRunning:  opts.CoreRunning,
		Apply:        apply,
		Logger:       opts.Logger,
	})
}

// acmeBackground собирает фоновый выпуск сертификата. Без домена и почты
// выпускать нечего, и панель работает со статическим сертификатом.
func acmeBackground(opts daemon.PanelOptions, manager *certs.Manager, apply func() error) func(context.Context) {
	domain, err := opts.Store.Domain()
	if err != nil {
		opts.Logger.Error("домен для ACME", "err", err)
		return nil
	}

	email, err := opts.Store.ACMEEmail()
	if err != nil {
		opts.Logger.Error("e-mail для ACME", "err", err)
		return nil
	}

	if domain == "" || email == "" {
		return nil
	}

	acmeSettings, err := opts.Store.ACMESettings()
	if err != nil {
		opts.Logger.Error("настройки ACME", "err", err)
		return nil
	}

	panelSettings, err := opts.Store.PanelSettings()
	if err != nil {
		opts.Logger.Error("настройки панели", "err", err)
		return nil
	}

	config := certs.ACMEConfig{
		Names:              panelSettings.Hosts(domain),
		Email:              email,
		DirectoryURL:       acmeSettings.Directory,
		AccountKeyPath:     certs.AccountKeyPath(opts.Config.TLS.Cert),
		CertPath:           opts.Config.TLS.Cert,
		KeyPath:            opts.Config.TLS.Key,
		Mode:               certs.Mode(acmeSettings.Mode),
		DNSProvider:        acmeSettings.DNSProvider,
		DNSHook:            acmeSettings.DNSHook,
		CloudflareAPIToken: acmeSettings.CloudflareAPIToken,
		CloudflareZoneID:   acmeSettings.CloudflareZoneID,
		Logger:             opts.Logger,
	}

	return func(ctx context.Context) {
		onRenew := func() {
			if err := apply(); err != nil {
				opts.Logger.Error("ядро не подхватило новый сертификат", "err", err)
			}
		}

		certs.Run(ctx, manager, config, onRenew)
	}
}

// panelHosts — имена, которые должен покрывать отладочный сертификат: панель
// открывают по тому адресу, который она слушает.
func panelHosts(listen string) []string {
	// Разбор может не сработать, а в адресе вроде «:8443» имени хоста нет вовсе.
	// Тогда остаётся имя по умолчанию: отладочный сертификат нужен ровно для того,
	// чтобы открыть панель с той же машины.
	//
	// Все интерфейсы — тоже не имя: покрывать сертификатом «0.0.0.0» нечего.
	host, _, err := net.SplitHostPort(listen)
	if err != nil || host == "" || host == "0.0.0.0" || host == "::" {
		return []string{"localhost"}
	}

	return []string{"localhost", host}
}

// checkCoreConfig проверяет то, что выясняется только перед запуском: читается ли
// сертификат и есть ли каталог подставного сайта. Панель спрашивает это до
// остановки работающего ядра, чтобы опечатка в пути не унесла с собой живой
// сервер.
func checkCoreConfig(cfg config.Config) error {
	if _, err := loadCertificate(cfg); err != nil {
		return err
	}

	return masqueradeOf(cfg.Masquerade).Validate()
}

// startCore собирает ядро Hysteria 2 по конфигурации из базы.
//
// Пользователи приходят списком в памяти: ядро спрашивает его о каждом
// подключении и каждом пакете, поэтому ходить за ними в базу на горячем пути
// нельзя. Список перечитывается после каждой правки в интерфейсе.
func startCore(cfg config.Config, registry *users.Registry, traffic *metrics.Collector) (daemon.Core, error) {
	certificate, err := loadCertificate(cfg)
	if err != nil {
		return nil, err
	}

	return hycore.Start(hycore.Config{
		ListenAddr:   cfg.Listen,
		TLS:          certificate,
		ObfsPassword: cfg.Obfs.Password,
		Masquerade:   masqueradeOf(cfg.Masquerade),
		Bandwidth: hycore.Bandwidth{
			Up:   cfg.Bandwidth.Up,
			Down: cfg.Bandwidth.Down,
		},
		Authenticator: registry,
		Traffic:       recorder{users: registry, metrics: traffic},
	})
}

// masqueradeOf переводит настройку панели в то, что нужно ядру. Словарь режимов
// у них общий: config и hycore — две стороны одной настройки, и расходиться им
// незачем.
func masqueradeOf(m config.Masquerade) hycore.Masquerade {
	return hycore.Masquerade{
		Mode:  hycore.MasqueradeMode(m.Mode),
		File:  m.File,
		Proxy: m.Proxy,
	}
}

// recorder — единственный приёмник, которого ждёт ядро: кого пускать, решает
// список пользователей, а сколько прошло — учёт трафика.
//
// Ядро умеет разговаривать только с одним приёмником, и развести эти две заботы
// по разным местам здесь незачем: обе отвечают на один и тот же вызов.
type recorder struct {
	users   *users.Registry
	metrics *metrics.Collector
}

// Traffic пропускает пакет и считает его только у того, кого пустили: трафик
// выключенного пользователя сервер не обслуживает, и в статистику он попасть не
// должен.
//
// Строка соединения — не идентификатор пользователя: в ней ещё и версия пароля.
// Пользователя называет список, а статистика получает уже его идентификатор.
func (r recorder) Traffic(id string, tx, rx uint64) bool {
	userID, ok := r.users.Traffic(id, tx, rx)
	if !ok {
		return false
	}

	r.metrics.Add(userID, tx, rx)

	return true
}

// Online передаёт состояние подключения списку пользователей: «в сети» в
// интерфейсе показывает именно он.
func (r recorder) Online(id string, online bool) {
	r.users.Online(id, online)
}

// loadCertificate читает сертификат панели. Читается при каждом запуске: «Применить»
// должно подхватывать и новый сертификат.
func loadCertificate(cfg config.Config) (tls.Certificate, error) {
	if cfg.TLS.Cert == "" || cfg.TLS.Key == "" {
		return tls.Certificate{}, errors.New("сертификат: не указано, где он лежит")
	}

	certificate, err := tls.LoadX509KeyPair(cfg.TLS.Cert, cfg.TLS.Key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("сертификат: %w", err)
	}

	return certificate, nil
}
