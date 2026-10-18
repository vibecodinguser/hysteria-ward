// Команда hysteria-ward — панель управления сервером Hysteria 2 на VPS.
package main

import (
	"os"

	"github.com/vibecodinguser/hysteria-ward/internal/cli"
)

// main намеренно состоит из одной строки: вся логика живёт в cli.Run, который
// можно вызвать из теста. Здесь остаётся только то, что тестировать нельзя —
// передача реальных потоков процесса и код возврата.
func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
