// Package cli собирает аргументы командной строки и превращает их в действия.
package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"

	"github.com/vibecodinguser/hysteria-ward/internal/api"
	"github.com/vibecodinguser/hysteria-ward/internal/build"
	"github.com/vibecodinguser/hysteria-ward/internal/certs"
	"github.com/vibecodinguser/hysteria-ward/internal/config"
	"github.com/vibecodinguser/hysteria-ward/internal/daemon"
	"github.com/vibecodinguser/hysteria-ward/internal/hycore"
	"github.com/vibecodinguser/hysteria-ward/internal/logging"
	"github.com/vibecodinguser/hysteria-ward/internal/ui"
)

// usage — справка. Имя программы подставляется из build.Name, чтобы оно
// не разъезжалось с именем бинаря.
var usage = fmt.Sprintf(`%[1]s — панель управления сервером Hysteria 2 на VPS.

Использование:
  %[1]s serve [флаги]
  %[1]s passwd [флаги]
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

Пароль панель спрашивает у того, кто её ставит: с терминала — дважды и не показывая
набранное, из трубы — одной строкой. Панель перед сменой пароля нужно остановить:
пока она работает, база занята.

Сам сервер настраивается в базе: адрес ядра, адрес панели, сертификат, обфускация
и masquerade хранятся там и меняются из интерфейса панели.
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
// Сертификат берётся из настроек — тот же, что и у ядра. В отладочном запуске,
// если настоящего сертификата ещё нет, выписывается самоподписанный: браузер его
// не признает, но посмотреть панель до появления домена можно.
func startPanel(opts daemon.PanelOptions, debug bool) (daemon.Panel, error) {
	certificate, err := loadCertificate(opts.Config)
	if err != nil {
		if !debug {
			return nil, err
		}

		opts.Logger.Warn("настоящий сертификат не читается, беру самоподписанный", "err", err)

		certificate, err = certs.SelfSigned(panelHosts(opts.Config.Panel.Listen)...)
		if err != nil {
			return nil, err
		}
	}

	return api.New(api.Options{
		Listen:      opts.Config.Panel.Listen,
		Certificate: certificate,
		Handler:     ui.Handler(),
		Auth:        opts.Auth,
		CoreRunning: opts.CoreRunning,
		Logger:      opts.Logger,
	})
}

// panelHosts — имена, которые должен покрывать отладочный сертификат: панель
// открывают по тому адресу, который она слушает.
func panelHosts(listen string) []string {
	// Разбор может не сработать, а в адресе вроде «:8443» имени хоста нет вовсе.
	// Тогда остаётся имя по умолчанию: отладочный сертификат нужен ровно для того,
	// чтобы открыть панель с той же машины.
	host, _, err := net.SplitHostPort(listen)
	if err != nil || host == "" {
		return []string{"localhost"}
	}

	return []string{"localhost", host}
}

// checkCoreConfig проверяет то, что выясняется только перед запуском: читается ли
// сертификат. Панель спрашивает это до остановки работающего ядра, чтобы опечатка
// в пути не унесла с собой живой сервер.
func checkCoreConfig(cfg config.Config) error {
	if _, err := loadCertificate(cfg); err != nil {
		return err
	}

	return nil
}

// startCore собирает ядро Hysteria 2 по конфигурации из базы.
func startCore(cfg config.Config) (daemon.Core, error) {
	certificate, err := loadCertificate(cfg)
	if err != nil {
		return nil, err
	}

	return hycore.Start(hycore.Config{
		ListenAddr:    cfg.Listen,
		TLS:           certificate,
		ObfsPassword:  cfg.Obfs.Password,
		Authenticator: noUsers{},
		Traffic:       noTraffic{},
	})
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

// noUsers отвергает любой ключ: пользователей ещё нет, они появятся вместе с
// выпуском ключей.
type noUsers struct{}

func (noUsers) Auth(string) (string, bool) {
	return "", false
}

// noTraffic принимает счётчики и никуда их не пишет: статистика появится вместе
// с пользователями.
type noTraffic struct{}

func (noTraffic) Traffic(string, uint64, uint64) bool {
	return true
}

func (noTraffic) Online(string, bool) {}
