package observability

import (
	"log/slog"
	"os"

	"go.uber.org/fx"
)

// Module monta o log em JSON (stdlib, sem dependência externa).
var Module = fx.Module("observability", fx.Provide(NewLogger))

// NewLogger entrega o JSON para a saída padrão.
func NewLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, nil))
}
