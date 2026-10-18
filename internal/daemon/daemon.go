// Пакет daemon ведёт жизненный цикл панели: открывает базу и журнал, поднимает
// ядро Hysteria 2, ждёт сигнала остановки и аккуратно всё закрывает.
//
// Падение ядра не роняет панель: оно попадает в журнал, а панель продолжает
// работать — иначе одна авария унесла бы с собой интерфейс и управление.
package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/vibecodinguser/hysteria-ward/internal/logging"
	"github.com/vibecodinguser/hysteria-ward/internal/store"
)

// Core — запущенное ядро Hysteria 2. Наружу от него нужны только остановка и
// признак того, что оно перестало обслуживать.
type Core interface {
	Stop() error
	Done() <-chan error
}

// Options — что открыть и кого запустить.
type Options struct {
	DBPath     string
	LogPath    string
	MaxLogSize int64
	LogLines   int

	// StartCore поднимает ядро. Отдельным полем, а не прямой зависимостью:
	// тест подставляет сюда падающее ядро и проверяет, что панель выживает.
	StartCore func() (Core, error)
}

// Run работает до отмены контекста (в бою — SIGINT или SIGTERM). Штатное
// завершение возвращает nil.
func Run(ctx context.Context, opts Options) (err error) {
	if opts.StartCore == nil {
		return errors.New("не задан запуск ядра")
	}

	log, err := logging.Open(logging.Options{
		Path:    opts.LogPath,
		MaxSize: opts.MaxLogSize,
		Lines:   opts.LogLines,
	})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, log.Close()) }()

	db, err := store.Open(opts.DBPath)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()

	logger := log.Logger()

	core, err := opts.StartCore()
	if err != nil {
		logger.Error("ядро не запустилось", "err", err)
		return fmt.Errorf("запуск ядра: %w", err)
	}

	logger.Info("ядро запущено")

	coreDone := core.Done()
	alive := true

	for {
		select {
		case <-ctx.Done():
			if alive {
				if err := core.Stop(); err != nil {
					logger.Error("ядро не остановилось", "err", err)
					return fmt.Errorf("остановка ядра: %w", err)
				}
			}

			logger.Info("панель остановлена")
			return nil

		case err := <-coreDone:
			alive = false
			coreDone = nil

			if err == nil {
				logger.Info("ядро остановилось")
				continue
			}

			logger.Error("ядро упало", "err", err)
		}
	}
}
