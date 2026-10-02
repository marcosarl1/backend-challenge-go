package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marcosarl1/backend-challenge-go/internal/platform/observability"
)

// DBTX é o que os repositórios precisam: funciona com o pool (fora de transação) e com a transação (dentro da unidade de trabalho).
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Connect abre o pool e prova com um ping que o banco responde. Sem banco, a aplicação nem sobe (falha rápida no OnStart do Fx, mais adiante).
func Connect(ctx context.Context, connString string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("config do banco: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pool do banco: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping do banco: %w", err)
	}
	return pool, nil
}

// maxAttempts limita a repetição em serialização/conflito (tentativa inicial + repetições). A função precisa ser pura de efeitos fora da transação: repetir executa tudo de novo.
const maxAttempts = 3

// PingChecker prova que o banco responde, para a saúde do serviço.
type PingChecker struct {
	pool *pgxpool.Pool
}

// NewPingChecker monta o cheque sobre um pool aberto.
func NewPingChecker(pool *pgxpool.Pool) PingChecker {
	return PingChecker{pool: pool}
}

// Name identifica o cheque de saúde.
func (c PingChecker) Name() string { return "postgres" }

// Check pinga com prazo curto.
func (c PingChecker) Check(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return c.pool.Ping(pingCtx)
}

// UnitOfWork amarra vários repositórios na mesma transação: ou tudo confirma junto, ou nada confirma. Conflito de escrita (40001/40P01) tenta de novo do zero, em transação nova.
type UnitOfWork struct {
	pool    *pgxpool.Pool
	metrics *observability.Metrics
}

// NewUnitOfWork monta a unidade sobre um pool aberto.
func NewUnitOfWork(pool *pgxpool.Pool) *UnitOfWork {
	return &UnitOfWork{pool: pool}
}

// NewUnitOfWorkWithMetrics monta a unidade com métricas de conflitos e retries.
func NewUnitOfWorkWithMetrics(pool *pgxpool.Pool, metrics *observability.Metrics) *UnitOfWork {
	return &UnitOfWork{pool: pool, metrics: metrics}
}

// Do executa fn dentro de uma transação e confirma no fim. Erro da função
// desfaz tudo; erro transitório repete (limitado); o resto volta direto.
func (u *UnitOfWork) Do(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err = u.once(ctx, fn)
		if err == nil {
			return nil
		}
		var pgErr *pgconn.PgError
		if u.metrics != nil && errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01") {
			u.metrics.Conflict()
		}
		if Classify(err) != ClassTransient || attempt == maxAttempts {
			return err
		}
		if u.metrics != nil {
			u.metrics.Retry("postgres")
		}
	}
	return err
}

func (u *UnitOfWork) once(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tx, err := u.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("abrindo transação: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		if rb := tx.Rollback(ctx); rb != nil {
			return errors.Join(err, fmt.Errorf("desfazendo transação: %w", rb))
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("confirmando transação: %w", err)
	}
	return nil
}
