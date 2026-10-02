package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/domain"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/events"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wager"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wallet"
)

// Agregados que originam eventos.
const (
	AggregateWallet = "wallet"
	AggregateWager  = "wager_transaction"
)

// OpenWalletCommand abre a carteira do jogador na moeda do saldo inicial.
type OpenWalletCommand struct {
	PlayerID       uuid.UUID
	InitialBalance money.Money
	CorrelationID  string
}

// OpenWalletResult conta o que aconteceu: a carteira e se houve abertura com movimento (saldo inicial positivo) ou só a carteira (saldo zero).
type OpenWalletResult struct {
	WalletID uuid.UUID
	Balance  money.Money
	Version  int64
	Opened   bool
}

// OpenWallet abre a carteira e, com saldo inicial positivo, grava na mesma transação a carteira, a abertura processada, o lançamento de crédito e os dois eventos (operação processada e saldo alterado). Com zero, grava só a carteira — sem transação, sem lançamento, sem evento. Jogador e moeda repetidos conflitam.
func OpenWallet(ctx context.Context, uow UnitOfWork, clock Clock, ids IDGenerator, cmd OpenWalletCommand) (*OpenWalletResult, error) {
	if cmd.PlayerID == uuid.Nil {
		return nil, fmt.Errorf("%w: jogador vazio", ErrInvalidInput)
	}
	if !cmd.InitialBalance.Valid() {
		return nil, fmt.Errorf("%w: saldo inválido: %w", ErrInvalidInput, domain.ErrUninitialized)
	}
	zero, _ := money.Zero(cmd.InitialBalance.Currency())
	negative, err := cmd.InitialBalance.Cmp(zero)
	if err != nil {
		return nil, err
	}
	if negative < 0 {
		return nil, fmt.Errorf("%w: saldo negativo", ErrInvalidInput)
	}
	if cmd.CorrelationID == "" {
		return nil, fmt.Errorf("%w: correlação vazia", ErrInvalidInput)
	}
	now := clock.Now()
	if now.IsZero() {
		return nil, fmt.Errorf("%w: relógio vazio", ErrInvalidInput)
	}
	walletID, err := ids.NewID()
	if err != nil {
		return nil, fmt.Errorf("gerando carteira: %w", err)
	}
	withMovement, err := cmd.InitialBalance.Cmp(zero)
	if err != nil {
		return nil, err
	}

	result := &OpenWalletResult{WalletID: walletID, Balance: cmd.InitialBalance, Version: 1}
	err = uow.Do(ctx, func(ctx context.Context, r Repositories) error {
		w, err := wallet.NewWallet(walletID, cmd.PlayerID, cmd.InitialBalance.Currency(), now)
		if err != nil {
			return err
		}
		if err := r.Wallets.Insert(ctx, w); err != nil {
			var conflict *ConflictError
			if errors.As(err, &conflict) {
				return fmt.Errorf("%w: %s", ErrWalletExists, conflict.Constraint)
			}
			return err
		}
		if withMovement == 0 {
			return nil
		}
		return openWithMovement(ctx, r, ids, w, cmd, now, result)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func isConflict(err error, target **ConflictError) bool {
	type causer interface{ error }
	var _ causer
	for err != nil {
		if c, ok := err.(*ConflictError); ok {
			*target = c
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// openWithMovement grava abertura, lançamento e eventos na transação aberta.
func openWithMovement(ctx context.Context, r Repositories, ids IDGenerator, w *wallet.Wallet, cmd OpenWalletCommand, now time.Time, result *OpenWalletResult) error {
	openingID, err := ids.NewID()
	if err != nil {
		return fmt.Errorf("gerando abertura: %w", err)
	}
	entry, err := w.ApplyOpening(openingID, cmd.InitialBalance, now)
	if err != nil {
		return err
	}
	opening, err := wager.NewOpening(wager.OpeningParams{
		ID: openingID, WalletID: w.ID(), PlayerID: cmd.PlayerID, Amount: cmd.InitialBalance,
	}, now)
	if err != nil {
		return err
	}
	if _, err := r.Wagers.Insert(ctx, opening, cmd.CorrelationID); err != nil {
		return err
	}
	if err := r.Ledger.Insert(ctx, entry); err != nil {
		return err
	}
	processedID, err := ids.NewID()
	if err != nil {
		return fmt.Errorf("gerando evento: %w", err)
	}
	processed, err := events.NewTransactionProcessed(processedID, openingID, cmd.CorrelationID, "", now, events.ProcessedData{
		TransactionID: openingID, WalletID: w.ID(), PlayerID: cmd.PlayerID,
		Kind: string(wager.KindOpening), Amount: cmd.InitialBalance,
		ResultBalance: cmd.InitialBalance, ResultWalletVersion: 1,
	})
	if err != nil {
		return err
	}
	changedID, err := ids.NewID()
	if err != nil {
		return fmt.Errorf("gerando evento: %w", err)
	}
	changed, err := events.NewBalanceChanged(changedID, w.ID(), cmd.CorrelationID, "", now, events.BalanceChangedData{
		WalletID: w.ID(), TransactionID: openingID, Direction: events.DirectionCredit,
		Amount: cmd.InitialBalance, BalanceBefore: entry.BalanceBefore(),
		BalanceAfter: entry.BalanceAfter(), WalletVersion: 1,
	})
	if err != nil {
		return err
	}
	processedPayload, err := processed.PayloadJSON()
	if err != nil {
		return fmt.Errorf("serializando %s: %w", events.TypeTransactionProcessed, err)
	}
	changedPayload, err := changed.PayloadJSON()
	if err != nil {
		return fmt.Errorf("serializando %s: %w", events.TypeBalanceChanged, err)
	}
	for _, env := range []struct {
		id      uuid.UUID
		aggType string
		aggID   uuid.UUID
		typ     string
		payload []byte
	}{
		{processedID, AggregateWager, openingID, events.TypeTransactionProcessed, processedPayload},
		{changedID, AggregateWallet, w.ID(), events.TypeBalanceChanged, changedPayload},
	} {
		if err := r.Outbox.Insert(ctx, OutboxEvent{
			ID: env.id, AggregateType: env.aggType, AggregateID: env.aggID,
			EventType: env.typ, EventVersion: events.Version, OrderingKey: w.ID().String(),
			CorrelationID: cmd.CorrelationID, Payload: env.payload, OccurredAt: now,
		}, now); err != nil {
			return err
		}
	}
	result.Opened = true
	return nil
}
