package wallet

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/domain"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
)

func testIDs() (id, playerID uuid.UUID) {
	var err error
	if id, err = uuid.NewV7(); err != nil {
		panic(err)
	}
	if playerID, err = uuid.NewV7(); err != nil {
		panic(err)
	}
	return id, playerID
}

func mustMoney(t *testing.T, amount string, cur string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, money.Currency(cur))
	if err != nil {
		t.Fatalf("Parse(%q) erro inesperado: %v", amount, err)
	}
	return m
}

func newFunded(t *testing.T, balance string) *Wallet {
	t.Helper()
	id, player := testIDs()
	now := time.Now().UTC()
	w, err := NewWallet(id, player, "BRL", now)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	tx, _ := uuid.NewV7()
	if _, err := w.ApplyOpening(tx, mustMoney(t, balance, "BRL"), now); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	return w
}

func TestNewWallet(t *testing.T) {
	id, player := testIDs()
	now := time.Now().UTC()
	w, err := NewWallet(id, player, "BRL", now)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if w.Balance().String() != "0.00" || w.Version() != 1 || w.Opened() {
		t.Fatalf("carteira nova inválida: saldo %s versão %d aberta=%v",
			w.Balance(), w.Version(), w.Opened())
	}

	for name, err := range map[string]error{
		"id vazio": func() error {
			_, err := NewWallet(uuid.Nil, player, "BRL", now)
			return err
		}(),
		"jogador vazio": func() error {
			_, err := NewWallet(id, uuid.Nil, "BRL", now)
			return err
		}(),
		"instante vazio": func() error {
			_, err := NewWallet(id, player, "BRL", time.Time{})
			return err
		}(),
	} {
		if !errors.Is(err, domain.ErrUninitialized) {
			t.Fatalf("%s erro = %v", name, err)
		}
	}
	if _, err := NewWallet(id, player, "JPY", now); !errors.Is(err, domain.ErrInvalidCurrency) {
		t.Fatalf("moeda ruim erro = %v", err)
	}
}

func TestOpening(t *testing.T) {
	w := newFunded(t, "100.00")
	if w.Balance().String() != "100.00" {
		t.Fatalf("saldo = %s", w.Balance())
	}
	if w.Version() != 1 {
		t.Fatalf("abertura não anda versão, versão = %d", w.Version())
	}
	if !w.Opened() {
		t.Fatal("carteira deveria estar aberta")
	}

	// Segunda abertura é rejeitada e nada muda.
	tx, _ := uuid.NewV7()
	before := w.Snapshot()
	if _, err := w.ApplyOpening(tx, mustMoney(t, "10.00", "BRL"), time.Now().UTC()); !errors.Is(err, domain.ErrAlreadyOpened) {
		t.Fatalf("abertura dupla erro = %v", err)
	}
	if after := w.Snapshot(); after != before {
		t.Fatalf("estado mudou na falha: %+v", after)
	}

	// Abertura zerada não existe.
	id, player := testIDs()
	now := time.Now().UTC()
	fresh, _ := NewWallet(id, player, "BRL", now)
	zero, _ := money.Zero("BRL")
	if _, err := fresh.ApplyOpening(tx, zero, now); !errors.Is(err, domain.ErrInvalidMoney) {
		t.Fatalf("abertura zero erro = %v", err)
	}
	if fresh.Opened() {
		t.Fatal("abertura zero não pode abrir")
	}
}

func TestDebitCredit(t *testing.T) {
	now := time.Now().UTC()
	w := newFunded(t, "100.00")

	tx, _ := uuid.NewV7()
	out, err := w.Debit(tx, mustMoney(t, "30.00", "BRL"), now)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if w.Balance().String() != "70.00" || w.Version() != 2 {
		t.Fatalf("saldo %s versão %d", w.Balance(), w.Version())
	}
	if out.Direction() != DirectionDebit || out.Amount().String() != "30.00" ||
		out.BalanceBefore().String() != "100.00" || out.BalanceAfter().String() != "70.00" {
		t.Fatalf("lançamento inconsistente: %+v", out)
	}

	tx2, _ := uuid.NewV7()
	in, err := w.Credit(tx2, mustMoney(t, "5.00", "BRL"), now)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if w.Balance().String() != "75.00" || w.Version() != 3 {
		t.Fatalf("saldo %s versão %d", w.Balance(), w.Version())
	}
	if in.Direction() != DirectionCredit || in.BalanceAfter().String() != "75.00" {
		t.Fatalf("lançamento inconsistente: %+v", in)
	}
}

func TestDebitWithoutFundsChangesNothing(t *testing.T) {
	w := newFunded(t, "100.00")
	before := w.Snapshot()
	tx, _ := uuid.NewV7()
	if _, err := w.Debit(tx, mustMoney(t, "100.01", "BRL"), time.Now().UTC()); !errors.Is(err, domain.ErrInsufficientFunds) {
		t.Fatalf("erro = %v", err)
	}
	if after := w.Snapshot(); after != before {
		t.Fatalf("estado mudou na falha: %+v", after)
	}
	// Gastar tudo é permitido, desde que não fique negativo.
	if _, err := w.Debit(tx, mustMoney(t, "100.00", "BRL"), time.Now().UTC()); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if w.Balance().String() != "0.00" {
		t.Fatalf("saldo = %s", w.Balance())
	}
}

func TestZeroMovesAreRejected(t *testing.T) {
	// Sem movimento não há lançamento nem versão nova: é assim que uma peração sem efeito (como LOSS) passa pela carteira sem deixar rastro.
	w := newFunded(t, "100.00")
	before := w.Snapshot()
	zero, _ := money.Zero("BRL")
	tx, _ := uuid.NewV7()
	now := time.Now().UTC()
	if _, err := w.Debit(tx, zero, now); !errors.Is(err, domain.ErrInvalidMoney) {
		t.Fatalf("débito zero erro = %v", err)
	}
	if _, err := w.Credit(tx, zero, now); !errors.Is(err, domain.ErrInvalidMoney) {
		t.Fatalf("crédito zero erro = %v", err)
	}
	if after := w.Snapshot(); after != before {
		t.Fatalf("estado mudou: %+v", after)
	}
	if w.Version() != 1 {
		t.Fatalf("versão = %d, esperado 1", w.Version())
	}
}

func TestCurrencyMismatch(t *testing.T) {
	w := newFunded(t, "100.00")
	tx, _ := uuid.NewV7()
	usd := mustMoney(t, "10.00", "USD")
	if _, err := w.Debit(tx, usd, time.Now().UTC()); !errors.Is(err, domain.ErrCurrencyMismatch) {
		t.Fatalf("débito em USD erro = %v", err)
	}
	if _, err := w.Credit(tx, usd, time.Now().UTC()); !errors.Is(err, domain.ErrCurrencyMismatch) {
		t.Fatalf("crédito em USD erro = %v", err)
	}
	if _, err := w.ApplyOpening(tx, usd, time.Now().UTC()); !errors.Is(err, domain.ErrAlreadyOpened) {
		t.Fatalf("abertura em carteira aberta erro = %v", err)
	}
}

func TestCreditOverflowChangesNothing(t *testing.T) {
	id, player := testIDs()
	now := time.Now().UTC()
	w, _ := NewWallet(id, player, "BRL", now)
	top, _ := money.FromMinor(9223372036854775807, "BRL")
	tx, _ := uuid.NewV7()
	if _, err := w.Credit(tx, top, now); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	before := w.Snapshot()
	one, _ := money.FromMinor(1, "BRL")
	if _, err := w.Credit(tx, one, now); !errors.Is(err, domain.ErrOverflow) {
		t.Fatalf("erro = %v", err)
	}
	if after := w.Snapshot(); after != before {
		t.Fatalf("estado mudou na falha: %+v", after)
	}
}

func TestRehydrate(t *testing.T) {
	original := newFunded(t, "42.50")
	tx, _ := uuid.NewV7()
	if _, err := original.Credit(tx, mustMoney(t, "7.50", "BRL"), time.Now().UTC()); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	// Reidratar não gera lançamento nem mexe em nada: volta igual.
	back, err := Rehydrate(original.Snapshot())
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if back.Snapshot() != original.Snapshot() {
		t.Fatalf("reidratação divergiu: %+v", back.Snapshot())
	}

	good := original.Snapshot()
	badBalance, _ := money.FromMinor(-1, "BRL")
	usd, _ := money.FromMinor(10, "USD")
	cases := []struct {
		name string
		mut  func(*Snapshot)
	}{
		{"id vazio", func(s *Snapshot) { s.ID = uuid.Nil }},
		{"jogador vazio", func(s *Snapshot) { s.PlayerID = uuid.Nil }},
		{"moeda ruim", func(s *Snapshot) { s.Currency = "JPY" }},
		{"saldo de outra moeda", func(s *Snapshot) { s.Balance = usd }},
		{"saldo negativo", func(s *Snapshot) { s.Balance = badBalance }},
		{"versão zero", func(s *Snapshot) { s.Version = 0 }},
		{"criação vazia", func(s *Snapshot) { s.CreatedAt = time.Time{} }},
		{"atualização vazia", func(s *Snapshot) { s.UpdatedAt = time.Time{} }},
		{"atualização antes da criação", func(s *Snapshot) {
			s.CreatedAt, s.UpdatedAt = s.UpdatedAt, s.CreatedAt
		}},
	}
	for _, tt := range cases {
		snap := good
		tt.mut(&snap)
		if _, err := Rehydrate(snap); err == nil {
			t.Fatalf("%s aceito, esperado rejeição", tt.name)
		}
	}
}

func TestLedgerEntryValidation(t *testing.T) {
	id, _ := testIDs()
	wid, _ := testIDs()
	tx, _ := uuid.NewV7()
	ten := mustMoney(t, "10.00", "BRL")
	fifteen := mustMoney(t, "15.00", "BRL")
	zero, _ := money.Zero("BRL")
	now := time.Now().UTC()
	entry := func(direction Direction, amount, before, after money.Money) (LedgerEntry, error) {
		return NewLedgerEntry(id, wid, tx, direction, amount, before, after, now)
	}

	if _, err := entry(DirectionDebit, ten, fifteen, mustMoney(t, "5.00", "BRL")); err != nil {
		t.Fatalf("lançamento válido rejeitado: %v", err)
	}
	if _, err := entry(DirectionCredit, ten, fifteen, mustMoney(t, "25.00", "BRL")); err != nil {
		t.Fatalf("lançamento válido rejeitado: %v", err)
	}

	// Ambos os sentidos com a conta errada são rejeitados, bem como o
	// saldo posterior negativo (a carteira nunca fica abaixo de zero).
	negFive, _ := money.FromMinor(-500, "BRL")
	for _, tt := range []struct {
		name      string
		direction Direction
		before    money.Money
		amount    money.Money
		after     money.Money
	}{
		{"débito errado", DirectionDebit, fifteen, ten, mustMoney(t, "6.00", "BRL")},
		{"crédito errado", DirectionCredit, fifteen, ten, mustMoney(t, "24.00", "BRL")},
		{"saldo negativo", DirectionDebit, mustMoney(t, "5.00", "BRL"), ten, negFive},
	} {
		if _, err := entry(tt.direction, tt.amount, tt.before, tt.after); err == nil {
			t.Fatalf("%s aceito, esperado rejeição", tt.name)
		}
	}
	// Moedas misturadas não fecham a conta.
	usd, _ := money.Parse("10.00", "USD")
	if _, err := entry(DirectionDebit, usd, fifteen, mustMoney(t, "5.00", "BRL")); !errors.Is(err, domain.ErrCurrencyMismatch) {
		t.Fatalf("moeda misturada erro = %v", err)
	}
	if _, err := NewLedgerEntry(id, wid, tx, "SIDEWAYS", ten, fifteen, fifteen, now); err == nil {
		t.Fatal("direção desconhecida aceita")
	}
	if _, err := entry(DirectionDebit, zero, fifteen, fifteen); err == nil {
		t.Fatal("valor zero aceito")
	}
	if _, err := NewLedgerEntry(uuid.Nil, wid, tx, DirectionDebit, ten, fifteen, fifteen, now); err == nil {
		t.Fatal("id vazio aceito")
	}
	if _, err := NewLedgerEntry(id, wid, tx, DirectionDebit, ten, fifteen, mustMoney(t, "5.00", "BRL"), time.Time{}); err == nil {
		t.Fatal("instante vazio aceito")
	}
}

func TestLedgerEntryKeepsMovementInstant(t *testing.T) {
	// O lançamento carrega o instante do movimento e não muda quando a carteira anda: é o retrato daquela operação.
	w := newFunded(t, "100.00")
	moment := time.Now().UTC().Add(time.Hour)
	tx, _ := uuid.NewV7()
	out, err := w.Debit(tx, mustMoney(t, "30.00", "BRL"), moment)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if !out.CreatedAt().Equal(moment) {
		t.Fatalf("instante = %v, esperado %v", out.CreatedAt(), moment)
	}
	tx2, _ := uuid.NewV7()
	if _, err := w.Credit(tx2, mustMoney(t, "5.00", "BRL"), moment.Add(time.Minute)); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if out.BalanceBefore().String() != "100.00" || out.BalanceAfter().String() != "70.00" {
		t.Fatalf("lançamento mudou depois: %+v", out)
	}
}
