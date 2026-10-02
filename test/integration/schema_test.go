//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// expectSchemaConstraint confere a restrição exata e deixa a transação utilizável para o próximo caso.
func expectSchemaConstraint(t *testing.T, ctx context.Context, tx pgx.Tx, name string, query string, args ...any) {
	t.Helper()
	if _, err := tx.Exec(ctx, "SAVEPOINT schema_case"); err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	_, err := tx.Exec(ctx, query, args...)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != name {
		t.Fatalf("constraint = %v, esperado %s", err, name)
	}
	if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT schema_case"); err != nil {
		t.Fatalf("rollback do caso: %v", err)
	}
	if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT schema_case"); err != nil {
		t.Fatalf("release do caso: %v", err)
	}
}

func TestSchemaConstraints(t *testing.T) {
	conn := connect(t, ownerURL(t))
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)

	walletID, playerID := uuid.New(), uuid.New()
	openingID, betID, secondBetID := uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
		VALUES ($1, $2, 'BRL', 10000, 1, $3, $3)`, walletID, playerID, now); err != nil {
		t.Fatalf("carteira inicial: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO wager_transactions
		(id, origin, kind, status, wallet_id, player_id, currency, amount_minor,
		 result_balance_minor, result_wallet_version, correlation_id, created_at, updated_at)
		VALUES ($1, 'INTERNAL', 'OPENING', 'PROCESSED', $2, $3, 'BRL', 10000,
		 10000, 1, 'schema', $4, $4)`, openingID, walletID, playerID, now); err != nil {
		t.Fatalf("abertura inicial: %v", err)
	}
	for i, id := range []uuid.UUID{betID, secondBetID} {
		if _, err := tx.Exec(ctx, `INSERT INTO wager_transactions
			(id, origin, kind, status, wallet_id, player_id, currency, amount_minor,
			 provider_id, external_transaction_id, idempotency_key, payload_hash,
			 round_id, game_id, correlation_id, created_at, updated_at)
			VALUES ($1, 'EXTERNAL', 'BET', 'PENDING', $2, $3, 'BRL', 100,
			 'provider-a', $4, $5, '\xaa', 'round', 'game', 'schema', $6, $6)`,
			id, walletID, playerID, fmt.Sprintf("bet-%d", i), fmt.Sprintf("key-%d", i), now); err != nil {
			t.Fatalf("aposta inicial %d: %v", i, err)
		}
	}
	entryID := uuid.New()
	if _, err := tx.Exec(ctx, `INSERT INTO wallet_ledger_entries
		(id, wallet_id, transaction_id, direction, amount_minor, currency, balance_before_minor, balance_after_minor, created_at)
		VALUES ($1, $2, $3, 'CREDIT', 10000, 'BRL', 0, 10000, $4)`, entryID, walletID, openingID, now); err != nil {
		t.Fatalf("lançamento inicial: %v", err)
	}

	// Cada violação ocorre com os dados-base ainda válidos; a falha não contamina os casos seguintes.
	for _, tt := range []struct {
		name       string
		constraint string
		query      string
		args       []any
	}{
		{"saldo negativo", "wallets_balance_minor_check", `UPDATE wallets SET balance_minor = -1 WHERE id = $1`, []any{walletID}},
		{"versão zero", "wallets_version_check", `UPDATE wallets SET version = 0 WHERE id = $1`, []any{walletID}},
		{"carteira duplicada", "wallets_player_currency_uk", `INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at) VALUES ($1, $2, 'BRL', 0, 1, $3, $3)`, []any{uuid.New(), playerID, now}},
		{"origem incompleta", "origin_shape", `UPDATE wager_transactions SET provider_id = NULL WHERE id = $1`, []any{betID}},
		{"aposta zerada", "amount_by_kind", `UPDATE wager_transactions SET amount_minor = 0 WHERE id = $1`, []any{betID}},
		{"reversão sem alvo", "reversal_has_reference", `UPDATE wager_transactions SET kind = 'REFUND' WHERE id = $1`, []any{betID}},
		{"processada sem resultado", "processed_has_result", `UPDATE wager_transactions SET status = 'PROCESSED' WHERE id = $1`, []any{betID}},
		{"rejeitada sem código", "failure_has_code", `UPDATE wager_transactions SET status = 'REJECTED' WHERE id = $1`, []any{betID}},
		{"id externo repetido", "wt_provider_external_uk", `UPDATE wager_transactions SET external_transaction_id = 'bet-0' WHERE id = $1`, []any{secondBetID}},
		{"chave repetida", "wt_provider_idemkey_uk", `UPDATE wager_transactions SET idempotency_key = 'key-0' WHERE id = $1`, []any{secondBetID}},
		{"conta do ledger errada", "ledger_arith", `INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount_minor, currency, balance_before_minor, balance_after_minor, created_at) VALUES ($1, $2, $3, 'DEBIT', 100, 'BRL', 10000, 10000, $4)`, []any{uuid.New(), walletID, betID, now}},
		{"lançamento duplicado", "ledger_wallet_tx_uk", `INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount_minor, currency, balance_before_minor, balance_after_minor, created_at) VALUES ($1, $2, $3, 'CREDIT', 10000, 'BRL', 0, 10000, $4)`, []any{uuid.New(), walletID, openingID, now}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			expectSchemaConstraint(t, ctx, tx, tt.constraint, tt.query, tt.args...)
		})
	}
}

func TestSchemaAtomicityOnLedgerFailure(t *testing.T) {
	conn := connect(t, ownerURL(t))
	ctx := context.Background()
	walletID, playerID, txID, eventID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at) VALUES ($1, $2, 'BRL', 10000, 1, $3, $3)`, walletID, playerID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO wager_transactions
		(id, origin, kind, status, wallet_id, player_id, currency, amount_minor,
		 result_balance_minor, result_wallet_version, correlation_id, created_at, updated_at)
		VALUES ($1, 'INTERNAL', 'OPENING', 'PROCESSED', $2, $3, 'BRL', 10000,
		 10000, 1, 'schema-atomic', $4, $4)`, txID, walletID, playerID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO outbox_events
		(id, aggregate_type, aggregate_id, event_type, event_version, ordering_key,
		 correlation_id, payload, occurred_at, next_attempt_at)
		VALUES ($1, 'transaction', $2, 'WagerTransactionProcessed', 1, $3,
		 'schema-atomic', '{}', $4, $4)`, eventID, txID, walletID.String(), now); err != nil {
		t.Fatal(err)
	}
	expectSchemaConstraint(t, ctx, tx, "ledger_arith", `INSERT INTO wallet_ledger_entries
		(id, wallet_id, transaction_id, direction, amount_minor, currency, balance_before_minor, balance_after_minor, created_at)
		VALUES ($1, $2, $3, 'CREDIT', 10000, 'BRL', 0, 9999, $4)`, uuid.New(), walletID, txID, now)
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, query string
		id          uuid.UUID
	}{
		{"carteira", `SELECT COUNT(*) FROM wallets WHERE id = $1`, walletID},
		{"operação", `SELECT COUNT(*) FROM wager_transactions WHERE id = $1`, txID},
		{"outbox", `SELECT COUNT(*) FROM outbox_events WHERE id = $1`, eventID},
		{"ledger", `SELECT COUNT(*) FROM wallet_ledger_entries WHERE transaction_id = $1`, txID},
	} {
		var count int
		if err := conn.QueryRow(ctx, tt.query, tt.id).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s persistiu: quantidade %d, erro %v", tt.name, count, err)
		}
	}
}
