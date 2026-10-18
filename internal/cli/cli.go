// Package cli собирает аргументы командной строки и превращает их в действия.
package cli

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"

	"github.com/vibecodinguser/hysteria-ward/internal/build"
	"github.com/vibecodinguser/hysteria-ward/internal/daemon"
	"github.com/vibecodinguser/hysteria-ward/internal/hycore"
	"github.com/vibecodinguser/hysteria-ward/internal/logging"
)

// usage — справка. Имя программы подставляется из build.Name, чтобы оно
// не разъезжалось с именем бинаря.
var usage = fmt.Sprintf(`%[1]s — панель управления сервером Hysteria 2 на VPS.

Использование:
  %[1]s serve [флаги]
  %[1]s [флаг]

Флаги:
  --version  показать версию, коммит и целевую платформу
  --help     показать эту справку

Флаги serve:
  --db      файл базы (по умолчанию /var/lib/hysteria-ward/hysteria-ward.db)
  --log     файл журнала (по умолчанию /var/log/hysteria-ward/hysteria-ward.log)
  --listen  адрес и порт ядра (по умолчанию :443)
  --cert    сертификат TLS
  --key     ключ TLS
`, build.Name)

// Run выполняет команду и возвращает код возврата.
//
// Run намеренно не вызывает os.Exit и не пишет в глобальные потоки: это
// позволяет проверять поведение бинаря внутри процесса, без порождения
// дочерних процессов. Контекст приходит снаружи: в бою его отменяют SIGINT и
// SIGTERM, в тесте — явный вызов cancel.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 1
	}

	switch args[0] {
	case "serve":
		return runServe(ctx, args[1:], stderr)

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

	dbPath := fs.String("db", "/var/lib/hysteria-ward/hysteria-ward.db", "файл базы")
	logPath := fs.String("log", "/var/log/hysteria-ward/hysteria-ward.log", "файл журнала")
	listenAddr := fs.String("listen", ":443", "адрес и порт ядра")
	certPath := fs.String("cert", "", "сертификат TLS")
	keyPath := fs.String("key", "", "ключ TLS")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *certPath == "" || *keyPath == "" {
		fmt.Fprintln(stderr, "сертификат не задан: укажите --cert и --key")
		return 1
	}

	certificate, err := tls.LoadX509KeyPair(*certPath, *keyPath)
	if err != nil {
		fmt.Fprintf(stderr, "сертификат: %v\n", err)
		return 1
	}

	err = daemon.Run(ctx, daemon.Options{
		DBPath: *dbPath,
		Log:    logging.Options{Path: *logPath},
		StartCore: func() (daemon.Core, error) {
			return hycore.Start(hycore.Config{
				ListenAddr:    *listenAddr,
				TLS:           certificate,
				Authenticator: noUsers{},
				Traffic:       noTraffic{},
			})
		},
	})
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	return 0
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
