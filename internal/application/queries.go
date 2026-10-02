package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wager"
)

// TransactionView é a transação como leitura: estado, código de falha,
// resultado guardado e agenda de espera, quando houver. Instante zerado e
// código vazio significam "não há".
type TransactionView struct {
	TransactionID       uuid.UUID
	ProviderID          string
	ExternalID          string
	IdempotencyKey      string
	WalletID            uuid.UUID
	PlayerID            uuid.UUID
	RoundID             string
	GameID              string
	Kind                wager.Kind
	Amount              money.Money
	ReferenceExternalID string
	Status              wager.Status
	FailureCode         wager.FailureCode
	FailureDetail       string
	HasResult           bool
	ResultBalance       money.Money
	ResultWalletVersion int64
	Attempts            int
	NextAttemptAt       time.Time
	ExpiresAt           time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// GetTransaction lê por id interno.
func GetTransaction(ctx context.Context, uow UnitOfWork, id uuid.UUID) (*TransactionView, error) {
	var out *TransactionView
	err := uow.Do(ctx, func(ctx context.Context, r Repositories) error {
		tx, err := r.Wagers.FindByID(ctx, id)
		if err != nil {
			return mapNotFound(err, "transação")
		}
		out = viewOf(tx)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetTransactionByExternal lê por provedor + id externo.
func GetTransactionByExternal(ctx context.Context, uow UnitOfWork, providerID, externalID string) (*TransactionView, error) {
	var out *TransactionView
	err := uow.Do(ctx, func(ctx context.Context, r Repositories) error {
		tx, err := r.Wagers.FindByProviderExternal(ctx, providerID, externalID)
		if err != nil {
			return mapNotFound(err, "transação")
		}
		out = viewOf(tx)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func mapNotFound(err error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrNotFound, what)
	}
	return err
}

func viewOf(tx *wager.WagerTransaction) *TransactionView {
	snap := tx.Snapshot()
	view := &TransactionView{
		TransactionID: snap.ID, ProviderID: snap.ProviderID, ExternalID: snap.ExternalID,
		IdempotencyKey: snap.IdempotencyKey, WalletID: snap.WalletID, PlayerID: snap.PlayerID,
		RoundID: snap.RoundID, GameID: snap.GameID, Kind: snap.Kind, Amount: snap.Amount,
		ReferenceExternalID: snap.ReferenceExtID, Status: snap.Status,
		FailureCode: snap.FailureCode, FailureDetail: snap.FailureDetail,
		Attempts: snap.Attempts, NextAttemptAt: snap.NextAttemptAt, ExpiresAt: snap.ExpiresAt,
		CreatedAt: snap.CreatedAt, UpdatedAt: snap.UpdatedAt,
	}
	if snap.Status == wager.StatusProcessed {
		view.HasResult = true
		view.ResultBalance = snap.ResultBalance
		view.ResultWalletVersion = snap.ResultVersion
	}
	return view
}
