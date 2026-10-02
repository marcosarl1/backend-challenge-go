package pending

import (
	"context"
	"log/slog"
	"time"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
)

// Worker retoma pendências em loop: espera por referência vencida e pendente órfã de interrupção. É só agendamento — quem decide é o RetryPending da aplicação, no banco (reiniciar o processo não perde nada).
type Worker struct {
	uow       application.UnitOfWork
	clock     application.Clock
	ids       application.IDGenerator
	batchSize int
	interval  time.Duration
	logger    *slog.Logger
}

// NewWorker monta com lote e intervalo de varredura.
func NewWorker(uow application.UnitOfWork, clock application.Clock, ids application.IDGenerator, batchSize int, interval time.Duration) *Worker {
	return NewWorkerWithLogger(uow, clock, ids, batchSize, interval, slog.Default())
}

// NewWorkerWithLogger monta o worker com logger estruturado.
func NewWorkerWithLogger(uow application.UnitOfWork, clock application.Clock, ids application.IDGenerator, batchSize int, interval time.Duration, logger *slog.Logger) *Worker {
	if batchSize < 1 {
		batchSize = 10
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{uow: uow, clock: clock, ids: ids, batchSize: batchSize, interval: interval, logger: logger}
}

// RunOnce faz uma passada e devolve quantas saíram da espera.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	return application.RetryPending(ctx, w.uow, w.clock, w.ids, w.batchSize)
}

// Run varre até o contexto acabar.
func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if n, err := w.RunOnce(ctx); err != nil {
				w.logger.ErrorContext(ctx, "retomada falhou", "error", err)
			} else if n > 0 {
				w.logger.InfoContext(ctx, "pendências retomadas", "count", n)
			}
		}
	}
}
