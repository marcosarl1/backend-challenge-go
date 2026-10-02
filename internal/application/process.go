package application

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/marcosarl1/backend-challenge-go/internal/domain"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/events"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/idempotency"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wager"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wallet"
)

// ProcessCommand é uma operação externa. BET e LOSS não aceitam referência; REFUND e ROLLBACK exigem; WIN aceita (opcional).
type ProcessCommand struct {
	ProviderID          string
	ExternalID          string
	IdempotencyKey      string
	PlayerID            uuid.UUID
	WalletID            uuid.UUID
	RoundID             string
	GameID              string
	Kind                wager.Kind
	Amount              money.Money
	ReferenceExternalID string
	CorrelationID       string
}

// ProcessResult é o desfecho: concluída traz o saldo observado; rejeitada
// traz o código; repetição traz o gravado original com a marca.
type ProcessResult struct {
	TransactionID    uuid.UUID
	Status           wager.Status
	Balance          money.Money
	WalletVersion    int64
	FailureCode      wager.FailureCode
	IdempotentReplay bool
}

// Execute roda o comando numa transação própria. O provedor do token manda: só processa a própria operação.
func Execute(ctx context.Context, uow UnitOfWork, clock Clock, ids IDGenerator, ident Identity, cmd ProcessCommand) (*ProcessResult, error) {
	if err := requireProvider(ident, cmd.ProviderID); err != nil {
		return nil, err
	}
	var out *ProcessResult
	err := uow.Do(ctx, func(ctx context.Context, r Repositories) error {
		res, err := ExecuteInTx(ctx, r, clock, ids, cmd)
		if err != nil {
			return err
		}
		out = res
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ExecuteInTx roda o comando nas portas já abertas numa transação (o SQS usa
// aqui com a inbox na mesma transação). Repetir por conflito de escrita é
// seguro: a porta de entrada é inserir-ou-detectar pela chave.
func ExecuteInTx(ctx context.Context, r Repositories, clock Clock, ids IDGenerator, cmd ProcessCommand) (*ProcessResult, error) {
	now := clock.Now()
	if err := checkCommand(cmd, now); err != nil {
		return nil, err
	}
	hash, err := idempotency.Hash(idempotency.Operation{
		ProviderID: cmd.ProviderID, ExternalID: cmd.ExternalID,
		PlayerID: cmd.PlayerID.String(), WalletID: cmd.WalletID.String(),
		RoundID: cmd.RoundID, GameID: cmd.GameID, Kind: string(cmd.Kind),
		Amount: cmd.Amount.String(), Currency: string(cmd.Amount.Currency()),
		ReferenceExt: cmd.ReferenceExternalID,
	})
	if err != nil {
		return nil, err
	}
	txID, err := ids.NewID()
	if err != nil {
		return nil, fmt.Errorf("gerando transação: %w", err)
	}
	tx, err := wager.NewExternal(wager.ExternalParams{
		ID: txID, ProviderID: cmd.ProviderID, ExternalID: cmd.ExternalID,
		IdempotencyKey: cmd.IdempotencyKey, PayloadHash: hash[:],
		WalletID: cmd.WalletID, PlayerID: cmd.PlayerID, RoundID: cmd.RoundID, GameID: cmd.GameID,
		Kind: cmd.Kind, Amount: cmd.Amount, ReferenceExtID: cmd.ReferenceExternalID,
	}, now)
	if err != nil {
		return nil, err
	}
	inserted, err := r.Wagers.Insert(ctx, tx, cmd.CorrelationID)
	if err != nil {
		return nil, mapInsertError(err)
	}
	if !inserted {
		return replay(ctx, r, cmd, hash[:])
	}
	return processNew(ctx, r, ids, cmd, tx, now)
}

func checkCommand(cmd ProcessCommand, now time.Time) error {
	if now.IsZero() {
		return fmt.Errorf("%w: relógio vazio", ErrInvalidInput)
	}
	if cmd.ProviderID == "" || cmd.ExternalID == "" || cmd.IdempotencyKey == "" ||
		cmd.RoundID == "" || cmd.GameID == "" || cmd.CorrelationID == "" {
		return fmt.Errorf("%w: campo vazio", ErrInvalidInput)
	}
	if cmd.PlayerID == uuid.Nil || cmd.WalletID == uuid.Nil {
		return fmt.Errorf("%w: identificador vazio", ErrInvalidInput)
	}
	if !cmd.Amount.Valid() {
		return fmt.Errorf("%w: valor inválido: %w", ErrInvalidInput, domain.ErrUninitialized)
	}
	switch cmd.Kind {
	case wager.KindBet, wager.KindWin, wager.KindLoss, wager.KindRefund, wager.KindRollback:
	default:
		return fmt.Errorf("%w: tipo %q", ErrInvalidInput, string(cmd.Kind))
	}
	switch cmd.Kind {
	case wager.KindRefund, wager.KindRollback:
		if cmd.ReferenceExternalID == "" {
			return fmt.Errorf("%w: %s exige referência", ErrInvalidInput, string(cmd.Kind))
		}
	case wager.KindBet, wager.KindLoss:
		if cmd.ReferenceExternalID != "" {
			return fmt.Errorf("%w: %s não aceita referência", ErrInvalidInput, string(cmd.Kind))
		}
	}
	return nil
}

// mapInsertError traduz o que o banco barrou na porta de entrada: id externo
// com outra chave conflita; carteira inexistente não encontra.
func mapInsertError(err error) error {
	conflict, ok := AsType[*ConflictError](err)
	if !ok {
		return err
	}
	switch conflict.Constraint {
	case "wt_provider_external_uk":
		return fmt.Errorf("%w: %s", ErrExternalIDReused, conflict.Constraint)
	case "wager_transactions_wallet_id_fkey":
		return fmt.Errorf("%w: carteira", ErrNotFound)
	default:
		return fmt.Errorf("%w: %s", ErrExternalIDReused, conflict.Constraint)
	}
}

// replay devolve o gravado sem reexecutar: concluída traz o saldo original
// (mesmo que a carteira já tenha andado), rejeitada traz o código, pendente
// traz o estado e o saldo atual.
func replay(ctx context.Context, r Repositories, cmd ProcessCommand, hash []byte) (*ProcessResult, error) {
	existing, err := r.Wagers.FindByProviderKey(ctx, cmd.ProviderID, cmd.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(existing.PayloadHash(), hash) {
		return nil, fmt.Errorf("%w: %s", ErrIdempotencyMismatch, cmd.IdempotencyKey)
	}
	switch existing.Status() {
	case wager.StatusProcessed:
		bal, ver, err := existing.Result()
		if err != nil {
			return nil, err
		}
		return &ProcessResult{TransactionID: existing.ID(), Status: wager.StatusProcessed,
			Balance: bal, WalletVersion: ver, IdempotentReplay: true}, nil
	case wager.StatusRejected, wager.StatusFailed:
		code, _, err := existing.Failure()
		if err != nil {
			return nil, err
		}
		bal, ver, err := currentBalance(ctx, r, cmd.WalletID)
		if err != nil {
			return nil, err
		}
		return &ProcessResult{TransactionID: existing.ID(), Status: existing.Status(),
			Balance: bal, WalletVersion: ver, FailureCode: code, IdempotentReplay: true}, nil
	default:
		bal, ver, err := currentBalance(ctx, r, cmd.WalletID)
		if err != nil {
			return nil, err
		}
		return &ProcessResult{TransactionID: existing.ID(), Status: existing.Status(),
			Balance: bal, WalletVersion: ver, IdempotentReplay: true}, nil
	}
}

func currentBalance(ctx context.Context, r Repositories, walletID uuid.UUID) (money.Money, int64, error) {
	w, err := r.Wallets.Get(ctx, walletID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return money.Money{}, 0, fmt.Errorf("%w: carteira", ErrNotFound)
		}
		return money.Money{}, 0, err
	}
	return w.Balance(), w.Version(), nil
}

// processNew trava a carteira e aplica a operação inédita.
func processNew(ctx context.Context, r Repositories, ids IDGenerator, cmd ProcessCommand, tx *wager.WagerTransaction, now time.Time) (*ProcessResult, error) {
	w, err := r.Wallets.GetForUpdate(ctx, cmd.WalletID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: carteira", ErrNotFound)
		}
		return nil, err
	}
	if w.PlayerID() != cmd.PlayerID {
		return reject(ctx, r, ids, cmd, tx, w, now, wager.CodeWalletMismatch, "jogador diverge da carteira")
	}
	if w.Currency() != cmd.Amount.Currency() {
		return reject(ctx, r, ids, cmd, tx, w, now, wager.CodeCurrencyMismatch, "moeda diverge da carteira")
	}
	prev := w.Version()
	if needsReference(cmd) {
		return resolveReference(ctx, r, ids, cmd, tx, w, prev, now)
	}
	switch cmd.Kind {
	case wager.KindLoss:
		return processLoss(ctx, r, ids, cmd, tx, w, now)
	case wager.KindBet:
		entry, err := w.Debit(tx.ID(), cmd.Amount, now)
		if err != nil {
			if errors.Is(err, domain.ErrInsufficientFunds) {
				return reject(ctx, r, ids, cmd, tx, w, now, wager.CodeInsufficientFunds, "sem saldo")
			}
			return nil, err
		}
		return commitMovement(ctx, r, ids, cmd, tx, w, prev, entry, now)
	default: // wager.KindWin sem referência: crédito direto.
		entry, err := w.Credit(tx.ID(), cmd.Amount, now)
		if err != nil {
			return nil, err
		}
		return commitMovement(ctx, r, ids, cmd, tx, w, prev, entry, now)
	}
}

// needsReference diz se o comando cita outra operação.
func needsReference(cmd ProcessCommand) bool {
	return cmd.Kind == wager.KindRefund || cmd.Kind == wager.KindRollback ||
		(cmd.Kind == wager.KindWin && cmd.ReferenceExternalID != "")
}

// Política de espera pela referência: tenta de novo com espera crescente (base dobrando até o teto) e desiste no limite de tentativas ou no prazo.
const (
	pendingBaseDelay   = time.Second
	pendingMaxDelay    = time.Minute
	pendingTTL         = 10 * time.Minute
	pendingMaxAttempts = 10
)

// backoff devolve quanto esperar antes da próxima tentativa, dado quantas já houve.
func backoff(attempts int) time.Duration {
	d := pendingBaseDelay << attempts
	if d <= 0 || d > pendingMaxDelay {
		return pendingMaxDelay
	}
	return d
}

// resolveReference cumpre o passo da referência: ausente ou pendente espera; terminada sem sucesso rejeita; processada decide aplicar ou rejeitar.
func resolveReference(ctx context.Context, r Repositories, ids IDGenerator, cmd ProcessCommand, tx *wager.WagerTransaction, w *wallet.Wallet, prev int64, now time.Time) (*ProcessResult, error) {
	ref, err := r.Wagers.FindByProviderExternal(ctx, cmd.ProviderID, cmd.ReferenceExternalID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return waitForReference(ctx, r, ids, cmd, tx, w, now)
		}
		return nil, err
	}
	switch ref.Status() {
	case wager.StatusPending, wager.StatusPendingReference:
		return waitForReference(ctx, r, ids, cmd, tx, w, now)
	case wager.StatusRejected, wager.StatusFailed:
		return reject(ctx, r, ids, cmd, tx, w, now, wager.CodeReferenceNotProcessed, "referência sem sucesso")
	default:
		return applyReversal(ctx, r, ids, cmd, tx, w, prev, ref, now)
	}
}

// applyReversal decide e cumpre: credita, debita ou rejeita com o código.
func applyReversal(ctx context.Context, r Repositories, ids IDGenerator, cmd ProcessCommand, tx *wager.WagerTransaction, w *wallet.Wallet, prev int64, ref *wager.WagerTransaction, now time.Time) (*ProcessResult, error) {
	reversed, err := r.Wagers.HasSuccessfulReversal(ctx, ref.ID())
	if err != nil {
		return nil, err
	}
	outcome, err := wager.DecideReversal(tx, ref, reversed, w.Balance())
	if err != nil {
		return nil, err
	}
	switch outcome.Action {
	case wager.ReversalApplyCredit:
		if err := tx.ResolveReference(ref.ID()); err != nil {
			return nil, err
		}
		entry, err := w.Credit(tx.ID(), cmd.Amount, now)
		if err != nil {
			return nil, err
		}
		return commitMovement(ctx, r, ids, cmd, tx, w, prev, entry, now)
	case wager.ReversalApplyDebit:
		if err := tx.ResolveReference(ref.ID()); err != nil {
			return nil, err
		}
		entry, err := w.Debit(tx.ID(), cmd.Amount, now)
		if err != nil {
			return nil, err
		}
		return commitMovement(ctx, r, ids, cmd, tx, w, prev, entry, now)
	default:
		return reject(ctx, r, ids, cmd, tx, w, now, outcome.FailureCode, "reversão recusada")
	}
}

// waitForReference registra a primeira espera, com a próxima tentativa e o prazo final, e avisa com o evento de pendência.
func waitForReference(ctx context.Context, r Repositories, ids IDGenerator, cmd ProcessCommand, tx *wager.WagerTransaction, w *wallet.Wallet, now time.Time) (*ProcessResult, error) {
	if err := tx.WaitForReference(now.Add(backoff(0)), now.Add(pendingTTL), now); err != nil {
		return nil, err
	}
	if err := r.Wagers.Save(ctx, tx); err != nil {
		return nil, err
	}
	eventID, err := ids.NewID()
	if err != nil {
		return nil, fmt.Errorf("gerando evento: %w", err)
	}
	envelope, err := events.NewTransactionPendingReference(eventID, tx.ID(), cmd.CorrelationID, now, events.PendingReferenceData{
		TransactionID: tx.ID(), ProviderID: cmd.ProviderID, ExternalTxID: cmd.ExternalID,
		WalletID: w.ID(), ReferenceExtID: cmd.ReferenceExternalID,
		Attempts: tx.Attempts(), NextAttemptAt: txNext(tx), ExpiresAt: txExpires(tx),
	})
	if err != nil {
		return nil, err
	}
	payload, err := envelope.PayloadJSON()
	if err != nil {
		return nil, fmt.Errorf("serializando espera: %w", err)
	}
	if err := r.Outbox.Insert(ctx, OutboxEvent{
		ID: eventID, AggregateType: AggregateWager, AggregateID: tx.ID(),
		EventType: events.TypeTransactionPendingReference, EventVersion: events.Version,
		OrderingKey: cmd.WalletID.String(), CorrelationID: cmd.CorrelationID,
		Payload: payload, OccurredAt: now,
	}, now); err != nil {
		return nil, err
	}
	return &ProcessResult{TransactionID: tx.ID(), Status: wager.StatusPendingReference,
		Balance: w.Balance(), WalletVersion: w.Version()}, nil
}

func txNext(tx *wager.WagerTransaction) (t time.Time) {
	next, _, _ := tx.NextAttempt()
	return next
}

func txExpires(tx *wager.WagerTransaction) (t time.Time) {
	_, expires, _ := tx.NextAttempt()
	return expires
}
func commitMovement(ctx context.Context, r Repositories, ids IDGenerator, cmd ProcessCommand, tx *wager.WagerTransaction, w *wallet.Wallet, prev int64, entry wallet.LedgerEntry, now time.Time) (*ProcessResult, error) {
	if err := r.Wallets.UpdateBalance(ctx, w, prev, now); err != nil {
		return nil, err
	}
	if err := r.Ledger.Insert(ctx, entry); err != nil {
		return nil, err
	}
	if err := tx.MarkProcessed(w.Balance(), w.Version(), now); err != nil {
		return nil, err
	}
	if err := r.Wagers.Save(ctx, tx); err != nil {
		return nil, err
	}
	if err := emitProcessed(ctx, r, ids, cmd, tx, w, entry, now, true); err != nil {
		return nil, err
	}
	return &ProcessResult{TransactionID: tx.ID(), Status: wager.StatusProcessed,
		Balance: w.Balance(), WalletVersion: w.Version()}, nil
}

// processLoss conclui a derrota informativa sem tocar no saldo, na versão ou
// no ledger — mas com evento de operação processada.
func processLoss(ctx context.Context, r Repositories, ids IDGenerator, cmd ProcessCommand, tx *wager.WagerTransaction, w *wallet.Wallet, now time.Time) (*ProcessResult, error) {
	if err := tx.MarkProcessed(w.Balance(), w.Version(), now); err != nil {
		return nil, err
	}
	if err := r.Wagers.Save(ctx, tx); err != nil {
		return nil, err
	}
	if err := emitProcessed(ctx, r, ids, cmd, tx, w, wallet.LedgerEntry{}, now, false); err != nil {
		return nil, err
	}
	return &ProcessResult{TransactionID: tx.ID(), Status: wager.StatusProcessed,
		Balance: w.Balance(), WalletVersion: w.Version()}, nil
}

// reject registra a recusa com código estável e emite o evento de rejeição.
// É um desfecho definitivo e auditável, não um erro.
func reject(ctx context.Context, r Repositories, ids IDGenerator, cmd ProcessCommand, tx *wager.WagerTransaction, w *wallet.Wallet, now time.Time, code wager.FailureCode, detail string) (*ProcessResult, error) {
	if err := tx.Reject(code, detail, now); err != nil {
		return nil, err
	}
	if err := r.Wagers.Save(ctx, tx); err != nil {
		return nil, err
	}
	eventID, err := ids.NewID()
	if err != nil {
		return nil, fmt.Errorf("gerando evento: %w", err)
	}
	envelope, err := events.NewTransactionRejected(eventID, tx.ID(), cmd.CorrelationID, "", now, events.RejectedData{
		ProcessedData: processedData(cmd, tx, w),
		FailureCode:   string(code),
		FailureDetail: detail,
	})
	if err != nil {
		return nil, err
	}
	if err := insertEvent(ctx, r, cmd, eventID, AggregateWager, tx.ID(), events.TypeTransactionRejected, envelope, now); err != nil {
		return nil, err
	}
	return &ProcessResult{TransactionID: tx.ID(), Status: wager.StatusRejected,
		Balance: w.Balance(), WalletVersion: w.Version(), FailureCode: code}, nil
}

// emitProcessed emite operação processada e, com movimento, saldo alterado.
func emitProcessed(ctx context.Context, r Repositories, ids IDGenerator, cmd ProcessCommand, tx *wager.WagerTransaction, w *wallet.Wallet, entry wallet.LedgerEntry, now time.Time, moved bool) error {
	processedID, err := ids.NewID()
	if err != nil {
		return fmt.Errorf("gerando evento: %w", err)
	}
	processed, err := events.NewTransactionProcessed(processedID, tx.ID(), cmd.CorrelationID, "", now, processedData(cmd, tx, w))
	if err != nil {
		return err
	}
	if err := insertEvent(ctx, r, cmd, processedID, AggregateWager, tx.ID(), events.TypeTransactionProcessed, processed, now); err != nil {
		return err
	}
	if !moved {
		return nil
	}
	changedID, err := ids.NewID()
	if err != nil {
		return fmt.Errorf("gerando evento: %w", err)
	}
	var direction events.Direction
	if entry.Direction() == wallet.DirectionDebit {
		direction = events.DirectionDebit
	} else {
		direction = events.DirectionCredit
	}
	changed, err := events.NewBalanceChanged(changedID, w.ID(), cmd.CorrelationID, "", now, events.BalanceChangedData{
		WalletID: w.ID(), TransactionID: tx.ID(), Direction: direction,
		Amount: cmd.Amount, BalanceBefore: entry.BalanceBefore(),
		BalanceAfter: entry.BalanceAfter(), WalletVersion: w.Version(),
	})
	if err != nil {
		return err
	}
	return insertEvent(ctx, r, cmd, changedID, AggregateWallet, w.ID(), events.TypeBalanceChanged, changed, now)
}

func processedData(cmd ProcessCommand, tx *wager.WagerTransaction, w *wallet.Wallet) events.ProcessedData {
	return events.ProcessedData{
		TransactionID: tx.ID(), ProviderID: cmd.ProviderID, ExternalTxID: cmd.ExternalID,
		WalletID: w.ID(), PlayerID: cmd.PlayerID, RoundID: cmd.RoundID, GameID: cmd.GameID,
		Kind: string(cmd.Kind), Amount: cmd.Amount,
		ResultBalance: w.Balance(), ResultWalletVersion: w.Version(),
	}
}

func insertEvent[T any](ctx context.Context, r Repositories, cmd ProcessCommand, eventID uuid.UUID, aggType string, aggregateID uuid.UUID, typ string, envelope events.Envelope[T], now time.Time) error {
	payload, err := envelope.PayloadJSON()
	if err != nil {
		return fmt.Errorf("serializando %s: %w", typ, err)
	}
	return r.Outbox.Insert(ctx, OutboxEvent{
		ID: eventID, AggregateType: aggType, AggregateID: aggregateID,
		EventType: typ, EventVersion: events.Version, OrderingKey: cmd.WalletID.String(),
		CorrelationID: cmd.CorrelationID, Payload: payload, OccurredAt: now,
	}, now)
}
