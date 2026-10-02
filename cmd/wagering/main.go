package main

import (
	"log/slog"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/marcosarl1/backend-challenge-go/internal/platform"
	"github.com/marcosarl1/backend-challenge-go/internal/platform/config"
)

func main() {
	cfg := config.Load()
	fx.New(platform.Module, fx.StopTimeout(cfg.ShutdownTimeout),
		fx.WithLogger(func(logger *slog.Logger) fxevent.Logger {
			return &fxevent.SlogLogger{Logger: logger}
		}),
	).Run()
}
