package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wager"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wallet"
)

// mapConflict traduz violação de unicidade do banco em conflito da aplicação.
func mapConflict(err error) error {
	if ce, ok := AsConstraint(err); ok {
		return &application.ConflictError{Constraint: ce.Constraint, Err: err}
	}
	return err
}

type walletAdapter struct{ db DBTX }

func (a walletAdapter) Insert(ctx context.Context, w *wallet.Wallet) error {
	return mapConflict(WalletStore{}.Insert(ctx, a.db, w))
}

func (a walletAdapter) GetForUpdate(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	return WalletStore{}.GetForUpdate(ctx, a.db, id)
}

func (a walletAdapter) Get(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	return WalletStore{}.Get(ctx, a.db, id)
}

func (a walletAdapter) GetByPlayerCurrency(ctx context.Context, playerID uuid.UUID, cur money.Currency) (*wallet.Wallet, error) {
	return WalletStore{}.GetByPlayerCurrency(ctx, a.db, playerID, cur)
}

func (a walletAdapter) UpdateBalance(ctx context.Context, w *wallet.Wallet, prevVersion int64, now time.Time) error {
	return WalletStore{}.UpdateBalance(ctx, a.db, w, prevVersion, now)
}

type wagerAdapter struct{ db DBTX }

func (a wagerAdapter) Insert(ctx context.Context, tx *wager.WagerTransaction, correlationID string) (bool, error) {
	inserted, err := WagerStore{}.Insert(ctx, a.db, tx, correlationID)
	if err != nil {
		return false, mapConflict(err)
	}
	return inserted, nil
}

func (a wagerAdapter) FindByProviderKey(ctx context.Context, providerID, key string) (*wager.WagerTransaction, error) {
	return WagerStore{}.FindByProviderKey(ctx, a.db, providerID, key)
}

func (a wagerAdapter) FindByProviderExternal(ctx context.Context, providerID, externalID string) (*wager.WagerTransaction, error) {
	return WagerStore{}.FindByProviderExternal(ctx, a.db, providerID, externalID)
}

func (a wagerAdapter) FindByID(ctx context.Context, id uuid.UUID) (*wager.WagerTransaction, error) {
	return WagerStore{}.FindByID(ctx, a.db, id)
}

func (a wagerAdapter) Save(ctx context.Context, tx *wager.WagerTransaction) error {
	return WagerStore{}.Save(ctx, a.db, tx)
}

func (a wagerAdapter) ClaimDue(ctx context.Context, now time.Time, limit int) ([]application.DueTransaction, error) {
	dues, err := WagerStore{}.ClaimDue(ctx, a.db, now, limit)
	if err != nil {
		return nil, err
	}
	out := make([]application.DueTransaction, 0, len(dues))
	for _, d := range dues {
		out = append(out, application.DueTransaction{Tx: d.Tx, CorrelationID: d.CorrelationID})
	}
	return out, nil
}

func (a wagerAdapter) HasSuccessfulReversal(ctx context.Context, id uuid.UUID) (bool, error) {
	return WagerStore{}.HasSuccessfulReversal(ctx, a.db, id)
}

type ledgerAdapter struct{ db DBTX }

func (a ledgerAdapter) Insert(ctx context.Context, e wallet.LedgerEntry) error {
	return LedgerStore{}.Insert(ctx, a.db, e)
}

func (a ledgerAdapter) SumByWallet(ctx context.Context, walletID uuid.UUID, cur money.Currency) (money.Money, int64, error) {
	return LedgerStore{}.SumByWallet(ctx, a.db, walletID, cur)
}

func (a ledgerAdapter) Page(ctx context.Context, walletID uuid.UUID, afterSeq int64, limit int) ([]wallet.LedgerEntry, error) {
	return LedgerStore{}.Page(ctx, a.db, walletID, afterSeq, limit)
}

type inboxAdapter struct{ db DBTX }

func (a inboxAdapter) Insert(ctx context.Context, consumer, messageID string, hash []byte, now time.Time) (bool, error) {
	return InboxStore{}.Insert(ctx, a.db, consumer, messageID, hash, now)
}

func (a inboxAdapter) HashOf(ctx context.Context, consumer, messageID string) ([]byte, error) {
	return InboxStore{}.HashOf(ctx, a.db, consumer, messageID)
}

type outboxAdapter struct{ db DBTX }

func (a outboxAdapter) Insert(ctx context.Context, e application.OutboxEvent, nextAttempt time.Time) error {
	return OutboxStore{}.Insert(ctx, a.db, OutboxEvent{
		ID: e.ID, AggregateType: e.AggregateType, AggregateID: e.AggregateID,
		EventType: e.EventType, EventVersion: e.EventVersion, OrderingKey: e.OrderingKey,
		CorrelationID: e.CorrelationID, CausationID: e.CausationID,
		Payload: e.Payload, OccurredAt: e.OccurredAt,
	}, nextAttempt)
}

// RepositoriesFor amarra as portas na conexão dada (pool ou transação).
func RepositoriesFor(db DBTX) application.Repositories {
	return application.Repositories{
		Wallets: walletAdapter{db},
		Wagers:  wagerAdapter{db},
		Ledger:  ledgerAdapter{db},
		Inbox:   inboxAdapter{db},
		Outbox:  outboxAdapter{db},
	}
}

// Runner executa casos de uso com as portas na mesma transação.
type Runner struct {
	uow *UnitOfWork
}

// NewRunner monta o executor sobre a unidade de trabalho.
func NewRunner(uow *UnitOfWork) Runner {
	return Runner{uow: uow}
}

// Do executa fn com todas as portas na mesma transação.
func (r Runner) Do(ctx context.Context, fn func(ctx context.Context, repos application.Repositories) error) error {
	return r.uow.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return fn(ctx, RepositoriesFor(tx))
	})
}
