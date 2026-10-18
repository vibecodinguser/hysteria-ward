// Команда hysteria-ward — панель управления сервером Hysteria 2 на VPS.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/vibecodinguser/hysteria-ward/internal/cli"
)

// main намеренно тонкий: вся логика живёт в cli.Run, который можно вызвать из
// теста. Здесь остаётся только то, что тестировать нельзя — реальные потоки
// процесса, сигналы и код возврата.
func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
}
