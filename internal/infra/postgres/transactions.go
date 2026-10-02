package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/marcosarl1/backend-challenge-go/internal/domain"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wager"
)

// WagerStore guarda e carrega transações, incluindo a detecção de
// idempotência e a varredura de pendências do worker.
type WagerStore struct{}

// Insert tenta gravar a transação pendente. A correlação vem de fora (é de transporte, não do domínio). Se a chave de idempotência já existe, não grava nada e devolve (false, nil): quem chama resolve o conflito ou o replay lendo a linha existente.
func (WagerStore) Insert(ctx context.Context, db DBTX, tx *wager.WagerTransaction, correlationID string) (bool, error) {
	s := tx.Snapshot()
	var origin, nullProvider, nullExternal, nullKey, nullHash, nullRound, nullGame any
	origin = "EXTERNAL"
	if s.Kind == wager.KindOpening {
		origin = "INTERNAL"
	} else {
		nullProvider, nullExternal, nullKey, nullHash, nullRound, nullGame =
			s.ProviderID, s.ExternalID, s.IdempotencyKey, s.PayloadHash, s.RoundID, s.GameID
	}
	var refExt, resolvedRef, failureCode, failureDetail, resultBalance, resultVersion any
	if s.ReferenceExtID != "" {
		refExt = s.ReferenceExtID
	}
	if s.Status == wager.StatusProcessed {
		resultBalance = s.ResultBalance.Minor()
		resultVersion = s.ResultVersion
	}
	if s.ResolvedRefID != uuid.Nil {
		resolvedRef = toPGUUID(s.ResolvedRefID)
	}
	tag, err := db.Exec(ctx, `INSERT INTO wager_transactions
		(id, origin, kind, status, wallet_id, player_id, currency, amount_minor,
		 provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
		 reference_external_transaction_id, resolved_reference_id,
		 failure_code, failure_detail, result_balance_minor, result_wallet_version,
		 attempts, next_attempt_at, expires_at, correlation_id, created_at, updated_at, resolved_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
		 $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27)
		ON CONFLICT (provider_id, idempotency_key) DO NOTHING`,
		toPGUUID(s.ID), origin, string(s.Kind), string(s.Status),
		toPGUUID(s.WalletID), toPGUUID(s.PlayerID), string(s.Amount.Currency()), s.Amount.Minor(),
		nullProvider, nullExternal, nullKey, nullHash, nullRound, nullGame,
		refExt, resolvedRef, failureCode, failureDetail, resultBalance, resultVersion,
		s.Attempts, nilTime(s.NextAttemptAt), nilTime(s.ExpiresAt), correlationID,
		s.CreatedAt, s.UpdatedAt, nilTime(s.ResolvedAt))
	if err != nil {
		return false, fmt.Errorf("inserindo transação: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func nilTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

const wagerColumns = `id, provider_id, external_transaction_id, idempotency_key, payload_hash,
	wallet_id, player_id, round_id, game_id, kind, currency, amount_minor,
	reference_external_transaction_id, resolved_reference_id, status,
	failure_code, failure_detail, result_balance_minor, result_wallet_version,
	attempts, next_attempt_at, expires_at, created_at, updated_at, resolved_at`

type wagerRow struct {
	ID             pgtype.UUID
	ProviderID     *string
	ExternalID     *string
	IdempotencyKey *string
	PayloadHash    []byte
	WalletID       pgtype.UUID
	PlayerID       pgtype.UUID
	RoundID        *string
	GameID         *string
	Kind           string
	Currency       string
	AmountMinor    int64
	ReferenceExtID *string
	ResolvedRefID  pgtype.UUID
	Status         string
	FailureCode    *string
	FailureDetail  *string
	ResultBalance  *int64
	ResultVersion  *int64
	Attempts       int
	NextAttemptAt  *time.Time
	ExpiresAt      *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ResolvedAt     *time.Time
}

func strVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func scanWager(row interface{ Scan(...any) error }) (*wager.WagerTransaction, error) {
	var r wagerRow
	if err := row.Scan(&r.ID, &r.ProviderID, &r.ExternalID, &r.IdempotencyKey, &r.PayloadHash,
		&r.WalletID, &r.PlayerID, &r.RoundID, &r.GameID, &r.Kind, &r.Currency, &r.AmountMinor,
		&r.ReferenceExtID, &r.ResolvedRefID, &r.Status, &r.FailureCode, &r.FailureDetail,
		&r.ResultBalance, &r.ResultVersion, &r.Attempts, &r.NextAttemptAt, &r.ExpiresAt,
		&r.CreatedAt, &r.UpdatedAt, &r.ResolvedAt); err != nil {
		return nil, fmt.Errorf("lendo transação: %w", err)
	}
	id, err := fromPGUUID("id", r.ID)
	if err != nil {
		return nil, err
	}
	walletID, err := fromPGUUID("wallet_id", r.WalletID)
	if err != nil {
		return nil, err
	}
	playerID, err := fromPGUUID("player_id", r.PlayerID)
	if err != nil {
		return nil, err
	}
	amount, err := moneyFromRow("amount_minor", r.AmountMinor, r.Currency)
	if err != nil {
		return nil, err
	}
	snap := wager.Snapshot{
		ID: id, ProviderID: strVal(r.ProviderID), ExternalID: strVal(r.ExternalID),
		IdempotencyKey: strVal(r.IdempotencyKey), PayloadHash: r.PayloadHash,
		WalletID: walletID, PlayerID: playerID, RoundID: strVal(r.RoundID), GameID: strVal(r.GameID),
		Kind: wager.Kind(r.Kind), Amount: amount, ReferenceExtID: strVal(r.ReferenceExtID),
		Status: wager.Status(r.Status), FailureCode: wager.FailureCode(strVal(r.FailureCode)),
		Attempts: r.Attempts, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if r.ResolvedRefID.Valid {
		snap.ResolvedRefID = r.ResolvedRefID.Bytes
	}
	if r.FailureDetail != nil {
		snap.FailureDetail = *r.FailureDetail
	}
	if r.ResultBalance != nil && r.ResultVersion != nil {
		bal, err := moneyFromRow("result_balance_minor", *r.ResultBalance, r.Currency)
		if err != nil {
			return nil, err
		}
		snap.ResultBalance = bal
		snap.ResultVersion = *r.ResultVersion
	}
	if r.NextAttemptAt != nil {
		snap.NextAttemptAt = *r.NextAttemptAt
	}
	if r.ExpiresAt != nil {
		snap.ExpiresAt = *r.ExpiresAt
	}
	if r.ResolvedAt != nil {
		snap.ResolvedAt = *r.ResolvedAt
	}
	return wager.Rehydrate(snap)
}

// FindByProviderKey acha pela chave de idempotência (replay e conflito).
func (WagerStore) FindByProviderKey(ctx context.Context, db DBTX, providerID, key string) (*wager.WagerTransaction, error) {
	return scanWager(db.QueryRow(ctx, `SELECT `+wagerColumns+` FROM wager_transactions
		WHERE provider_id = $1 AND idempotency_key = $2`, providerID, key))
}

// FindByProviderExternal acha pelo id externo (unicidade por provedor).
func (WagerStore) FindByProviderExternal(ctx context.Context, db DBTX, providerID, externalID string) (*wager.WagerTransaction, error) {
	return scanWager(db.QueryRow(ctx, `SELECT `+wagerColumns+` FROM wager_transactions
		WHERE provider_id = $1 AND external_transaction_id = $2`, providerID, externalID))
}

// FindByID acha pelo id interno.
func (WagerStore) FindByID(ctx context.Context, db DBTX, id uuid.UUID) (*wager.WagerTransaction, error) {
	return scanWager(db.QueryRow(ctx, `SELECT `+wagerColumns+` FROM wager_transactions WHERE id = $1`, toPGUUID(id)))
}

// Save grava o estado atual (transições, resultado, tentativas, referência resolvida). A trigger do banco recusa mexer em estado final.
func (WagerStore) Save(ctx context.Context, db DBTX, tx *wager.WagerTransaction) error {
	s := tx.Snapshot()
	var refExt, resolvedRef, failureCode, failureDetail, resultBalance, resultVersion any
	if s.ReferenceExtID != "" {
		refExt = s.ReferenceExtID
	}
	if s.Status == wager.StatusProcessed {
		resultBalance = s.ResultBalance.Minor()
		resultVersion = s.ResultVersion
	}
	if s.ResolvedRefID != uuid.Nil {
		resolvedRef = toPGUUID(s.ResolvedRefID)
	}
	if s.FailureCode != "" {
		failureCode = string(s.FailureCode)
	}
	if s.FailureDetail != "" {
		failureDetail = s.FailureDetail
	}
	if s.Status == wager.StatusProcessed {
		resultBalance = s.ResultBalance.Minor()
		resultVersion = s.ResultVersion
	}
	tag, err := db.Exec(ctx, `UPDATE wager_transactions SET status = $2,
		reference_external_transaction_id = $3, resolved_reference_id = $4,
		failure_code = $5, failure_detail = $6,
		result_balance_minor = $7, result_wallet_version = $8,
		attempts = $9, next_attempt_at = $10, expires_at = $11,
		updated_at = $12, resolved_at = $13
		WHERE id = $1`,
		toPGUUID(s.ID), string(s.Status), refExt, resolvedRef,
		failureCode, failureDetail, resultBalance, resultVersion,
		s.Attempts, nilTime(s.NextAttemptAt), nilTime(s.ExpiresAt),
		s.UpdatedAt, nilTime(s.ResolvedAt))
	if err != nil {
		return fmt.Errorf("gravando transação: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: transação %s sumiu", domain.ErrInvalidTransition, s.ID)
	}
	return nil
}

// ClaimDue reserva um lote de pendências vencidas para o worker, pulando as que outro worker já pegou. Sem lease separado: a transação que processa é a mesma que reserva (se ela morrer, a trava solta e outra assume).
func (WagerStore) ClaimDue(ctx context.Context, db DBTX, now time.Time, limit int) ([]*wager.WagerTransaction, error) {
	rows, err := db.Query(ctx, `SELECT `+wagerColumns+` FROM wager_transactions
		WHERE status IN ('PENDING', 'PENDING_REFERENCE') AND next_attempt_at <= $1
		ORDER BY next_attempt_at LIMIT $2 FOR UPDATE SKIP LOCKED`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("reservando pendências: %w", err)
	}
	defer rows.Close()
	var out []*wager.WagerTransaction
	for rows.Next() {
		tx, err := scanWager(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tx)
	}
	return out, rows.Err()
}
