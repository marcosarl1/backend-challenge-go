package events

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/domain"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
)

func TestProcessedValidation(t *testing.T) {
	tx, wallet, player := uuid.New(), uuid.New(), uuid.New()
	base := ProcessedData{TransactionID: tx, WalletID: wallet, PlayerID: player, Kind: "BET", Amount: mustMoney(t, "1.00"), ResultBalance: mustMoney(t, "9.00"), ResultWalletVersion: 2}
	for _, tt := range []struct {
		name string
		mut  func(*ProcessedData)
		want error
	}{
		{"transação divergente", func(d *ProcessedData) { d.TransactionID = uuid.New() }, domain.ErrInvalidMoney},
		{"carteira vazia", func(d *ProcessedData) { d.WalletID = uuid.Nil }, domain.ErrUninitialized},
		{"jogador vazio", func(d *ProcessedData) { d.PlayerID = uuid.Nil }, domain.ErrUninitialized},
		{"tipo vazio", func(d *ProcessedData) { d.Kind = "" }, domain.ErrInvalidMoney},
		{"valor inválido", func(d *ProcessedData) { d.Amount = money.Money{} }, domain.ErrUninitialized},
		{"saldo inválido", func(d *ProcessedData) { d.ResultBalance = money.Money{} }, domain.ErrUninitialized},
		{"versão inválida", func(d *ProcessedData) { d.ResultWalletVersion = 0 }, domain.ErrInvalidMoney},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := base
			tt.mut(&data)
			if _, err := NewTransactionProcessed(uuid.New(), tx, "corr", "", fixtureTime(), data); !errors.Is(err, tt.want) {
				t.Fatalf("erro = %v, esperado %v", err, tt.want)
			}
		})
	}
	for _, tt := range []struct {
		name  string
		event uuid.UUID
		tx    uuid.UUID
		corr  string
		when  time.Time
		want  error
	}{
		{"evento vazio", uuid.Nil, tx, "corr", fixtureTime(), domain.ErrUninitialized},
		{"agregado vazio", uuid.New(), uuid.Nil, "corr", fixtureTime(), domain.ErrUninitialized},
		{"correlação vazia", uuid.New(), tx, "", fixtureTime(), domain.ErrInvalidMoney},
		{"instante vazio", uuid.New(), tx, "corr", time.Time{}, domain.ErrUninitialized},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewTransactionProcessed(tt.event, tt.tx, tt.corr, "", tt.when, base); !errors.Is(err, tt.want) {
				t.Fatalf("erro = %v, esperado %v", err, tt.want)
			}
		})
	}
	if _, err := NewTransactionRejected(uuid.New(), tx, "corr", "", fixtureTime(), RejectedData{ProcessedData: base}); !errors.Is(err, domain.ErrInvalidMoney) {
		t.Fatalf("rejeição sem código: %v", err)
	}
	bad := base
	bad.Kind = ""
	if _, err := NewTransactionRejected(uuid.New(), tx, "corr", "", fixtureTime(), RejectedData{ProcessedData: bad, FailureCode: "DENIED"}); !errors.Is(err, domain.ErrInvalidMoney) {
		t.Fatalf("rejeição com payload inválido: %v", err)
	}
	// PayloadJSON conserva o contrato de serialização usado pela outbox.
	env, err := NewTransactionProcessed(uuid.New(), tx, "corr", "cause", fixtureTime(), base)
	if err != nil {
		t.Fatalf("evento válido: %v", err)
	}
	got, err := env.PayloadJSON()
	if err != nil {
		t.Fatalf("PayloadJSON: %v", err)
	}
	want, _ := json.Marshal(env)
	if string(got) != string(want) {
		t.Fatalf("payload divergente: %s", got)
	}
}

func TestBalanceChangedValidation(t *testing.T) {
	wallet := uuid.New()
	base := BalanceChangedData{WalletID: wallet, TransactionID: uuid.New(), Direction: DirectionDebit, Amount: mustMoney(t, "1.00"), BalanceBefore: mustMoney(t, "10.00"), BalanceAfter: mustMoney(t, "9.00"), WalletVersion: 2}
	for _, tt := range []struct {
		name string
		mut  func(*BalanceChangedData)
		want error
	}{
		{"carteira divergente", func(d *BalanceChangedData) { d.WalletID = uuid.New() }, domain.ErrInvalidMoney},
		{"transação vazia", func(d *BalanceChangedData) { d.TransactionID = uuid.Nil }, domain.ErrUninitialized},
		{"direção inválida", func(d *BalanceChangedData) { d.Direction = "SIDEWAYS" }, domain.ErrInvalidMoney},
		{"valor inválido", func(d *BalanceChangedData) { d.Amount = money.Money{} }, domain.ErrUninitialized},
		{"saldo anterior inválido", func(d *BalanceChangedData) { d.BalanceBefore = money.Money{} }, domain.ErrUninitialized},
		{"saldo posterior inválido", func(d *BalanceChangedData) { d.BalanceAfter = money.Money{} }, domain.ErrUninitialized},
		{"versão inválida", func(d *BalanceChangedData) { d.WalletVersion = 0 }, domain.ErrInvalidMoney},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := base
			tt.mut(&data)
			if _, err := NewBalanceChanged(uuid.New(), wallet, "corr", "", fixtureTime(), data); !errors.Is(err, tt.want) {
				t.Fatalf("erro = %v, esperado %v", err, tt.want)
			}
		})
	}
}

func TestPendingReferenceValidation(t *testing.T) {
	tx := uuid.New()
	base := PendingReferenceData{TransactionID: tx, ProviderID: "provider", ExternalTxID: "refund", WalletID: uuid.New(), ReferenceExtID: "bet", NextAttemptAt: fixtureTime(), ExpiresAt: fixtureTime().Add(time.Minute)}
	for _, tt := range []struct {
		name string
		mut  func(*PendingReferenceData)
		want error
	}{
		{"transação divergente", func(d *PendingReferenceData) { d.TransactionID = uuid.New() }, domain.ErrInvalidMoney},
		{"provedor vazio", func(d *PendingReferenceData) { d.ProviderID = "" }, domain.ErrInvalidMoney},
		{"externo vazio", func(d *PendingReferenceData) { d.ExternalTxID = "" }, domain.ErrInvalidMoney},
		{"referência vazia", func(d *PendingReferenceData) { d.ReferenceExtID = "" }, domain.ErrInvalidMoney},
		{"carteira vazia", func(d *PendingReferenceData) { d.WalletID = uuid.Nil }, domain.ErrUninitialized},
		{"tentativa vazia", func(d *PendingReferenceData) { d.NextAttemptAt = time.Time{} }, domain.ErrUninitialized},
		{"prazo vazio", func(d *PendingReferenceData) { d.ExpiresAt = time.Time{} }, domain.ErrUninitialized},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := base
			tt.mut(&data)
			if _, err := NewTransactionPendingReference(uuid.New(), tx, "corr", fixtureTime(), data); !errors.Is(err, tt.want) {
				t.Fatalf("erro = %v, esperado %v", err, tt.want)
			}
		})
	}
}
