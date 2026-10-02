//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wager"
)

func TestGetWallet(t *testing.T) {
	runner := openRunner(t)
	ctx := context.Background()
	walletID, playerID := fundWallet(t, runner, "1000.00")

	view, err := application.GetWallet(ctx, runner, internalIdent(), walletID)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if view.Balance.String() != "1000.00" || view.Version != 1 ||
		view.PlayerID != playerID || !view.Opened {
		t.Fatalf("carteira = %+v", view)
	}
	ghost, _ := uuid.NewV7()
	if _, err := application.GetWallet(ctx, runner, internalIdent(), ghost); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("fantasma erro = %v", err)
	}
}

func TestListLedgerPages(t *testing.T) {
	runner := openRunner(t)
	ctx := context.Background()
	walletID, playerID := fundWallet(t, runner, "1000.00")

	// 120 apostas de verdade: 1 de abertura + 120 no ledger.
	for range 120 {
		runProcess(t, runner, processCmd(t, walletID, playerID, wager.KindBet, "1.00"))
	}

	seen := map[string]bool{}
	count := 0
	var firstBefore, lastAfter money.Money
	cursor := ""
	for {
		page, err := application.ListLedger(ctx, runner, internalIdent(), walletID, cursor, 50)
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		for _, e := range page.Entries {
			id := e.ID.String()
			if seen[id] {
				t.Fatalf("lançamento duplicado: %s", id)
			}
			seen[id] = true
			// Ordem estável e conta certa em cada linha: depois = antes ± valor.
			var want money.Money
			if e.Direction == "DEBIT" {
				want, _ = e.BalanceBefore.Sub(e.Amount)
			} else {
				want, _ = e.BalanceBefore.Add(e.Amount)
			}
			if cmp, _ := e.BalanceAfter.Cmp(want); cmp != 0 {
				t.Fatalf("posição %d: depois=%s esperado=%s", count, e.BalanceAfter, want)
			}
			if count == 0 {
				firstBefore = e.BalanceBefore
			}
			lastAfter = e.BalanceAfter
			count++
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			t.Fatal("cursor não andou")
		}
		cursor = page.NextCursor
	}
	if count != 121 {
		t.Fatalf("lançamentos = %d, esperado 121", count)
	}
	if firstBefore.String() != "0.00" || lastAfter.String() != "880.00" {
		t.Fatalf("pontas = %s..%s", firstBefore, lastAfter)
	}

	// Teto: pedido gigante volta no máximo 100, com cursor para seguir.
	full, err := application.ListLedger(ctx, runner, internalIdent(), walletID, "", 10000)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if len(full.Entries) != 100 || full.NextCursor == "" {
		t.Fatalf("teto = %d entradas, cursor %q", len(full.Entries), full.NextCursor)
	}
}

func TestListLedgerCursorAndLimit(t *testing.T) {
	runner := openRunner(t)
	ctx := context.Background()
	walletID, _ := fundWallet(t, runner, "1000.00")

	// Cursor inválido é entrada inválida (o HTTP vira 400).
	for _, bad := range []string{"!!!", "aGk=", "bm90LWpzb24=", "eyJzZXEiOi0xfQ=="} {
		if _, err := application.ListLedger(ctx, runner, internalIdent(), walletID, bad, 50); !errors.Is(err, application.ErrInvalidInput) {
			t.Fatalf("cursor %q erro = %v", bad, err)
		}
	}
	ghost, _ := uuid.NewV7()
	if _, err := application.ListLedger(ctx, runner, internalIdent(), ghost, "", 50); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("fantasma erro = %v", err)
	}
}
