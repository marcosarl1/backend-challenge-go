package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/marcosarl1/backend-challenge-go/internal/domain"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wallet"
)

// WalletStore guarda e carrega carteiras. Todo método recebe a conexão (pool ou transação): quem decide o contorno é a unidade de trabalho.
type WalletStore struct{}

// Insert cria a carteira zerada na versão 1.
func (WalletStore) Insert(ctx context.Context, db DBTX, w *wallet.Wallet) error {
	snap := w.Snapshot()
	_, err := db.Exec(ctx, `INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		toPGUUID(snap.ID), toPGUUID(snap.PlayerID), string(snap.Currency),
		snap.Balance.Minor(), snap.Version, snap.CreatedAt, snap.UpdatedAt)
	if err != nil {
		return fmt.Errorf("inserindo carteira: %w", err)
	}
	return nil
}

// walletRow é a linha lida do banco.
type walletRow struct {
	ID           pgtype.UUID
	PlayerID     pgtype.UUID
	Currency     string
	BalanceMinor int64
	Version      int64
	Opened       bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

const walletColumns = `w.id, w.player_id, w.currency, w.balance_minor, w.version,
	EXISTS (SELECT 1 FROM wager_transactions o WHERE o.wallet_id = w.id AND o.kind = 'OPENING') AS opened,
	w.created_at, w.updated_at`

func scanWallet(row interface{ Scan(...any) error }) (*wallet.Wallet, error) {
	var r walletRow
	if err := row.Scan(&r.ID, &r.PlayerID, &r.Currency, &r.BalanceMinor, &r.Version, &r.Opened, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, fmt.Errorf("lendo carteira: %w", err)
	}
	id, err := fromPGUUID("id", r.ID)
	if err != nil {
		return nil, err
	}
	playerID, err := fromPGUUID("player_id", r.PlayerID)
	if err != nil {
		return nil, err
	}
	balance, err := moneyFromRow("balance_minor", r.BalanceMinor, r.Currency)
	if err != nil {
		return nil, err
	}
	return wallet.Rehydrate(wallet.Snapshot{
		ID: id, PlayerID: playerID, Currency: money.Currency(r.Currency),
		Balance: balance, Version: r.Version, Opened: r.Opened,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	})
}

// GetForUpdate trava a linha da carteira e devolve o agregado. É o ponto de serialização por carteira: operações da mesma carteira passam por aqui em fila, cada uma na sua transação.
func (WalletStore) GetForUpdate(ctx context.Context, db DBTX, id uuid.UUID) (*wallet.Wallet, error) {
	return scanWallet(db.QueryRow(ctx, `SELECT `+walletColumns+` FROM wallets w WHERE w.id = $1 FOR UPDATE`, toPGUUID(id)))
}

// Get devolve sem travar (leituras e reconciliação usam retrato).
func (WalletStore) Get(ctx context.Context, db DBTX, id uuid.UUID) (*wallet.Wallet, error) {
	return scanWallet(db.QueryRow(ctx, `SELECT `+walletColumns+` FROM wallets w WHERE w.id = $1`, toPGUUID(id)))
}

// GetByPlayerCurrency acha a carteira do par jogador+moeda.
func (WalletStore) GetByPlayerCurrency(ctx context.Context, db DBTX, playerID uuid.UUID, cur money.Currency) (*wallet.Wallet, error) {
	return scanWallet(db.QueryRow(ctx, `SELECT `+walletColumns+` FROM wallets w WHERE w.player_id = $1 AND w.currency = $2`,
		toPGUUID(playerID), string(cur)))
}

// UpdateBalance grava saldo e versão novos, mas só se a versão for a esperada: linhas afetadas zero significam que outro escritor passou na frente (com o FOR UPDATE isso não acontece; sem ele, o erro aparece aqui em vez de uma atualização perdida).
func (WalletStore) UpdateBalance(ctx context.Context, db DBTX, w *wallet.Wallet, prevVersion int64, now time.Time) error {
	snap := w.Snapshot()
	tag, err := db.Exec(ctx, `UPDATE wallets SET balance_minor = $2, version = $3, updated_at = $4
		WHERE id = $1 AND version = $5`,
		toPGUUID(snap.ID), snap.Balance.Minor(), snap.Version, now, prevVersion)
	if err != nil {
		return fmt.Errorf("atualizando saldo: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: carteira %s versão %d", domain.ErrInvalidTransition, snap.ID, prevVersion)
	}
	return nil
}
