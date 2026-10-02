package wallet

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/domain"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
)

// Direction indica o sentido do lançamento: saída (DEBIT) ou entrada (CREDIT).
type Direction string

const (
	DirectionDebit  Direction = "DEBIT"
	DirectionCredit Direction = "CREDIT"
)

type LedgerEntry struct {
	id            uuid.UUID
	walletID      uuid.UUID
	transactionID uuid.UUID
	direction     Direction
	amount        money.Money
	balanceBefore money.Money
	balanceAfter  money.Money
	createdAt     time.Time
}

func NewLedgerEntry(id, walletID, transactionID uuid.UUID, direction Direction, amount, balanceBefore, balanceAfter money.Money, createdAt time.Time) (LedgerEntry, error) {
	if id == uuid.Nil || walletID == uuid.Nil || transactionID == uuid.Nil {
		return LedgerEntry{}, fmt.Errorf("%w: identificador vazio no lançamento", domain.ErrUninitialized)
	}
	if direction != DirectionDebit && direction != DirectionCredit {
		return LedgerEntry{}, fmt.Errorf("%w: direção %q", domain.ErrInvalidMoney, direction)
	}
	if !amount.Valid() || !balanceBefore.Valid() || !balanceAfter.Valid() {
		return LedgerEntry{}, domain.ErrUninitialized
	}
	if createdAt.IsZero() {
		return LedgerEntry{}, fmt.Errorf("%w: instante vazio no lançamento", domain.ErrUninitialized)
	}
	zero, _ := money.Zero(amount.Currency())
	if cmp, _ := amount.Cmp(zero); cmp <= 0 {
		return LedgerEntry{}, fmt.Errorf("%w: lançamento exige valor positivo", domain.ErrInvalidMoney)
	}
	expected, err := apply(balanceBefore, direction, amount)
	if err != nil {
		return LedgerEntry{}, err
	}
	cmp, err := expected.Cmp(balanceAfter)
	if err != nil {
		return LedgerEntry{}, err
	}
	if cmp != 0 {
		return LedgerEntry{}, fmt.Errorf("%w: saldo posterior inconsistente", domain.ErrInvalidMoney)
	}
	// O saldo de carteira nunca é negativo; um lançamento que terminasse abaixo de zero viola a invariante.
	floor, _ := money.Zero(balanceAfter.Currency())
	if cmp, _ := balanceAfter.Cmp(floor); cmp < 0 {
		return LedgerEntry{}, fmt.Errorf("%w: saldo posterior negativo", domain.ErrInvalidMoney)
	}
	return LedgerEntry{
		id: id, walletID: walletID, transactionID: transactionID,
		direction: direction, amount: amount,
		balanceBefore: balanceBefore, balanceAfter: balanceAfter,
		createdAt: createdAt,
	}, nil
}

// apply soma ou subtrai conforme a direção. Exige mesma moeda nos dois valores (o erro de incompatibilidade vem do próprio Money).
func apply(balance money.Money, direction Direction, amount money.Money) (money.Money, error) {
	if direction == DirectionCredit {
		return balance.Add(amount)
	}
	return balance.Sub(amount)
}

// Leitores do lançamento (a escrita acontece só na construção).
func (e LedgerEntry) ID() uuid.UUID              { return e.id }
func (e LedgerEntry) WalletID() uuid.UUID        { return e.walletID }
func (e LedgerEntry) TransactionID() uuid.UUID   { return e.transactionID }
func (e LedgerEntry) Direction() Direction       { return e.direction }
func (e LedgerEntry) Amount() money.Money        { return e.amount }
func (e LedgerEntry) BalanceBefore() money.Money { return e.balanceBefore }
func (e LedgerEntry) BalanceAfter() money.Money  { return e.balanceAfter }
func (e LedgerEntry) CreatedAt() time.Time       { return e.createdAt }
