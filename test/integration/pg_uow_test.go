//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/marcosarl1/backend-challenge-go/internal/infra/postgres"
)

func testPool(t *testing.T) *postgres.UnitOfWork {
	t.Helper()
	url := ownerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := postgres.Connect(ctx, url)
	if err != nil {
		t.Fatalf("conectando: %v", err)
	}
	t.Cleanup(pool.Close)
	return postgres.NewUnitOfWork(pool)
}

func TestUnitOfWorkCommitAndRollback(t *testing.T) {
	uow := testPool(t)
	ctx := context.Background()

	wid, pid := newIDs(t)
	// Confirma: o que rodou dentro aparece fora.
	if err := uow.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
			VALUES ($1, $2, 'BRL', 0, 1, now(), now())`, wid, pid)
		return err
	}); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	wid2, pid2 := newIDs(t)
	// Desfaz: erro da função apaga tudo que ela fez.
	fnErr := errors.New("negócio recusou")
	if err := uow.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
			VALUES ($1, $2, 'BRL', 0, 1, now(), now())`, wid2, pid2); err != nil {
			return err
		}
		return fnErr
	}); !errors.Is(err, fnErr) {
		t.Fatalf("erro = %v", err)
	}
}

func TestUnitOfWorkRetry(t *testing.T) {
	uow := testPool(t)
	ctx := context.Background()

	// Conflito de serialização repete do zero e conclui na segunda.
	attempts := 0
	err := uow.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		attempts++
		if attempts == 1 {
			return &pgconn.PgError{Code: "40001", Message: "sintético"}
		}
		return nil
	})
	if err != nil || attempts != 2 {
		t.Fatalf("tentativas = %d, erro = %v", attempts, err)
	}

	// Erro comum não repete: uma tentativa só.
	attempts = 0
	boom := errors.New("boom")
	if err := uow.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		attempts++
		return boom
	}); !errors.Is(err, boom) || attempts != 1 {
		t.Fatalf("tentativas = %d, erro = %v", attempts, err)
	}
}
