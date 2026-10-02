package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wager"
)

func TestGetTransactionAllStates(t *testing.T) {
	runner := openRunner(t)
	ctx := context.Background()
	walletID, playerID := fundWallet(t, runner, "1000.00")

	// Processada: com resultado.
	bet := processCmd(t, walletID, playerID, wager.KindBet, "25.00")
	processed := runProcess(t, runner, bet)
	view, err := application.GetTransaction(ctx, runner, processed.TransactionID)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if view.Status != wager.StatusProcessed || !view.HasResult ||
		view.ResultBalance.String() != "975.00" || view.ResultWalletVersion != 2 ||
		view.Kind != wager.KindBet || view.FailureCode != "" {
		t.Fatalf("processada = %+v", view)
	}

	// Rejeitada: com código, sem resultado.
	poor := processCmd(t, walletID, playerID, wager.KindBet, "5000.00")
	rejected := runProcess(t, runner, poor)
	view, err = application.GetTransactionByExternal(ctx, runner, "provider-a", poor.ExternalID)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if view.Status != wager.StatusRejected || view.FailureCode != wager.CodeInsufficientFunds ||
		view.HasResult || view.TransactionID != rejected.TransactionID {
		t.Fatalf("rejeitada = %+v", view)
	}

	// Em espera: com tentativas e agenda.
	refUniq := uuid.NewString()
	waiting := runProcess(t, runner, processRefCmd(t, walletID, playerID, wager.KindRefund, "10.00", "bet-w-"+refUniq))
	view, err = application.GetTransaction(ctx, runner, waiting.TransactionID)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if view.Status != wager.StatusPendingReference || view.Attempts != 1 ||
		view.NextAttemptAt.IsZero() || view.ExpiresAt.IsZero() {
		t.Fatalf("espera = %+v", view)
	}

	// Pendente crua: inserida sem processar (aceite assíncrono interrompido).
	pendingTx, err := buildPendingBet(playerID, walletID, "bet-crua-"+refUniq)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if err := runner.Do(ctx, func(ctx context.Context, r application.Repositories) error {
		_, err := r.Wagers.Insert(ctx, pendingTx, "corr-crua")
		return err
	}); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	view, err = application.GetTransactionByExternal(ctx, runner, "provider-a", "bet-crua-"+refUniq)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if view.Status != wager.StatusPending || view.HasResult || view.FailureCode != "" {
		t.Fatalf("pendente = %+v", view)
	}

	// Falha permanente: marcada direto no domínio e gravada.
	if err := runner.Do(ctx, func(ctx context.Context, r application.Repositories) error {
		found, err := r.Wagers.FindByProviderExternal(ctx, "provider-a", "bet-crua-"+refUniq)
		if err != nil {
			return err
		}
		if err := found.Fail(application.SystemClock{}.Now()); err != nil {
			return err
		}
		return r.Wagers.Save(ctx, found)
	}); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	view, err = application.GetTransaction(ctx, runner, pendingTx.ID())
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if view.Status != wager.StatusFailed || view.FailureCode != wager.CodeInternalPermanentFailure {
		t.Fatalf("falha = %+v", view)
	}
}

func TestGetTransactionNotFound(t *testing.T) {
	runner := openRunner(t)
	ctx := context.Background()
	ghost, _ := uuid.NewV7()
	if _, err := application.GetTransaction(ctx, runner, ghost); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("por id erro = %v", err)
	}
	if _, err := application.GetTransactionByExternal(ctx, runner, "provider-a", "ext-fantasma"); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("por externo erro = %v", err)
	}
}
