package main

import (
	"go.uber.org/fx"

	"github.com/marcosarl1/backend-challenge-go/internal/platform"
	"github.com/marcosarl1/backend-challenge-go/internal/platform/config"
)

func main() {
	cfg := config.Load()
	fx.New(platform.Module, fx.StopTimeout(cfg.ShutdownTimeout)).Run()
}
