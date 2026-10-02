package application

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
)

// MaxPageSize é o teto do tamanho da página: pedido maior é cortado aqui.
const MaxPageSize = 100

// DefaultPageSize vale quando o pedido não diz o tamanho.
const DefaultPageSize = 50

// WalletView é a carteira como leitura.
type WalletView struct {
	ID        uuid.UUID
	PlayerID  uuid.UUID
	Currency  money.Currency
	Balance   money.Money
	Version   int64
	Opened    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// GetWallet lê a carteira.
func GetWallet(ctx context.Context, uow UnitOfWork, walletID uuid.UUID) (*WalletView, error) {
	var out *WalletView
	err := uow.Do(ctx, func(ctx context.Context, r Repositories) error {
		w, err := r.Wallets.Get(ctx, walletID)
		if err != nil {
			return mapNotFound(err, "carteira")
		}
		snap := w.Snapshot()
		out = &WalletView{
			ID: snap.ID, PlayerID: snap.PlayerID, Currency: snap.Currency,
			Balance: snap.Balance, Version: snap.Version, Opened: snap.Opened,
			CreatedAt: snap.CreatedAt, UpdatedAt: snap.UpdatedAt,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// LedgerEntryView é o lançamento como leitura (sem o seq interno).
type LedgerEntryView struct {
	ID            uuid.UUID
	WalletID      uuid.UUID
	TransactionID uuid.UUID
	Direction     string
	Amount        money.Money
	BalanceBefore money.Money
	BalanceAfter  money.Money
	CreatedAt     time.Time
}

// LedgerPage é uma página: os lançamentos em ordem estável e o cursor para a próxima (vazio quando acabou).
type LedgerPage struct {
	Entries    []LedgerEntryView
	NextCursor string
}

// ListLedger pagina o ledger em ordem estável. Cursor vazio começa do início; cursor inválido é entrada inválida (o HTTP vira 400).
func ListLedger(ctx context.Context, uow UnitOfWork, walletID uuid.UUID, cursor string, limit int) (*LedgerPage, error) {
	if walletID == uuid.Nil {
		return nil, fmt.Errorf("%w: carteira vazia", ErrInvalidInput)
	}
	after, err := decodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	size := limit
	if size <= 0 {
		size = DefaultPageSize
	}
	if size > MaxPageSize {
		size = MaxPageSize
	}
	page := &LedgerPage{}
	err = uow.Do(ctx, func(ctx context.Context, r Repositories) error {
		if _, err := r.Wallets.Get(ctx, walletID); err != nil {
			return mapNotFound(err, "carteira")
		}
		entries, lastSeq, err := r.Ledger.Page(ctx, walletID, after, size)
		if err != nil {
			return err
		}
		for _, e := range entries {
			page.Entries = append(page.Entries, LedgerEntryView{
				ID: e.ID(), WalletID: e.WalletID(), TransactionID: e.TransactionID(),
				Direction: string(e.Direction()), Amount: e.Amount(),
				BalanceBefore: e.BalanceBefore(), BalanceAfter: e.BalanceAfter(),
				CreatedAt: e.CreatedAt(),
			})
		}
		if len(entries) == size {
			page.NextCursor = encodeCursor(lastSeq)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return page, nil
}

// ledgerCursor guarda só o seq: opaco para fora, estável para dentro.
type ledgerCursor struct {
	Seq int64 `json:"seq"`
}

func encodeCursor(seq int64) string {
	data, _ := json.Marshal(ledgerCursor{Seq: seq})
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeCursor(cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, fmt.Errorf("%w: cursor malformado", ErrInvalidInput)
	}
	var decoded ledgerCursor
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.Seq < 0 {
		return 0, fmt.Errorf("%w: cursor malformado", ErrInvalidInput)
	}
	return decoded.Seq, nil
}
