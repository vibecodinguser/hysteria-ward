package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// runACME меняет настройки выпуска сертификата на уже настроенной базе.
//
// setup применяет ответы только к пустой базе, поэтому без этой команды
// существующая панель не могла перейти на встроенный DNS-провайдер. База
// открывается на запись: службу нужно остановить, иначе bbolt не даст доступ.
func runACME(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("acme", flag.ContinueOnError)
	fs.SetOutput(stderr)

	dbPath := fs.String("db", defaultDBPath, "файл базы")
	mode := fs.String("mode", "", "способ проверки: http или dns")
	directory := fs.String("directory", "", "адрес ACME-каталога")
	provider := fs.String("provider", "", "DNS-провайдер: hook или cloudflare")
	hook := fs.String("hook", "", "программа для записей TXT")
	tokenFile := fs.String("token-file", "", "файл с API-токеном Cloudflare")
	zoneID := fs.String("zone-id", "", "идентификатор зоны Cloudflare")

	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "лишний аргумент: %s\n", fs.Arg(0))
		return 1
	}

	changed := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { changed[f.Name] = true })

	// --db есть почти всегда; показом считаются только вызовы без настроек.
	settingsChanged := false
	for name := range changed {
		if name != "db" {
			settingsChanged = true
		}
	}
	if !settingsChanged {
		return showACME(*dbPath, stdout, stderr)
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "база: %v\nбаза занята, если панель работает: остановите её и повторите\n", err)
		return 1
	}
	defer func() { _ = db.Close() }()

	settings, err := db.ACMESettings()
	if err != nil {
		fmt.Fprintf(stderr, "настройки ACME: %v\n", err)
		return 1
	}

	if changed["mode"] {
		settings.Mode = *mode
	}
	if changed["directory"] {
		settings.Directory = *directory
	}
	if changed["provider"] {
		settings.DNSProvider = *provider
	}
	if changed["hook"] {
		settings.DNSHook = *hook
	}
	if changed["zone-id"] {
		settings.CloudflareZoneID = *zoneID
	}

	// Переход на HTTP-01 не должен оставлять в базе хвосты DNS: иначе
	// Validate справедливо откажется сохранять настройки.
	if changed["mode"] && settings.Mode == "http" {
		if changed["provider"] || changed["hook"] || changed["token-file"] || changed["zone-id"] {
			fmt.Fprintln(stderr, "для проверки http настройки DNS не нужны")
			return 1
		}

		settings.DNSProvider = ""
		settings.DNSHook = ""
		settings.CloudflareAPIToken = ""
		settings.CloudflareZoneID = ""
	}

	// Если способ проверки не указан, но меняется DNS-настройка, считаем, что
	// администратор включает DNS-01: иначе флаг провайдера молча отвергся бы.
	if !changed["mode"] && settings.Mode == "http" &&
		(changed["provider"] || changed["hook"] || changed["token-file"] || changed["zone-id"]) {
		settings.Mode = "dns"
	}

	if settings.Mode == "dns" && settings.DNSProvider == "" {
		settings.DNSProvider = "hook"
	}

	if changed["provider"] {
		switch settings.DNSProvider {
		case "cloudflare":
			if changed["hook"] {
				fmt.Fprintln(stderr, "для DNS-01 Cloudflare не нужен --hook")
				return 1
			}
			settings.DNSHook = ""
		case "hook":
			if changed["token-file"] || changed["zone-id"] {
				fmt.Fprintln(stderr, "для DNS-01 с хуком не нужны настройки Cloudflare")
				return 1
			}
			settings.CloudflareAPIToken = ""
			settings.CloudflareZoneID = ""
		}
	}

	if changed["token-file"] {
		if settings.Mode != "dns" || settings.DNSProvider != "cloudflare" {
			fmt.Fprintln(stderr, "--token-file нужен только для DNS-01 через Cloudflare")
			return 1
		}
		token, err := readCloudflareToken(*tokenFile)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		settings.CloudflareAPIToken = token
	}

	if changed["zone-id"] && settings.DNSProvider != "cloudflare" {
		fmt.Fprintln(stderr, "--zone-id нужен только для DNS-01 через Cloudflare")
		return 1
	}
	if changed["hook"] && settings.DNSProvider != "hook" {
		fmt.Fprintln(stderr, "--hook нужен только для DNS-01 с хуком")
		return 1
	}

	if err := settings.Validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if settings.Mode == "dns" {
		switch settings.DNSProvider {
		case "hook":
			if err := checkDNSHook(settings.DNSHook); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
		case "cloudflare":
			if settings.CloudflareAPIToken == "" {
				fmt.Fprintln(stderr, "для DNS-01 через Cloudflare нужен --token-file")
				return 1
			}
		}
	}

	if err := db.SaveACMESettings(settings); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "ACME: %s\n", acmeText(settings))
	fmt.Fprintln(stdout, "перезапустите службу, чтобы панель применила новые настройки:")
	fmt.Fprintln(stdout, "  systemctl restart hysteria-ward")

	return 0
}

// showACME показывает текущие настройки без изменений и без секрета.
func showACME(dbPath string, stdout, stderr io.Writer) int {
	db, err := store.OpenReadOnly(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "база: %v\nесли панель работает, остановите её и повторите: база занята\n", err)
		return 1
	}
	defer func() { _ = db.Close() }()

	settings, err := db.ACMESettings()
	if err != nil {
		fmt.Fprintf(stderr, "настройки ACME: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "ACME: %s\n", acmeText(settings))
	if settings.Mode == "dns" && settings.DNSProvider == "cloudflare" && settings.CloudflareZoneID != "" {
		fmt.Fprintf(stdout, "зона Cloudflare: %s\n", settings.CloudflareZoneID)
	}
	if settings.Directory != "" {
		fmt.Fprintf(stdout, "каталог ACME: %s\n", settings.Directory)
	}
	if settings.CloudflareAPIToken != "" {
		fmt.Fprintln(stdout, "токен Cloudflare: задан")
	}

	return 0
}

// readCloudflareToken читает секрет из файла. Токен не принимается аргументом:
// аргументы видны в списке процессов и остаются в истории оболочки.
func readCloudflareToken(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("файл с токеном Cloudflare: %w", err)
	}
	if len(raw) > 4096 {
		return "", errors.New("файл с токеном Cloudflare слишком большой: ожидается одна строка с токеном")
	}

	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", errors.New("файл с токеном Cloudflare пуст")
	}
	if strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("файл с токеном Cloudflare должен содержать одну строку без пробелов")
	}

	return token, nil
}

// checkDNSHook проверяет, что программу можно запустить от имени службы.
// Существование пути видно и без запуска, поэтому опечатка ловится до записи.
func checkDNSHook(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("хук DNS: %w", err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("хук DNS %q: нужен исполняемый файл", path)
	}

	return nil
}
