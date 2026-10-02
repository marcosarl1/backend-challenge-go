package application

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wager"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wallet"
)

// WalletRepository é o que os casos de uso precisam das carteiras.
type WalletRepository interface {
	Insert(ctx context.Context, w *wallet.Wallet) error
	GetForUpdate(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error)
	Get(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error)
	GetByPlayerCurrency(ctx context.Context, playerID uuid.UUID, cur money.Currency) (*wallet.Wallet, error)
	UpdateBalance(ctx context.Context, w *wallet.Wallet, prevVersion int64, now time.Time) error
}

// DueTransaction é uma pendência vencida com a correlação original (para os eventos que a retomada emitir).
type DueTransaction struct {
	Tx            *wager.WagerTransaction
	CorrelationID string
}

// WagerRepository é o que os casos de uso precisam das transações.
type WagerRepository interface {
	Insert(ctx context.Context, tx *wager.WagerTransaction, correlationID string) (bool, error)
	FindByProviderKey(ctx context.Context, providerID, key string) (*wager.WagerTransaction, error)
	FindByProviderExternal(ctx context.Context, providerID, externalID string) (*wager.WagerTransaction, error)
	FindByID(ctx context.Context, id uuid.UUID) (*wager.WagerTransaction, error)
	Save(ctx context.Context, tx *wager.WagerTransaction) error
	ClaimDue(ctx context.Context, now time.Time, limit int) ([]DueTransaction, error)
	HasSuccessfulReversal(ctx context.Context, id uuid.UUID) (bool, error)
}

// LedgerRepository é o que os casos de uso precisam do ledger.
type LedgerRepository interface {
	Insert(ctx context.Context, e wallet.LedgerEntry) error
	SumByWallet(ctx context.Context, walletID uuid.UUID, cur money.Currency) (money.Money, int64, error)
	Page(ctx context.Context, walletID uuid.UUID, afterSeq int64, limit int) ([]wallet.LedgerEntry, error)
}

// InboxRepository é o que o consumidor precisa da caixa de entrada.
type InboxRepository interface {
	Insert(ctx context.Context, consumer, messageID string, hash []byte, now time.Time) (bool, error)
	HashOf(ctx context.Context, consumer, messageID string) ([]byte, error)
}

// OutboxEvent é o evento pronto para enfileirar (payload já serializado).
type OutboxEvent struct {
	ID            uuid.UUID
	AggregateType string
	AggregateID   uuid.UUID
	EventType     string
	EventVersion  int
	OrderingKey   string
	CorrelationID string
	CausationID   string
	Payload       []byte
	OccurredAt    time.Time
}

// OutboxRepository é o que os casos de uso e o publicador precisam.
type OutboxRepository interface {
	Insert(ctx context.Context, e OutboxEvent, nextAttempt time.Time) error
}

// Repositories junta as portas para a unidade de trabalho entregar de uma
// vez, todas amarradas na mesma transação.
type Repositories struct {
	Wallets WalletRepository
	Wagers  WagerRepository
	Ledger  LedgerRepository
	Inbox   InboxRepository
	Outbox  OutboxRepository
}

// UnitOfWork executa a função com todas as portas na mesma transação.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context, r Repositories) error) error
}

// Clock é o relógio injetado (o domínio e os casos de uso não leem a hora
// sozinhos).
type Clock interface {
	Now() time.Time
}

// SystemClock entrega a hora atual em UTC.
type SystemClock struct{}

// Now devolve o instante atual em UTC.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// IDGenerator gera identificadores únicos (UUIDv7).
type IDGenerator interface {
	NewID() (uuid.UUID, error)
}

// UUIDv7Generator gera UUIDv7.
type UUIDv7Generator struct{}

// NewID gera um UUIDv7.
func (UUIDv7Generator) NewID() (uuid.UUID, error) { return uuid.NewV7() }
