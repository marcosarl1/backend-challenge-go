package integration

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wager"
)

func TestReconcileConsistent(t *testing.T) {
	runner := openRunner(t)
	ctx := context.Background()
	walletID, playerID := fundWallet(t, runner, "1000.00")
	runProcess(t, runner, processCmd(t, walletID, playerID, wager.KindBet, "25.00"))

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	rep, err := application.Reconcile(ctx, runner, logger, internalIdent(), walletID)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if !rep.Consistent || rep.Difference.String() != "0.00" ||
		rep.Stored.String() != "975.00" || rep.Calculated.String() != "975.00" ||
		rep.CheckedEntries != 2 {
		t.Fatalf("relatório = %+v", rep)
	}
	if logs.Len() != 0 {
		t.Fatalf("sem divergência não loga: %s", logs.String())
	}
}

func TestReconcileDivergence(t *testing.T) {
	runner := openRunner(t)
	ctx := context.Background()
	walletID, playerID := fundWallet(t, runner, "1000.00")
	runProcess(t, runner, processCmd(t, walletID, playerID, wager.KindBet, "25.00"))

	// Força a divergência como dono (sem a trigger da T2.3, o banco deixa).
	conn := connect(t, ownerURL(t))
	if _, err := conn.Exec(ctx, `UPDATE wallets SET balance_minor = 1 WHERE id = $1`,
		walletID.String()); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	rep, err := application.Reconcile(ctx, runner, logger, internalIdent(), walletID)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if rep.Consistent || rep.Difference.String() != "-974.99" ||
		rep.Stored.String() != "0.01" || rep.Calculated.String() != "975.00" {
		t.Fatalf("relatório = %+v", rep)
	}
	if !strings.Contains(logs.String(), "divergência na reconciliação") {
		t.Fatalf("divergência sem log: %s", logs.String())
	}

	// A reconciliação não mexe no saldo, nem para "consertar".
	var balance int64
	if err := conn.QueryRow(ctx, `SELECT balance_minor FROM wallets WHERE id = $1`,
		walletID.String()).Scan(&balance); err != nil || balance != 1 {
		t.Fatalf("saldo = %d, %v", balance, err)
	}
}

func TestReconcileNotFound(t *testing.T) {
	runner := openRunner(t)
	ghost, _ := uuid.NewV7()
	if _, err := application.Reconcile(context.Background(), runner, nil, internalIdent(), ghost); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("erro = %v", err)
	}
}
