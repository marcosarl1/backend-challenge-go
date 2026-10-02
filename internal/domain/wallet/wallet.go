package wallet

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/domain"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
)

/*
 * Wallet é a raiz do agregado financeiro: identidade, jogador, moeda, saldo,
 versão e abertura. Todo o estado muda sob controle dos métodos abaixo; não
 há escrita direta nos campos.

 A versão começa em 1 e só anda quando o saldo muda (débito ou crédito). A
 abertura credita sem mexer na versão (ela continua 1), então "versão"
 conta quantas movimentações a carteira sofreu.
*/

type Wallet struct {
	id        uuid.UUID
	playerID  uuid.UUID
	currency  money.Currency
	balance   money.Money
	version   int64
	opened    bool
	createdAt time.Time
	updatedAt time.Time
}

// Snapshot é a foto do estado para guardar no banco e reidratar depois.
type Snapshot struct {
	ID        uuid.UUID
	PlayerID  uuid.UUID
	Currency  money.Currency
	Balance   money.Money
	Version   int64
	Opened    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewWallet cria uma carteira zerada
func NewWallet(id, playerID uuid.UUID, cur money.Currency, now time.Time) (*Wallet, error) {
	if id == uuid.Nil || playerID == uuid.Nil {
		return nil, fmt.Errorf("%w: identificador vazio na carteira", domain.ErrUninitialized)
	}
	zero, err := money.Zero(cur)
	if err != nil {
		return nil, err
	}
	if now.IsZero() {
		return nil, fmt.Errorf("%w: instante vazio na carteira", domain.ErrUninitialized)
	}
	return &Wallet{
		id: id, playerID: playerID, currency: cur, balance: zero,
		version: 1, createdAt: now, updatedAt: now,
	}, nil
}

// Rehydrate reconstrói a carteira a partir do que está guardado, conferindo
// as invariantes. Não aplica nada, não emite nada: é só leitura validada.
func Rehydrate(s Snapshot) (*Wallet, error) {
	if s.ID == uuid.Nil || s.PlayerID == uuid.Nil {
		return nil, fmt.Errorf("%w: identificador vazio na carteira", domain.ErrUninitialized)
	}
	if !s.Currency.Valid() {
		return nil, fmt.Errorf("%w: %q", domain.ErrInvalidCurrency, string(s.Currency))
	}
	if !s.Balance.Valid() {
		return nil, fmt.Errorf("%w: saldo guardado", domain.ErrUninitialized)
	}
	if cur := s.Balance.Currency(); cur != s.Currency {
		return nil, fmt.Errorf("%w: %q vs %q", domain.ErrCurrencyMismatch, cur, s.Currency)
	}
	floor, _ := money.Zero(s.Currency)
	if cmp, _ := s.Balance.Cmp(floor); cmp < 0 {
		return nil, fmt.Errorf("%w: saldo guardado negativo", domain.ErrInvalidMoney)
	}
	if s.Version < 1 {
		return nil, fmt.Errorf("%w: versão %d", domain.ErrInvalidMoney, s.Version)
	}
	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		return nil, fmt.Errorf("%w: instante vazio na carteira", domain.ErrUninitialized)
	}
	if s.UpdatedAt.Before(s.CreatedAt) {
		return nil, fmt.Errorf("%w: atualização antes da criação", domain.ErrInvalidMoney)
	}
	return &Wallet{
		id: s.ID, playerID: s.PlayerID, currency: s.Currency, balance: s.Balance,
		version: s.Version, opened: s.Opened,
		createdAt: s.CreatedAt, updatedAt: s.UpdatedAt,
	}, nil
}

// Leitores (o saldo e a versão só mudam pelos métodos de movimento).
func (w *Wallet) ID() uuid.UUID            { return w.id }
func (w *Wallet) PlayerID() uuid.UUID      { return w.playerID }
func (w *Wallet) Currency() money.Currency { return w.currency }
func (w *Wallet) Balance() money.Money     { return w.balance }
func (w *Wallet) Version() int64           { return w.version }
func (w *Wallet) Opened() bool             { return w.opened }
func (w *Wallet) CreatedAt() time.Time     { return w.createdAt }
func (w *Wallet) UpdatedAt() time.Time     { return w.updatedAt }

// Snapshot devolve a objeto atual para persistir.
func (w *Wallet) Snapshot() Snapshot {
	return Snapshot{
		ID: w.id, PlayerID: w.playerID, Currency: w.currency, Balance: w.balance,
		Version: w.version, Opened: w.opened,
		CreatedAt: w.createdAt, UpdatedAt: w.updatedAt,
	}
}

/*
* ApplyOpening aplica o crédito inicial, uma única vez, sem andar a versão
(ela permanece 1). Valor zerado não abre carteira: quem abre com zero só
cria a carteira, sem lançamento.
*/
func (w *Wallet) ApplyOpening(txID uuid.UUID, amt money.Money, now time.Time) (LedgerEntry, error) {
	if w.opened {
		return LedgerEntry{}, domain.ErrAlreadyOpened
	}
	after, entry, err := w.prepare(txID, amt, DirectionCredit, now)
	if err != nil {
		return LedgerEntry{}, err
	}
	w.balance = after
	w.opened = true
	w.updatedAt = now
	return entry, nil
}

// Debit tira valor da carteira. Sem saldo suficiente, nada muda e volta saldo insuficiente.
func (w *Wallet) Debit(txID uuid.UUID, amt money.Money, now time.Time) (LedgerEntry, error) {
	if cmp, err := w.balance.Cmp(amt); err != nil {
		return LedgerEntry{}, err
	} else if cmp < 0 {
		return LedgerEntry{}, fmt.Errorf("%w: %s < %s", domain.ErrInsufficientFunds, w.balance, amt)
	}
	after, entry, err := w.prepare(txID, amt, DirectionDebit, now)
	if err != nil {
		return LedgerEntry{}, err
	}
	w.balance = after
	w.version++
	w.updatedAt = now
	return entry, nil
}

// Credit põe valor na carteira.
func (w *Wallet) Credit(txID uuid.UUID, amt money.Money, now time.Time) (LedgerEntry, error) {
	after, entry, err := w.prepare(txID, amt, DirectionCredit, now)
	if err != nil {
		return LedgerEntry{}, err
	}
	w.balance = after
	w.version++
	w.updatedAt = now
	return entry, nil
}

// prepare confere tudo e monta o lançamento, sem encostar no estado. Se der erro aqui, a carteira continua exatamente como estava.
func (w *Wallet) prepare(txID uuid.UUID, amt money.Money, direction Direction, now time.Time) (after money.Money, entry LedgerEntry, err error) {
	if txID == uuid.Nil {
		return after, entry, fmt.Errorf("%w: transação vazia no movimento", domain.ErrUninitialized)
	}
	if now.IsZero() {
		return after, entry, fmt.Errorf("%w: instante vazio no movimento", domain.ErrUninitialized)
	}
	if !amt.Valid() {
		return after, entry, domain.ErrUninitialized
	}
	if cur := amt.Currency(); cur != w.currency {
		return after, entry, fmt.Errorf("%w: %q vs %q", domain.ErrCurrencyMismatch, cur, w.currency)
	}
	zero, _ := money.Zero(w.currency)
	if cmp, _ := amt.Cmp(zero); cmp <= 0 {
		return after, entry, fmt.Errorf("%w: movimento exige valor positivo", domain.ErrInvalidMoney)
	}
	after, err = apply(w.balance, direction, amt)
	if err != nil {
		return after, entry, err
	}
	entryID, err := uuid.NewV7()
	if err != nil {
		return after, entry, fmt.Errorf("gerando lançamento: %w", err)
	}
	entry, err = NewLedgerEntry(entryID, w.id, txID, direction, amt, w.balance, after, now)
	if err != nil {
		return after, entry, err
	}
	return after, entry, nil
}
