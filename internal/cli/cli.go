// Package cli собирает аргументы командной строки и превращает их в действия.
package cli

import (
	"fmt"
	"io"

	"github.com/vibecodinguser/hysteria-ward/internal/build"
)

// usage — справка. Имя программы подставляется из build.Name, чтобы оно
// не разъезжалось с именем бинаря.
var usage = fmt.Sprintf(`%[1]s — панель управления сервером Hysteria 2 на VPS.

Использование:
  %[1]s [флаг]

Флаги:
  --version  показать версию, коммит и целевую платформу
  --help     показать эту справку
`, build.Name)

// Run выполняет команду и возвращает код возврата.
//
// Run намеренно не вызывает os.Exit и не пишет в глобальные потоки: это
// позволяет проверять поведение бинаря внутри процесса, без порождения
// дочерних процессов.
//
// Run — диспетчер, а не только разбор флагов: «показать версию» и «запустить
// панель» являются ветвями одной команды, поэтому запуск панели появится
// здесь же отдельной ветвью, а не отдельной точкой входа.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 1
	}

	switch args[0] {
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
