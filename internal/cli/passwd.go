package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/vibecodinguser/hysteria-ward/internal/authn"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// runPasswd задаёт пароль администратора.
//
// Флагом пароль не принимается: аргументы видны в списке процессов, и пароль в
// них — это пароль, отданный всем желающим. С терминала он спрашивается дважды и
// не показывается на экране, из трубы читается одной строкой: так установщик
// передаёт сгенерированный пароль, не светя его.
func runPasswd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("passwd", flag.ContinueOnError)
	fs.SetOutput(stderr)

	dbPath := fs.String("db", defaultDBPath, "файл базы")
	name := fs.String("name", "", "имя администратора (по умолчанию текущее)")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	if err := setPassword(*dbPath, *name, stdin, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	return 0
}

// setPassword открывает базу, меняет учётные данные и закрывает базу обратно.
func setPassword(dbPath, name string, stdin io.Reader, stdout, stderr io.Writer) error {
	db, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("%w\nбаза занята, если панель работает: остановите её и повторите", err)
	}
	defer func() { _ = db.Close() }()

	auth, err := authn.New(db, slog.New(slog.NewTextHandler(stderr, nil)))
	if err != nil {
		return err
	}

	password, err := askPassword(stdin, stderr)
	if err != nil {
		return err
	}

	admin := name
	if admin == "" {
		if admin, err = auth.AdminName(); err != nil {
			return err
		}
	}

	hash, err := authn.Hash(password)
	if err != nil {
		return err
	}

	if err := auth.SetCredential(authn.Credential{Name: admin, Hash: hash}); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "пароль администратора «%s» изменён\n", admin)

	return nil
}

// askPassword спрашивает пароль.
//
// С терминала — дважды и без эха, чтобы опечатка не осталась в пароле, а пароль —
// на экране. Из трубы — одной строкой и без подсказок: спрашивать дважды у
// установщика нечего, он передаёт то, что уже решил.
func askPassword(stdin io.Reader, stderr io.Writer) (string, error) {
	if file, ok := stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		return askFromTerminal(file, stderr)
	}

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

// askFromTerminal читает пароль дважды, не показывая набранное.
func askFromTerminal(terminal *os.File, stderr io.Writer) (string, error) {
	first, err := readHidden(terminal, stderr, "пароль: ")
	if err != nil {
		return "", err
	}

	second, err := readHidden(terminal, stderr, "повторите пароль: ")
	if err != nil {
		return "", err
	}

	if first != second {
		return "", errors.New("пароли не совпали")
	}

	if first == "" {
		return "", errors.New("пустой пароль")
	}

	return first, nil
}

// readHidden читает строку, не показывая её на экране. Пустая строка тоже
// возвращается: проверку на пустоту делает тот, кто спрашивает, — иначе он не
// отличил бы «ничего не ввели» от «не прочитали».
func readHidden(terminal *os.File, stderr io.Writer, prompt string) (string, error) {
	fmt.Fprint(stderr, prompt)

	password, err := term.ReadPassword(int(terminal.Fd()))
	fmt.Fprintln(stderr)

	if err != nil {
		return "", fmt.Errorf("чтение пароля: %w", err)
	}

	return string(password), nil
}
