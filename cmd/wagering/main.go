package main

import (
	"go.uber.org/fx"

	"github.com/marcosarl1/backend-challenge-go/internal/platform"
)

func main() {
	fx.New(platform.Module).Run()
}
