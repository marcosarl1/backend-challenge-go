package application

import (
	"context"
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
// OpenWallet abre a carteira e, com saldo inicial positivo, grava na mesma transação a carteira, a abertura processada, o lançamento de crédito e os dois eventos (operação processada e saldo alterado). Com zero, grava só a carteira — sem transação, sem lançamento, sem evento. Jogador e moeda repetidos conflitam. Só o serviço interno abre carteira.
func OpenWallet(ctx context.Context, uow UnitOfWork, clock Clock, ids IDGenerator, ident Identity, cmd OpenWalletCommand) (*OpenWalletResult, error) {
	if err := requireInternal(ident); err != nil {
		return nil, err
	}
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
		var entry wallet.LedgerEntry
		var opening *wager.WagerTransaction
		if withMovement != 0 {
			openingID, err := ids.NewID()
			if err != nil {
				return fmt.Errorf("gerando abertura: %w", err)
			}
			if entry, err = w.ApplyOpening(openingID, cmd.InitialBalance, now); err != nil {
				return err
			}
			if opening, err = wager.NewOpening(wager.OpeningParams{
				ID: openingID, WalletID: walletID, PlayerID: cmd.PlayerID, Amount: cmd.InitialBalance,
			}, now); err != nil {
				return err
			}
		}
		// A carteira entra já com o saldo final: sem UPDATE posterior.
		if err := r.Wallets.Insert(ctx, w); err != nil {
			if conflict, ok := AsType[*ConflictError](err); ok {
				return fmt.Errorf("%w: %s", ErrWalletExists, conflict.Constraint)
			}
			return err
		}
		if withMovement == 0 {
			return nil
		}
		return openWithMovement(ctx, r, ids, w, opening, entry, cmd, now, result)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// openWithMovement grava abertura, lançamento e eventos na transação aberta.
func openWithMovement(ctx context.Context, r Repositories, ids IDGenerator, w *wallet.Wallet, opening *wager.WagerTransaction, entry wallet.LedgerEntry, cmd OpenWalletCommand, now time.Time, result *OpenWalletResult) error {
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
	processed, err := events.NewTransactionProcessed(processedID, opening.ID(), cmd.CorrelationID, "", now, events.ProcessedData{
		TransactionID: opening.ID(), WalletID: w.ID(), PlayerID: cmd.PlayerID,
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
		WalletID: w.ID(), TransactionID: opening.ID(), Direction: events.DirectionCredit,
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
		{processedID, AggregateWager, opening.ID(), events.TypeTransactionProcessed, processedPayload},
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
