package wager

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/domain"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
)

// Snapshot é a foto da transação como guardada no banco, para reidratar depois. Campos de resultado só valem em PROCESSED; código de falha só em REJECTED ou FAILED; próxima tentativa e prazo só em PENDING_REFERENCE.
type Snapshot struct {
	ID             uuid.UUID
	ProviderID     string
	ExternalID     string
	IdempotencyKey string
	PayloadHash    []byte
	WalletID       uuid.UUID
	PlayerID       uuid.UUID
	RoundID        string
	GameID         string
	Kind           Kind
	Amount         money.Money
	ReferenceExtID string
	ResolvedRefID  uuid.UUID
	Status         Status
	FailureCode    FailureCode
	FailureDetail  string
	ResultBalance  money.Money
	ResultVersion  int64
	Attempts       int
	NextAttemptAt  time.Time
	ExpiresAt      time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ResolvedAt     time.Time
}

// Snapshot devolve a objeto atual para persistir.
func (t *WagerTransaction) Snapshot() Snapshot {
	return Snapshot{
		ID: t.id, ProviderID: t.providerID, ExternalID: t.externalID,
		IdempotencyKey: t.idempotencyKey, PayloadHash: append([]byte(nil), t.payloadHash...),
		WalletID: t.walletID, PlayerID: t.playerID, RoundID: t.roundID, GameID: t.gameID,
		Kind: t.kind, Amount: t.amount, ReferenceExtID: t.referenceExtID,
		ResolvedRefID: t.resolvedRefID, Status: t.status,
		FailureCode: t.failureCode, FailureDetail: t.failureDetail,
		ResultBalance: t.resultBalance, ResultVersion: t.resultVersion,
		Attempts: t.attempts, NextAttemptAt: t.nextAttemptAt, ExpiresAt: t.expiresAt,
		CreatedAt: t.createdAt, UpdatedAt: t.updatedAt, ResolvedAt: t.resolvedAt,
	}
}

// Rehydrate reconstrói a transação a partir do guardado, conferindo as invariantes. Não aplica nada, não emite nada: é só leitura validada.
func Rehydrate(s Snapshot) (*WagerTransaction, error) {
	if s.ID == uuid.Nil || s.WalletID == uuid.Nil || s.PlayerID == uuid.Nil {
		return nil, fmt.Errorf("%w: identificador vazio na transação", domain.ErrUninitialized)
	}
	if !s.Amount.Valid() {
		return nil, domain.ErrUninitialized
	}
	switch s.Kind {
	case KindOpening, KindBet, KindWin, KindLoss, KindRefund, KindRollback:
	default:
		return nil, fmt.Errorf("%w: %q", domain.ErrInvalidKind, string(s.Kind))
	}
	switch s.Status {
	case StatusPending, StatusPendingReference, StatusProcessed, StatusRejected, StatusFailed:
	default:
		return nil, fmt.Errorf("%w: estado %q", domain.ErrInvalidMoney, string(s.Status))
	}
	if s.Kind == KindOpening {
		if s.ProviderID != "" || s.ExternalID != "" || s.IdempotencyKey != "" ||
			len(s.PayloadHash) != 0 || s.RoundID != "" || s.GameID != "" || s.ReferenceExtID != "" {
			return nil, fmt.Errorf("%w: abertura com metadado externo", domain.ErrInvalidMoney)
		}
	} else {
		if s.ProviderID == "" || s.ExternalID == "" || s.IdempotencyKey == "" ||
			len(s.PayloadHash) == 0 || s.RoundID == "" || s.GameID == "" {
			return nil, fmt.Errorf("%w: externa incompleta", domain.ErrInvalidMoney)
		}
	}
	if s.Status == StatusProcessed {
		if !s.ResultBalance.Valid() || s.ResultVersion < 1 {
			return nil, fmt.Errorf("%w: processada sem resultado", domain.ErrInvalidMoney)
		}
	} else if s.ResultBalance.Valid() || s.ResultVersion != 0 {
		return nil, fmt.Errorf("%w: resultado fora de processada", domain.ErrInvalidMoney)
	}
	if s.Status == StatusRejected || s.Status == StatusFailed {
		if !s.FailureCode.Valid() {
			return nil, fmt.Errorf("%w: falha sem código", domain.ErrInvalidMoney)
		}
	} else if s.FailureCode != "" {
		return nil, fmt.Errorf("%w: código fora de falha", domain.ErrInvalidMoney)
	}
	if s.Status == StatusPendingReference {
		if s.NextAttemptAt.IsZero() || s.ExpiresAt.IsZero() {
			return nil, fmt.Errorf("%w: espera sem prazo", domain.ErrInvalidMoney)
		}
	}
	if s.Attempts < 0 {
		return nil, fmt.Errorf("%w: tentativas %d", domain.ErrInvalidMoney, s.Attempts)
	}
	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		return nil, fmt.Errorf("%w: instante vazio na transação", domain.ErrUninitialized)
	}
	tx := &WagerTransaction{
		id: s.ID, providerID: s.ProviderID, externalID: s.ExternalID,
		idempotencyKey: s.IdempotencyKey, payloadHash: append([]byte(nil), s.PayloadHash...),
		walletID: s.WalletID, playerID: s.PlayerID, roundID: s.RoundID, gameID: s.GameID,
		kind: s.Kind, amount: s.Amount, referenceExtID: s.ReferenceExtID,
		resolvedRefID: s.ResolvedRefID, status: s.Status,
		failureCode: s.FailureCode, failureDetail: s.FailureDetail,
		resultBalance: s.ResultBalance, resultVersion: s.ResultVersion,
		attempts: s.Attempts, nextAttemptAt: s.NextAttemptAt, expiresAt: s.ExpiresAt,
		createdAt: s.CreatedAt, updatedAt: s.UpdatedAt, resolvedAt: s.ResolvedAt,
	}
	return tx, nil
}
