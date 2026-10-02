package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marcosarl1/backend-challenge-go/internal/domain"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wager"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wallet"
)

// RetryPending retoma um lote de pendências vencidas (espera por referência ou pendente órfã) na mesma transação que as reserva. É o motor que o worker de referências vai agendar; aqui, também serve para provar a resolução posterior nos testes. Devolve quantas saíram da espera.
func RetryPending(ctx context.Context, uow UnitOfWork, clock Clock, ids IDGenerator, limit int) (int, error) {
	now := clock.Now()
	retomed := 0
	err := uow.Do(ctx, func(ctx context.Context, r Repositories) error {
		dues, err := r.Wagers.ClaimDue(ctx, now, limit)
		if err != nil {
			return err
		}
		for _, due := range dues {
			done, err := continueDue(ctx, r, ids, due, now)
			if err != nil {
				return err
			}
			if done {
				retomed++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return retomed, nil
}

// continueDue retoma uma pendência: reconstrói o comando do gravado e segue o mesmo caminho da entrada nova. Reagendamento não reemite evento (o aviso de espera saiu uma vez); esgotada a espera, rejeita com referência não encontrada.
func continueDue(ctx context.Context, r Repositories, ids IDGenerator, due DueTransaction, now time.Time) (bool, error) {
	tx := due.Tx
	if tx.Status() != wager.StatusPending && tx.Status() != wager.StatusPendingReference {
		return false, nil
	}
	snap := tx.Snapshot()
	cmd := ProcessCommand{
		ProviderID: snap.ProviderID, ExternalID: snap.ExternalID,
		IdempotencyKey: snap.IdempotencyKey, PlayerID: snap.PlayerID, WalletID: snap.WalletID,
		RoundID: snap.RoundID, GameID: snap.GameID, Kind: snap.Kind, Amount: snap.Amount,
		ReferenceExternalID: snap.ReferenceExtID, CorrelationID: due.CorrelationID,
	}
	w, err := r.Wallets.GetForUpdate(ctx, cmd.WalletID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, fmt.Errorf("%w: carteira", ErrNotFound)
		}
		return false, err
	}
	if w.PlayerID() != cmd.PlayerID {
		_, err := reject(ctx, r, ids, cmd, tx, w, now, wager.CodeWalletMismatch, "jogador diverge da carteira")
		return err == nil, err
	}
	if w.Currency() != cmd.Amount.Currency() {
		_, err := reject(ctx, r, ids, cmd, tx, w, now, wager.CodeCurrencyMismatch, "moeda diverge da carteira")
		return err == nil, err
	}
	prev := w.Version()
	if tx.Status() == wager.StatusPendingReference || needsReference(cmd) {
		return continueReference(ctx, r, ids, cmd, tx, w, prev, now)
	}
	return continueSimple(ctx, r, ids, cmd, tx, w, prev, now)
}

// continueSimple conclui pendente sem referência (retomada de interrupção).
func continueSimple(ctx context.Context, r Repositories, ids IDGenerator, cmd ProcessCommand, tx *wager.WagerTransaction, w *wallet.Wallet, prev int64, now time.Time) (bool, error) {
	switch cmd.Kind {
	case wager.KindLoss:
		_, err := processLoss(ctx, r, ids, cmd, tx, w, now)
		return err == nil, err
	case wager.KindBet:
		entry, err := w.Debit(tx.ID(), cmd.Amount, now)
		if err != nil {
			if errors.Is(err, domain.ErrInsufficientFunds) {
				_, rerr := reject(ctx, r, ids, cmd, tx, w, now, wager.CodeInsufficientFunds, "sem saldo")
				return rerr == nil, rerr
			}
			return false, err
		}
		_, err = commitMovement(ctx, r, ids, cmd, tx, w, prev, entry, now)
		return err == nil, err
	default:
		entry, err := w.Credit(tx.ID(), cmd.Amount, now)
		if err != nil {
			return false, err
		}
		_, err = commitMovement(ctx, r, ids, cmd, tx, w, prev, entry, now)
		return err == nil, err
	}
}

// continueReference tenta de novo a referência: resolveu, aplica; segue ausente, remarca ou encerra por expiração.
func continueReference(ctx context.Context, r Repositories, ids IDGenerator, cmd ProcessCommand, tx *wager.WagerTransaction, w *wallet.Wallet, prev int64, now time.Time) (bool, error) {
	ref, err := r.Wagers.FindByProviderExternal(ctx, cmd.ProviderID, cmd.ReferenceExternalID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return rescheduleOrExpire(ctx, r, ids, cmd, tx, w, now)
		}
		return false, err
	}
	switch ref.Status() {
	case wager.StatusPending, wager.StatusPendingReference:
		return rescheduleOrExpire(ctx, r, ids, cmd, tx, w, now)
	case wager.StatusRejected, wager.StatusFailed:
		_, err := reject(ctx, r, ids, cmd, tx, w, now, wager.CodeReferenceNotProcessed, "referência sem sucesso")
		return err == nil, err
	default:
		_, err := applyReversal(ctx, r, ids, cmd, tx, w, prev, ref, now)
		return err == nil, err
	}
}

// rescheduleOrExpire remarca a espera com espera crescente, ou rejeita quando
// a espera esgotou (tentativas ou prazo).
func rescheduleOrExpire(ctx context.Context, r Repositories, ids IDGenerator, cmd ProcessCommand, tx *wager.WagerTransaction, w *wallet.Wallet, now time.Time) (bool, error) {
	_, expires, _ := tx.NextAttempt()
	if tx.Attempts() >= pendingMaxAttempts || (!expires.IsZero() && !now.Before(expires)) {
		_, err := reject(ctx, r, ids, cmd, tx, w, now, wager.CodeReferenceNotFound, "referência não chegou")
		return err == nil, err
	}
	if err := tx.Reschedule(now.Add(backoff(tx.Attempts())), now); err != nil {
		// Pendente simples (nunca esperou) entra aqui na primeira vez.
		if err := tx.WaitForReference(now.Add(backoff(tx.Attempts())), now.Add(pendingTTL), now); err != nil {
			return false, err
		}
	}
	if err := r.Wagers.Save(ctx, tx); err != nil {
		return false, err
	}
	return true, nil
}
