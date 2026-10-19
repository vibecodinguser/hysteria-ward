package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/vibecodinguser/hysteria-ward/internal/authn"
	"github.com/vibecodinguser/hysteria-ward/internal/config"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// Пути сертификата сервера по умолчанию. Установщик раскладывает каталоги по FHS,
// поэтому ответы конфигуратора сразу указывают туда, куда ACME положит файлы.
const (
	defaultCertPath = "/etc/hysteria-ward/certs/fullchain.pem"
	defaultKeyPath  = "/etc/hysteria-ward/certs/privkey.pem"
)

// runSetup сохраняет ответы конфигуратора установки.
//
// Это не второй интерфейс настройки: домен, доступ и masquerade меняются из
// панели. Команда нужна установщику, чтобы записать ответы до первого запуска
// службы, и умеет только начальную настройку — уже настроенную базу она не
// переписывает.
func runSetup(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(stderr)

	dbPath := fs.String("db", defaultDBPath, "файл базы")
	domain := fs.String("domain", "", "домен сервера")
	email := fs.String("email", "", "e-mail для ACME")
	access := fs.String("access", "", "режим доступа к панели: tunnel или public")
	mode := fs.String("masquerade", "", "режим masquerade: off, file или proxy")
	modeFile := fs.String("masquerade-file", "", "каталог сайта для режима file")
	modeProxy := fs.String("masquerade-proxy", "", "адрес зеркала для режима proxy")
	passwordStdin := fs.Bool("password-stdin", false, "прочитать пароль администратора из stdin")
	generate := fs.Bool("generate-password", false, "сгенерировать пароль и напечатать его один раз")
	check := fs.Bool("check", false, "только проверить ответы, ничего не записывать")
	certPath := fs.String("cert", defaultCertPath, "файл сертификата")
	keyPath := fs.String("key", defaultKeyPath, "файл ключа")

	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "лишний аргумент: %s\n", fs.Arg(0))
		return 1
	}

	if missing := missingSetupFlags(*domain, *email, *access, *mode); len(missing) != 0 {
		fmt.Fprintf(stderr, "не заданы обязательные флаги: %s\n", strings.Join(missing, ", "))
		return 1
	}

	cfg := config.Default()
	cfg.TLS = config.TLS{Cert: *certPath, Key: *keyPath}

	masquerade, err := setupMasquerade(*mode, *modeFile, *modeProxy)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	cfg.Masquerade = masquerade

	panel := store.DefaultPanelSettings()
	panel.Access = store.Access(*access)
	if err := panel.Validate(); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	setup := store.InitialSetup{
		Domain:    *domain,
		ACMEEmail: *email,
		Config:    cfg,
		Panel:     panel,
		Admin:     authn.Credential{Name: authn.DefaultName},
	}

	password := ""

	if *check {
		// Проверка ответов не должна съедать пароль: установщик вызывает её до
		// того, как передаст настоящий пароль команде настройки. Поэтому здесь
		// проверяется всё, кроме самого пароля, — его проверяет установщик.
		setup.Admin.Hash = "check"
		if _, err := store.ValidateInitialSetup(setup); err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			return 1
		}

		fmt.Fprintln(stdout, "проверка пройдена")
		return 0
	}

	switch {
	case *passwordStdin && *generate:
		fmt.Fprintln(stderr, "пароль задаётся либо --password-stdin, либо --generate-password")
		return 1
	case *passwordStdin:
		password, err = readPasswordLine(stdin)
		if err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			return 1
		}
	case *generate:
		password, err = authn.GeneratePassword()
		if err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			return 1
		}
	default:
		fmt.Fprintln(stderr, "не задан пароль администратора: нужен --password-stdin или --generate-password")
		return 1
	}

	// Пароль по умолчанию не годится даже как начальный: его знает кто угодно, а
	// панель после установки уже доступна по выбранному адресу.
	if password == authn.DefaultPassword {
		fmt.Fprintln(stderr, "пароль администратора не должен совпадать с паролем по умолчанию admin")
		return 1
	}

	hash, err := authn.Hash(password)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	setup.Admin.Hash = hash

	setup, err = store.ValidateInitialSetup(setup)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "%v\nбаза занята, если панель работает: остановите её и повторите\n", err)
		return 1
	}
	defer func() { _ = db.Close() }()

	if err := db.ApplyInitialSetup(setup); err != nil {
		if errors.Is(err, store.ErrAlreadyConfigured) {
			fmt.Fprintln(stderr, "панель уже настроена: домен, доступ и masquerade меняются из интерфейса")
			return 1
		}

		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	// Печатаются уже нормализованные ответы: ровно то, что легло на диск, а не
	// то, что ввели с лишними пробелами или заглавными буквами.
	fmt.Fprintf(stdout, "домен: %s\n", setup.Domain)
	fmt.Fprintf(stdout, "e-mail ACME: %s\n", setup.ACMEEmail)
	fmt.Fprintf(stdout, "панель: %s, порт %d\n", accessText(panel.Access), panel.Port)
	fmt.Fprintf(stdout, "masquerade: %s\n", masqueradeText(masquerade))

	if *generate {
		// Пароль печатается ровно один раз: в базе остался только хеш, и
		// восстановить отсюда нечего.
		fmt.Fprintf(stdout, "пароль администратора: %s\n", password)
	}

	return 0
}

// setupMasquerade собирает режим masquerade из ответов и проверяет то, что видно
// без запуска ядра: непустой путь, каталог на диске и адрес зеркала.
func setupMasquerade(mode, file, proxy string) (config.Masquerade, error) {
	masquerade := config.Masquerade{Mode: config.MasqueradeMode(mode)}

	switch masquerade.Mode {
	case config.MasqueradeOff:
		if file != "" || proxy != "" {
			return config.Masquerade{}, errors.New("для masquerade off не нужны --masquerade-file и --masquerade-proxy")
		}
	case config.MasqueradeFile:
		if file == "" {
			return config.Masquerade{}, errors.New("для masquerade file нужен --masquerade-file")
		}
		if proxy != "" {
			return config.Masquerade{}, errors.New("для masquerade file не нужен --masquerade-proxy")
		}

		info, err := os.Stat(file)
		if err != nil {
			if errors.Is(err, os.ErrPermission) {
				return config.Masquerade{}, fmt.Errorf("каталог сайта %q недоступен пользователю панели: %w", file, err)
			}

			return config.Masquerade{}, fmt.Errorf("каталог сайта: %w", err)
		}
		if !info.IsDir() {
			return config.Masquerade{}, fmt.Errorf("каталог сайта %q: это не каталог", file)
		}

		masquerade.File = file
	case config.MasqueradeProxy:
		if proxy == "" {
			return config.Masquerade{}, errors.New("для masquerade proxy нужен --masquerade-proxy")
		}
		if file != "" {
			return config.Masquerade{}, errors.New("для masquerade proxy не нужен --masquerade-file")
		}

		masquerade.Proxy = proxy
	default:
		return config.Masquerade{}, fmt.Errorf("masquerade: неизвестный режим %q", mode)
	}

	if err := masquerade.Validate(); err != nil {
		return config.Masquerade{}, err
	}

	return masquerade, nil
}

// missingSetupFlags перечисляет незаданные обязательные ответы в том порядке, в
// котором их спрашивает установщик.
func missingSetupFlags(domain, email, access, mode string) []string {
	var missing []string
	if domain == "" {
		missing = append(missing, "--domain")
	}
	if email == "" {
		missing = append(missing, "--email")
	}
	if access == "" {
		missing = append(missing, "--access")
	}
	if mode == "" {
		missing = append(missing, "--masquerade")
	}

	return missing
}

// accessText называет режим доступа так, как его понимает администратор.
func accessText(access store.Access) string {
	if access == store.AccessPublic {
		return "публичный"
	}

	return "SSH-туннель"
}

// masqueradeText называет режим masquerade для итогового отчёта.
func masqueradeText(masquerade config.Masquerade) string {
	switch masquerade.Mode {
	case config.MasqueradeFile:
		return "file, " + masquerade.File
	case config.MasqueradeProxy:
		return "proxy, " + masquerade.Proxy
	default:
		return "off"
	}
}

// readPasswordLine читает пароль одной строкой. Так установщик передаёт пароль,
// уже спрошенный у администратора, не показывая его в списке процессов.
func readPasswordLine(stdin io.Reader) (string, error) {
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("чтение пароля: %w", err)
	}

	password := strings.TrimRight(line, "\r\n")
	if password == "" {
		return "", errors.New("пустой пароль")
	}

	return password, nil
}
