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

func providerIdent(provider string) application.Identity {
	return application.Identity{ProviderID: provider, Roles: []string{application.RoleProvider}}
}

func internalIdent() application.Identity {
	return application.Identity{Roles: []string{application.RoleInternal}}
}

func parseZero(t *testing.T) (money.Money, error) {
	t.Helper()
	return money.Zero("BRL")
}

func countBalance(t *testing.T, walletID uuid.UUID) string {
	t.Helper()
	conn := connect(t, ownerURL(t))
	var minor int64
	if err := conn.QueryRow(context.Background(), `SELECT balance_minor FROM wallets WHERE id = $1`,
		walletID.String()).Scan(&minor); err != nil {
		t.Fatalf("saldo: %v", err)
	}
	bal, _ := money.FromMinor(minor, "BRL")
	return bal.String()
}

func TestAuthorizationMatrix(t *testing.T) {
	runner := openRunner(t)
	ctx := context.Background()
	provA := providerIdent("provider-a")
	provB := providerIdent("provider-b")
	intern := internalIdent()
	anon := application.Identity{}

	// Carteira do provedor A, com saldo, para as leituras cruzadas.
	walletID, playerID := fundWallet(t, runner, "1000.00")
	betCmd := processCmd(t, walletID, playerID, wager.KindBet, "25.00")
	betRes, err := application.Execute(ctx, runner, application.SystemClock{}, application.UUIDv7Generator{}, provA, betCmd)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	// Provedor B processando como A: negado, sem efeito financeiro.
	evil := processCmd(t, walletID, playerID, wager.KindBet, "25.00")
	evil.ProviderID = "provider-a"
	if _, err := application.Execute(ctx, runner, application.SystemClock{}, application.UUIDv7Generator{}, provB, evil); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("B como A = %v", err)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM wager_transactions WHERE external_transaction_id = $1`, evil.ExternalID); n != 0 {
		t.Fatalf("operação negada persistiu: %d", n)
	}
	balance := countBalance(t, walletID)
	if balance != "975.00" {
		t.Fatalf("saldo mexeu no negado: %s", balance)
	}

	// Serviço interno não processa aposta de provedor.
	if _, err := application.Execute(ctx, runner, application.SystemClock{}, application.UUIDv7Generator{}, intern, betCmd); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("interno processando = %v", err)
	}

	// Sem identidade, nada anda.
	if _, err := application.Execute(ctx, runner, application.SystemClock{}, application.UUIDv7Generator{}, anon, betCmd); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("anônimo = %v", err)
	}

	// Provedor não abre carteira, não lê carteira, não reconcilia.
	player, _ := uuid.NewV7()
	zero, _ := parseZero(t)
	if _, err := application.OpenWallet(ctx, runner, application.SystemClock{}, application.UUIDv7Generator{}, provA,
		application.OpenWalletCommand{PlayerID: player, InitialBalance: zero, CorrelationID: "c"}); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("provedor abrindo = %v", err)
	}
	if _, err := application.GetWallet(ctx, runner, provA, walletID); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("provedor lendo carteira = %v", err)
	}
	if _, err := application.Reconcile(ctx, runner, nil, provA, walletID); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("provedor reconciliando = %v", err)
	}

	// Leitura cruzada: caminho de outro provedor nega; id direto esconde.
	if _, err := application.GetTransactionByExternal(ctx, runner, provB, "provider-a", betCmd.ExternalID); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("caminho alheio = %v", err)
	}
	if _, err := application.GetTransaction(ctx, runner, provB, betRes.TransactionID); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("id alheio = %v", err)
	}

	// Dono lê pelos dois caminhos; interno lê tudo.
	if _, err := application.GetTransaction(ctx, runner, provA, betRes.TransactionID); err != nil {
		t.Fatalf("dono = %v", err)
	}
	if _, err := application.GetTransaction(ctx, runner, intern, betRes.TransactionID); err != nil {
		t.Fatalf("interno = %v", err)
	}
}
