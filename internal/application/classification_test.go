package application

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wager"
)

func TestAsTypeAndMapInsertError(t *testing.T) {
	cause := errors.New("restrição no banco")
	for _, tt := range []struct {
		name       string
		constraint string
		want       error
	}{
		{"id externo", "wt_provider_external_uk", ErrExternalIDReused},
		{"carteira ausente", "wager_transactions_wallet_id_fkey", ErrNotFound},
		{"outra restrição", "wt_provider_idemkey_uk", ErrExternalIDReused},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wrapped := fmt.Errorf("inserindo: %w", &ConflictError{Constraint: tt.constraint, Err: cause})
			conflict, ok := AsType[*ConflictError](wrapped)
			if !ok || conflict.Constraint != tt.constraint || !errors.Is(conflict, cause) {
				t.Fatalf("conflito extraído = %+v, ok = %v", conflict, ok)
			}
			if got := mapInsertError(wrapped); !errors.Is(got, tt.want) {
				t.Fatalf("classificação = %v, esperado %v", got, tt.want)
			}
		})
	}
	if conflict, ok := AsType[*ConflictError](cause); ok || conflict != nil {
		t.Fatalf("erro comum classificado: %+v", conflict)
	}
	if got := mapInsertError(cause); got != cause {
		t.Fatalf("erro comum alterado: %v", got)
	}
}

func TestAuthorization(t *testing.T) {
	for _, tt := range []struct {
		name     string
		identity Identity
		provider string
		internal bool
		allowed  bool
	}{
		{"provedor certo", Identity{ProviderID: "a", Roles: []string{RoleProvider}}, "a", false, true},
		{"provedor diferente", Identity{ProviderID: "a", Roles: []string{RoleProvider}}, "b", false, false},
		{"provedor vazio", Identity{Roles: []string{RoleProvider}}, "a", false, false},
		{"papel ausente", Identity{ProviderID: "a"}, "a", false, false},
		{"interno", Identity{Roles: []string{RoleInternal}}, "", true, true},
		{"externo não é interno", Identity{Roles: []string{RoleProvider}}, "", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			if tt.internal {
				err = requireInternal(tt.identity)
			} else {
				err = requireProvider(tt.identity, tt.provider)
			}
			if (err == nil) != tt.allowed {
				t.Fatalf("autorização = %v, permitida = %v", err, tt.allowed)
			}
			if !tt.allowed && !errors.Is(err, ErrForbidden) {
				t.Fatalf("erro = %v, esperado acesso negado", err)
			}
		})
	}
}

func TestCheckCommand(t *testing.T) {
	amount, _ := money.Parse("10.00", "BRL")
	base := ProcessCommand{ProviderID: "provider", ExternalID: "bet", IdempotencyKey: "key", PlayerID: uuid.New(), WalletID: uuid.New(), RoundID: "round", GameID: "game", Kind: wager.KindBet, Amount: amount, CorrelationID: "corr"}
	for _, tt := range []struct {
		name string
		mut  func(*ProcessCommand)
	}{
		{"provedor vazio", func(c *ProcessCommand) { c.ProviderID = "" }},
		{"externo vazio", func(c *ProcessCommand) { c.ExternalID = "" }},
		{"chave vazia", func(c *ProcessCommand) { c.IdempotencyKey = "" }},
		{"rodada vazia", func(c *ProcessCommand) { c.RoundID = "" }},
		{"jogo vazio", func(c *ProcessCommand) { c.GameID = "" }},
		{"correlação vazia", func(c *ProcessCommand) { c.CorrelationID = "" }},
		{"jogador vazio", func(c *ProcessCommand) { c.PlayerID = uuid.Nil }},
		{"carteira vazia", func(c *ProcessCommand) { c.WalletID = uuid.Nil }},
		{"valor inválido", func(c *ProcessCommand) { c.Amount = money.Money{} }},
		{"tipo inválido", func(c *ProcessCommand) { c.Kind = "UNKNOWN" }},
		{"aposta referenciada", func(c *ProcessCommand) { c.ReferenceExternalID = "bet-old" }},
		{"perda referenciada", func(c *ProcessCommand) { c.Kind = wager.KindLoss; c.ReferenceExternalID = "bet-old" }},
		{"estorno sem referência", func(c *ProcessCommand) { c.Kind = wager.KindRefund }},
		{"reversão sem referência", func(c *ProcessCommand) { c.Kind = wager.KindRollback }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd := base
			tt.mut(&cmd)
			if err := checkCommand(cmd, time.Now()); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("erro = %v, esperado entrada inválida", err)
			}
		})
	}
	if err := checkCommand(base, time.Time{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("relógio vazio: %v", err)
	}
	for _, kind := range []wager.Kind{wager.KindBet, wager.KindWin, wager.KindLoss, wager.KindRefund, wager.KindRollback} {
		cmd := base
		cmd.Kind = kind
		if kind == wager.KindRefund || kind == wager.KindRollback {
			cmd.ReferenceExternalID = "bet-old"
		}
		if err := checkCommand(cmd, time.Now()); err != nil {
			t.Fatalf("tipo %s rejeitado: %v", kind, err)
		}
	}
}
