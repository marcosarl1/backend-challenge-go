package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wallet"
)

// LedgerStore grava lançamentos e lê o ledger (reconciliação e paginação).
type LedgerStore struct{}

// Insert grava o lançamento do movimento. A unicidade (carteira, transação) e a aritmética são cobradas pelo banco de novo, além do domínio.
func (LedgerStore) Insert(ctx context.Context, db DBTX, e wallet.LedgerEntry) error {
	_, err := db.Exec(ctx, `INSERT INTO wallet_ledger_entries
		(id, wallet_id, transaction_id, direction, amount_minor, currency, balance_before_minor, balance_after_minor, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		toPGUUID(e.ID()), toPGUUID(e.WalletID()), toPGUUID(e.TransactionID()),
		string(e.Direction()), e.Amount().Minor(), string(e.Amount().Currency()),
		e.BalanceBefore().Minor(), e.BalanceAfter().Minor(), e.CreatedAt())
	if err != nil {
		return fmt.Errorf("inserindo lançamento: %w", err)
	}
	return nil
}

// SumByWallet soma créditos menos débitos a partir do ledger, para conferir contra o saldo guardado.
func (LedgerStore) SumByWallet(ctx context.Context, db DBTX, walletID uuid.UUID, cur money.Currency) (money.Money, int64, error) {
	var credits, debits *int64
	var count int64
	err := db.QueryRow(ctx, `SELECT
		COALESCE(SUM(amount_minor) FILTER (WHERE direction = 'CREDIT'), 0),
		COALESCE(SUM(amount_minor) FILTER (WHERE direction = 'DEBIT'), 0),
		COUNT(*)
		FROM wallet_ledger_entries WHERE wallet_id = $1 AND currency = $2`,
		toPGUUID(walletID), string(cur)).Scan(&credits, &debits, &count)
	if err != nil {
		return money.Money{}, 0, fmt.Errorf("somando ledger: %w", err)
	}
	total, err := money.FromMinor(*credits-*debits, cur)
	if err != nil {
		return money.Money{}, 0, err
	}
	return total, count, nil
}

// Page devolve uma página estável por seq (cursor opaco = último seq visto).
// Devolve também o maior seq da página, para montar o próximo cursor.
func (LedgerStore) Page(ctx context.Context, db DBTX, walletID uuid.UUID, afterSeq int64, limit int) ([]wallet.LedgerEntry, int64, error) {
	rows, err := db.Query(ctx, `SELECT seq, id, wallet_id, transaction_id, direction, amount_minor, currency,
		balance_before_minor, balance_after_minor, created_at
		FROM wallet_ledger_entries WHERE wallet_id = $1 AND seq > $2
		ORDER BY seq LIMIT $3`, toPGUUID(walletID), afterSeq, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("paginando ledger: %w", err)
	}
	defer rows.Close()
	var out []wallet.LedgerEntry
	var lastSeq int64
	for rows.Next() {
		var seq int64
		e, err := scanEntryWithSeq(rows, &seq)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
		lastSeq = seq
	}
	return out, lastSeq, rows.Err()
}

// ReconcileSnapshot é o retrato de uma vez só para a reconciliação.
type ReconcileSnapshot struct {
	StoredMinor int64
	Currency    string
	Credits     int64
	Debits      int64
	Entries     int64
}

// SnapshotForReconcile lê saldo guardado e agregados do ledger num SELECT só: o mesmo objeto, sem falso positivo sob carga.
func (LedgerStore) SnapshotForReconcile(ctx context.Context, db DBTX, walletID uuid.UUID) (ReconcileSnapshot, error) {
	var snap ReconcileSnapshot
	err := db.QueryRow(ctx, `SELECT w.balance_minor, w.currency,
		COALESCE(SUM(e.amount_minor) FILTER (WHERE e.direction = 'CREDIT'), 0),
		COALESCE(SUM(e.amount_minor) FILTER (WHERE e.direction = 'DEBIT'), 0),
		COUNT(e.seq)
		FROM wallets w LEFT JOIN wallet_ledger_entries e
		  ON e.wallet_id = w.id AND e.currency = w.currency
		WHERE w.id = $1
		GROUP BY w.balance_minor, w.currency`, toPGUUID(walletID)).Scan(
		&snap.StoredMinor, &snap.Currency, &snap.Credits, &snap.Debits, &snap.Entries)
	if err != nil {
		return ReconcileSnapshot{}, fmt.Errorf("lendo retrato: %w", err)
	}
	return snap, nil
}

func scanEntryWithSeq(row interface{ Scan(...any) error }, seq *int64) (wallet.LedgerEntry, error) {
	var id, walletID, txID pgtype.UUID
	var direction, currency string
	var amount, before, after int64
	var createdAt time.Time
	if err := row.Scan(seq, &id, &walletID, &txID, &direction, &amount, &currency, &before, &after, &createdAt); err != nil {
		return wallet.LedgerEntry{}, fmt.Errorf("lendo lançamento: %w", err)
	}
	entryID, err := fromPGUUID("id", id)
	if err != nil {
		return wallet.LedgerEntry{}, err
	}
	wID, err := fromPGUUID("wallet_id", walletID)
	if err != nil {
		return wallet.LedgerEntry{}, err
	}
	tID, err := fromPGUUID("transaction_id", txID)
	if err != nil {
		return wallet.LedgerEntry{}, err
	}
	amt, err := moneyFromRow("amount_minor", amount, currency)
	if err != nil {
		return wallet.LedgerEntry{}, err
	}
	balBefore, err := moneyFromRow("balance_before_minor", before, currency)
	if err != nil {
		return wallet.LedgerEntry{}, err
	}
	balAfter, err := moneyFromRow("balance_after_minor", after, currency)
	if err != nil {
		return wallet.LedgerEntry{}, err
	}
	return wallet.NewLedgerEntry(entryID, wID, tID, wallet.Direction(direction), amt, balBefore, balAfter, createdAt)
}
