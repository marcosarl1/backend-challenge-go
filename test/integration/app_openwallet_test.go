package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/postgres"
)

func openRunner(t *testing.T) postgres.Runner {
	t.Helper()
	url := ownerURL(t)
	ctx := context.Background()
	pool, err := postgres.Connect(ctx, url)
	if err != nil {
		t.Fatalf("conectando: %v", err)
	}
	t.Cleanup(pool.Close)
	return postgres.NewRunner(postgres.NewUnitOfWork(pool))
}

func openCmd(player uuid.UUID, balance money.Money) application.OpenWalletCommand {
	return application.OpenWalletCommand{
		PlayerID:       player,
		InitialBalance: balance,
		CorrelationID:  "corr-" + player.String(),
	}
}

// checkOpenWalletRows confere o que o commit deixou: sempre a carteira; com
// movimento, a abertura processada, o lançamento de crédito e os 2 eventos.
func checkOpenWalletRows(t *testing.T, corr string, walletID uuid.UUID, withMovement bool) {
	t.Helper()
	conn := connect(t, ownerURL(t))
	ctx := context.Background()
	var wallets, txs, ledger, outbox int
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM wallets WHERE id = $1`,
		walletID.String()).Scan(&wallets); err != nil {
		t.Fatalf("carteiras: %v", err)
	}
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM wager_transactions WHERE wallet_id = $1`,
		walletID.String()).Scan(&txs); err != nil {
		t.Fatalf("transações: %v", err)
	}
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE wallet_id = $1`,
		walletID.String()).Scan(&ledger); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM outbox_events WHERE correlation_id = $1`,
		corr).Scan(&outbox); err != nil {
		t.Fatalf("outbox: %v", err)
	}
	wantTx, wantLedger, wantOutbox := 0, 0, 0
	if withMovement {
		wantTx, wantLedger, wantOutbox = 1, 1, 2
	}
	if wallets != 1 || txs != wantTx || ledger != wantLedger || outbox != wantOutbox {
		t.Fatalf("linhas: carteiras=%d transações=%d ledger=%d outbox=%d",
			wallets, txs, ledger, outbox)
	}
	if !withMovement {
		return
	}
	var status, direction string
	if err := conn.QueryRow(ctx, `SELECT status FROM wager_transactions WHERE wallet_id = $1`,
		walletID.String()).Scan(&status); err != nil || status != "PROCESSED" {
		t.Fatalf("abertura = %q, %v", status, err)
	}
	if err := conn.QueryRow(ctx, `SELECT direction FROM wallet_ledger_entries WHERE wallet_id = $1`,
		walletID.String()).Scan(&direction); err != nil || direction != "CREDIT" {
		t.Fatalf("lançamento = %q, %v", direction, err)
	}
	var types []string
	rows, err := conn.Query(ctx, `SELECT event_type FROM outbox_events WHERE correlation_id = $1 ORDER BY event_type`, corr)
	if err != nil {
		t.Fatalf("eventos: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			t.Fatalf("evento: %v", err)
		}
		types = append(types, typ)
	}
	if len(types) != 2 || types[0] != "WagerTransactionProcessed" || types[1] != "WalletBalanceChanged" {
		t.Fatalf("eventos = %v", types)
	}
}

func TestOpenWalletWithMovement(t *testing.T) {
	runner := openRunner(t)
	ctx := context.Background()
	player, _ := uuid.NewV7()
	cmd := openCmd(player, mustParseMoney(t, "1000.00"))

	res, err := application.OpenWallet(ctx, runner, application.SystemClock{}, application.UUIDv7Generator{}, internalIdent(), cmd)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if !res.Opened || res.Balance.String() != "1000.00" || res.Version != 1 {
		t.Fatalf("resultado = %+v", res)
	}
	checkOpenWalletRows(t, cmd.CorrelationID, res.WalletID, true)
}

func TestOpenWalletZero(t *testing.T) {
	runner := openRunner(t)
	ctx := context.Background()
	player, _ := uuid.NewV7()
	zero, err := money.Zero("BRL")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	cmd := openCmd(player, zero)

	res, err := application.OpenWallet(ctx, runner, application.SystemClock{}, application.UUIDv7Generator{}, internalIdent(), cmd)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if res.Opened {
		t.Fatal("zero não abre movimento")
	}
	checkOpenWalletRows(t, cmd.CorrelationID, res.WalletID, false)
}

func TestOpenWalletDuplicate(t *testing.T) {
	runner := openRunner(t)
	ctx := context.Background()
	player, _ := uuid.NewV7()
	cmd := openCmd(player, mustParseMoney(t, "10.00"))
	if _, err := application.OpenWallet(ctx, runner, application.SystemClock{}, application.UUIDv7Generator{}, internalIdent(), cmd); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	_, err := application.OpenWallet(ctx, runner, application.SystemClock{}, application.UUIDv7Generator{}, internalIdent(), cmd)
	if !errors.Is(err, application.ErrWalletExists) {
		t.Fatalf("duplicata erro = %v", err)
	}
}
