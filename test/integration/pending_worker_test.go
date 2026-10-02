//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wager"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/postgres"
	pendingworker "github.com/marcosarl1/backend-challenge-go/internal/workers/pending"
)

func insertPendingBet(t *testing.T, runner postgres.Runner, playerID, walletID uuid.UUID, externalID string) {
	t.Helper()
	betTx, err := buildPendingBet(playerID, walletID, externalID)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if err := runner.Do(context.Background(), func(ctx context.Context, r application.Repositories) error {
		_, err := r.Wagers.Insert(ctx, betTx, "corr-manual")
		return err
	}); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
}

func txStatus(t *testing.T, txID uuid.UUID) wager.Status {
	t.Helper()
	conn := connect(t, ownerURL(t))
	var status string
	if err := conn.QueryRow(context.Background(), `SELECT status FROM wager_transactions WHERE id = $1`,
		txID.String()).Scan(&status); err != nil {
		t.Fatalf("lendo: %v", err)
	}
	return wager.Status(status)
}

func TestPendingWorkerResolvesBatch(t *testing.T) {
	cleanPendingTx(t)
	runner := openRunner(t)
	worker := pendingworker.NewWorker(runner, application.SystemClock{}, application.UUIDv7Generator{}, 10, 0)
	ctx := context.Background()
	walletID, playerID := fundWallet(t, runner, "1000.00")
	refExt := "bet2-" + uuid.NewString()

	// Reembolso esperando aposta que ainda não existe
	refund := runProcess(t, runner, processRefCmd(t, walletID, playerID, wager.KindRefund, "40.00", refExt))
	if refund.Status != wager.StatusPendingReference {
		t.Fatalf("estado = %s", refund.Status)
	}
	// ...e a aposta inserida à mão como pendente (aceite interrompido).
	insertPendingBet(t, runner, playerID, walletID, refExt)
	makeDueExt(t, refExt)

	// Uma passada: a aposta manual conclui (o reembolso ainda não venceu).
	n, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if n != 1 {
		t.Fatalf("retomadas = %d", n)
	}
	conn := connect(t, ownerURL(t))
	var betStatus string
	if err := conn.QueryRow(ctx, `SELECT status FROM wager_transactions WHERE external_transaction_id = $1`,
		refExt).Scan(&betStatus); err != nil || betStatus != string(wager.StatusProcessed) {
		t.Fatalf("aposta=%s, %v", betStatus, err)
	}
	// Outra passada (vencida): o reembolso conclui com o crédito de volta.
	makeDue(t, refund.TransactionID)
	n, err = worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if n < 1 {
		t.Fatalf("retomadas = %d", n)
	}
	if st := txStatus(t, refund.TransactionID); st != wager.StatusProcessed {
		t.Fatalf("reembolso = %s", st)
	}
	if balance := countBalance(t, walletID); balance != "1000.00" {
		t.Fatalf("saldo = %s", balance)
	}
}

func TestPendingWorkerFreshInstanceResumes(t *testing.T) {
	cleanPendingTx(t)
	runner := openRunner(t)
	walletID, playerID := fundWallet(t, runner, "1000.00")
	ext := "bet-nova-" + uuid.NewString()
	insertPendingBet(t, runner, playerID, walletID, ext)
	makeDueExt(t, ext)

	// Outra instância (runner novo) retoma do banco, sem memória compartilhada.
	fresh := pendingworker.NewWorker(openRunner(t), application.SystemClock{}, application.UUIDv7Generator{}, 10, 0)
	n, err := fresh.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if n < 1 {
		t.Fatalf("retomadas = %d", n)
	}
	if balance := countBalance(t, walletID); balance != "960.00" {
		t.Fatalf("saldo = %s", balance)
	}
}

func TestPendingWorkerRunLoop(t *testing.T) {
	cleanPendingTx(t)
	runner := openRunner(t)
	walletID, playerID := fundWallet(t, runner, "1000.00")
	ext := "bet-loop-" + uuid.NewString()
	insertPendingBet(t, runner, playerID, walletID, ext)
	makeDueExt(t, ext)

	worker := pendingworker.NewWorker(runner, application.SystemClock{}, application.UUIDv7Generator{}, 10, 50*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	conn := connect(t, ownerURL(t))
	var status string
	if err := conn.QueryRow(context.Background(), `SELECT status FROM wager_transactions WHERE external_transaction_id = $1`,
		ext).Scan(&status); err != nil || status != string(wager.StatusProcessed) {
		t.Fatalf("estado=%s, %v", status, err)
	}
}
