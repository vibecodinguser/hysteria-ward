package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/vibecodinguser/hysteria-ward/internal/config"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// runConfig показывает и приводит в порядок настройки, которые панель читает из
// базы. Это не второй интерфейс настройки: команда не спрашивает домен и пароль
// и не меняет пользователей, а проверяет, что хранилище понимается текущей
// версией, и дополняет недостающие поля умолчаниями.
func runConfig(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	fs.SetOutput(stderr)

	dbPath := fs.String("db", defaultDBPath, "файл базы")
	check := fs.Bool("check", false, "только проверить настройки, ничего не записывать")
	repair := fs.Bool("repair", false, "дополнить недостающие настройки умолчаниями и записать их")

	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "лишний аргумент: %s\n", fs.Arg(0))
		return 1
	}
	if *check && *repair {
		fmt.Fprintln(stderr, "проверка и починка — разные действия: выберите одно")
		return 1
	}

	if *repair {
		return repairConfig(*dbPath, stdout, stderr)
	}

	// По умолчанию — проверка без записи. Так команду можно звать при работающей
	// панели и не бояться, что она что-то изменит.
	return checkConfig(*dbPath, stdout, stderr)
}

// checkConfig читает настройки и проверяет их. База открывается только для
// чтения: работающая панель держит её на записи.
func checkConfig(dbPath string, stdout, stderr io.Writer) int {
	db, err := store.OpenReadOnly(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "база: %v\nесли панель работает, остановите её и повторите: база занята\n", err)
		return 1
	}
	defer func() { _ = db.Close() }()

	summary, err := readSettings(db)
	if err != nil {
		fmt.Fprintf(stderr, "настройки: %v\n", err)
		return 1
	}

	printSettings(stdout, summary)

	return 0
}

// repairConfig дополняет настройки умолчаниями и записывает их обратно.
//
// База должна быть свободна: панель держит её на записи. Пользователи, пароль,
// домен и сертификаты не трогаются — меняются только недостающие поля настроек.
func repairConfig(dbPath string, stdout, stderr io.Writer) int {
	if _, err := os.Stat(dbPath); err != nil {
		fmt.Fprintf(stderr, "база: %v\n", err)
		return 1
	}

	db, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "база: %v\nбаза занята, если панель работает: остановите её и повторите\n", err)
		return 1
	}
	defer func() { _ = db.Close() }()

	cfg, err := db.Config()
	if err != nil {
		fmt.Fprintf(stderr, "конфигурация сервера: %v\n", err)
		return 1
	}
	cfg = cfg.FillDefaults()
	if cfg.TLS.Cert == "" {
		cfg.TLS.Cert = defaultCertPath
	}
	if cfg.TLS.Key == "" {
		cfg.TLS.Key = defaultKeyPath
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(stderr, "конфигурация сервера: %v\n", err)
		return 1
	}

	panel, err := db.PanelSettings()
	if err != nil {
		fmt.Fprintf(stderr, "настройки панели: %v\n", err)
		return 1
	}

	acme, err := db.ACMESettings()
	if err != nil {
		fmt.Fprintf(stderr, "настройки ACME: %v\n", err)
		return 1
	}

	if err := db.SaveConfig(cfg); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	if err := db.SavePanelSettings(panel); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	if err := db.SaveACMESettings(acme); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	summary, err := readSettings(db)
	if err != nil {
		fmt.Fprintf(stderr, "настройки: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "настройки приведены в порядок")
	printSettings(stdout, summary)

	return 0
}

// settingsSummary — то, что команда показывает о настройках.
type settingsSummary struct {
	domain      string
	email       string
	server      config.Config
	panel       store.PanelSettings
	acme        store.ACMESettings
	certState   string
	domainUnset bool
	emailUnset  bool
}

// readSettings собирает и проверяет настройки из базы.
func readSettings(db *store.Store) (settingsSummary, error) {
	domain, err := db.Domain()
	if err != nil {
		return settingsSummary{}, err
	}

	email, err := db.ACMEEmail()
	if err != nil {
		return settingsSummary{}, err
	}

	cfg, err := db.Config()
	if err != nil {
		return settingsSummary{}, err
	}
	if err := cfg.Validate(); err != nil {
		return settingsSummary{}, err
	}

	panel, err := db.PanelSettings()
	if err != nil {
		return settingsSummary{}, err
	}

	acme, err := db.ACMESettings()
	if err != nil {
		return settingsSummary{}, err
	}

	return settingsSummary{
		domain:      domain,
		email:       email,
		server:      cfg,
		panel:       panel,
		acme:        acme,
		certState:   certState(cfg.TLS.Cert, cfg.TLS.Key),
		domainUnset: domain == "",
		emailUnset:  email == "",
	}, nil
}

// printSettings показывает настройки без секретов: журнал и отчёт не должны
// содержать пароль администратора или пользователя.
func printSettings(w io.Writer, summary settingsSummary) {
	if summary.domainUnset {
		fmt.Fprintln(w, "домен: не задан")
	} else {
		fmt.Fprintf(w, "домен: %s\n", summary.domain)
	}

	if summary.emailUnset {
		fmt.Fprintln(w, "e-mail ACME: не задан")
	} else {
		fmt.Fprintf(w, "e-mail ACME: %s\n", summary.email)
	}

	fmt.Fprintf(w, "сервер: %s\n", summary.server.Listen)
	fmt.Fprintf(w, "TLS: %s, %s\n", summary.server.TLS.Cert, summary.server.TLS.Key)
	fmt.Fprintf(w, "masquerade: %s\n", summary.server.Masquerade.Mode)
	fmt.Fprintf(w, "панель: %s, %d\n", summary.panel.Access, summary.panel.Port)
	fmt.Fprintf(w, "ACME: %s", summary.acme.Mode)
	if summary.acme.Directory != "" {
		fmt.Fprintf(w, ", %s", summary.acme.Directory)
	}
	fmt.Fprintln(w)

	if summary.certState != "" {
		fmt.Fprintf(w, "сертификат: %s\n", summary.certState)
	}
}

// certState говорит, лежит ли на месте сертификат. Его отсутствие — не ошибка:
// до выпуска ACME панель работает с временным.
func certState(certPath, keyPath string) string {
	if _, err := os.Stat(certPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "ещё не выпущен"
		}

		return fmt.Sprintf("не читается: %v", err)
	}
	if _, err := os.Stat(keyPath); err != nil {
		return fmt.Sprintf("ключ не читается: %v", err)
	}

	return fmt.Sprintf("на месте (%s)", filepath.Base(certPath))
}
