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
	"github.com/marcosarl1/backend-challenge-go/internal/infra/postgres"
)

// fundWallet abre uma carteira com saldo e devolve seus identificadores.
func fundWallet(t *testing.T, runner postgres.Runner, balance string) (walletID, playerID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	playerID, _ = uuid.NewV7()
	res, err := application.OpenWallet(ctx, runner, application.SystemClock{}, application.UUIDv7Generator{}, internalIdent(),
		application.OpenWalletCommand{
			PlayerID:       playerID,
			InitialBalance: mustParseMoney(t, balance),
			CorrelationID:  "corr-fund-" + playerID.String(),
		})
	if err != nil {
		t.Fatalf("financiando: %v", err)
	}
	return res.WalletID, playerID
}

func processCmd(t *testing.T, walletID, playerID uuid.UUID, kind wager.Kind, amount string) application.ProcessCommand {
	t.Helper()
	uniq := uuid.NewString()
	amt, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if kind == "LOSS" {
		amt, _ = money.Zero("BRL")
	}
	return application.ProcessCommand{
		ProviderID: "provider-a", ExternalID: "ext-" + uniq,
		IdempotencyKey: "k-" + uniq, PlayerID: playerID, WalletID: walletID,
		RoundID: "round-1", GameID: "jogo", Kind: kind,
		Amount: amt, CorrelationID: "corr-" + uniq,
	}
}

func runProcess(t *testing.T, runner postgres.Runner, cmd application.ProcessCommand) *application.ProcessResult {
	t.Helper()
	res, err := application.Execute(context.Background(), runner, application.SystemClock{}, application.UUIDv7Generator{}, providerIdent("provider-a"), cmd)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	return res
}

func countRows(t *testing.T, query string, args ...any) int {
	t.Helper()
	conn := connect(t, ownerURL(t))
	var n int
	if err := conn.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("contando: %v", err)
	}
	return n
}

func TestProcessBet(t *testing.T) {
	runner := openRunner(t)
	walletID, playerID := fundWallet(t, runner, "1000.00")
	cmd := processCmd(t, walletID, playerID, wager.KindBet, "25.00")

	res := runProcess(t, runner, cmd)
	if res.Status != wager.StatusProcessed || res.Balance.String() != "975.00" ||
		res.WalletVersion != 2 || res.IdempotentReplay {
		t.Fatalf("resultado = %+v", res)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND transaction_id = $2`,
		walletID.String(), res.TransactionID.String()); n != 1 {
		t.Fatalf("lançamentos = %d", n)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE correlation_id = $1`, cmd.CorrelationID); n != 2 {
		t.Fatalf("eventos = %d", n)
	}
}

func TestProcessReplayKeepsOriginalBalance(t *testing.T) {
	runner := openRunner(t)
	walletID, playerID := fundWallet(t, runner, "1000.00")
	first := processCmd(t, walletID, playerID, wager.KindBet, "25.00")
	res1 := runProcess(t, runner, first)

	second := processCmd(t, walletID, playerID, wager.KindBet, "10.00")
	runProcess(t, runner, second)

	replay := runProcess(t, runner, first)
	if !replay.IdempotentReplay || replay.TransactionID != res1.TransactionID {
		t.Fatalf("replay = %+v", replay)
	}
	if replay.Balance.String() != "975.00" {
		t.Fatalf("replay devolveu %s, esperado o saldo original 975.00", replay.Balance)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, walletID.String()); n != 3 {
		t.Fatalf("lançamentos = %d, esperado 3 (abertura + 2 apostas)", n)
	}
}

func TestProcessIdempotencyMismatch(t *testing.T) {
	runner := openRunner(t)
	walletID, playerID := fundWallet(t, runner, "1000.00")
	cmd := processCmd(t, walletID, playerID, wager.KindBet, "25.00")
	runProcess(t, runner, cmd)

	cmd.Amount = mustParseMoney(t, "30.00")
	_, err := application.Execute(context.Background(), runner, application.SystemClock{}, application.UUIDv7Generator{}, providerIdent("provider-a"), cmd)
	if !errors.Is(err, application.ErrIdempotencyMismatch) {
		t.Fatalf("erro = %v", err)
	}
}

func TestProcessExternalIDReuse(t *testing.T) {
	runner := openRunner(t)
	walletID, playerID := fundWallet(t, runner, "1000.00")
	cmd := processCmd(t, walletID, playerID, wager.KindBet, "25.00")
	runProcess(t, runner, cmd)

	cmd.IdempotencyKey = "k-outra-" + uuid.NewString()
	_, err := application.Execute(context.Background(), runner, application.SystemClock{}, application.UUIDv7Generator{}, providerIdent("provider-a"), cmd)
	if !errors.Is(err, application.ErrExternalIDReused) {
		t.Fatalf("erro = %v", err)
	}
}

func TestProcessWinAndLoss(t *testing.T) {
	runner := openRunner(t)
	walletID, playerID := fundWallet(t, runner, "1000.00")

	win := runProcess(t, runner, processCmd(t, walletID, playerID, wager.KindWin, "50.00"))
	if win.Status != wager.StatusProcessed || win.Balance.String() != "1050.00" || win.WalletVersion != 2 {
		t.Fatalf("prêmio = %+v", win)
	}

	loss := runProcess(t, runner, processCmd(t, walletID, playerID, wager.KindLoss, "0.00"))
	if loss.Status != wager.StatusProcessed || loss.Balance.String() != "1050.00" || loss.WalletVersion != 2 {
		t.Fatalf("derrota = %+v", loss)
	}
	// Derrota não mexe no ledger nem na versão, mas gera o evento de processada.
	if n := countRows(t, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE transaction_id = $1`,
		loss.TransactionID.String()); n != 0 {
		t.Fatalf("lançamentos da derrota = %d", n)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE aggregate_id = $1`,
		loss.TransactionID.String()); n != 1 {
		t.Fatalf("eventos da derrota = %d", n)
	}
}

func TestProcessInsufficientRejects(t *testing.T) {
	runner := openRunner(t)
	walletID, playerID := fundWallet(t, runner, "20.00")
	cmd := processCmd(t, walletID, playerID, wager.KindBet, "80.00")

	res := runProcess(t, runner, cmd)
	if res.Status != wager.StatusRejected || res.FailureCode != wager.CodeInsufficientFunds {
		t.Fatalf("resultado = %+v", res)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE transaction_id = $1`,
		res.TransactionID.String()); n != 0 {
		t.Fatalf("lançamentos da rejeitada = %d", n)
	}
	// Repetir devolve a rejeição com o código, sem reexecutar.
	replay := runProcess(t, runner, cmd)
	if !replay.IdempotentReplay || replay.FailureCode != wager.CodeInsufficientFunds {
		t.Fatalf("replay = %+v", replay)
	}
}

func TestProcessWalletNotFound(t *testing.T) {
	runner := openRunner(t)
	_, playerID := fundWallet(t, runner, "1000.00")
	ghost, _ := uuid.NewV7()
	cmd := processCmd(t, ghost, playerID, wager.KindBet, "10.00")

	_, err := application.Execute(context.Background(), runner, application.SystemClock{}, application.UUIDv7Generator{}, providerIdent("provider-a"), cmd)
	if !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("erro = %v", err)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM wager_transactions WHERE external_transaction_id = $1`, cmd.ExternalID); n != 0 {
		t.Fatalf("transação fantasma persistida: %d", n)
	}
}

func TestProcessMismatchRejects(t *testing.T) {
	runner := openRunner(t)
	walletID, playerID := fundWallet(t, runner, "1000.00")

	other, _ := uuid.NewV7()
	cmd := processCmd(t, walletID, other, wager.KindBet, "10.00")
	res := runProcess(t, runner, cmd)
	if res.Status != wager.StatusRejected || res.FailureCode != wager.CodeWalletMismatch {
		t.Fatalf("jogador = %+v", res)
	}

	usd, _ := money.Parse("10.00", "USD")
	cmd2 := processCmd(t, walletID, playerID, wager.KindBet, "10.00")
	cmd2.Amount = usd
	res2 := runProcess(t, runner, cmd2)
	if res2.Status != wager.StatusRejected || res2.FailureCode != wager.CodeCurrencyMismatch {
		t.Fatalf("moeda = %+v", res2)
	}
}

func TestExecuteInTxWithInbox(t *testing.T) {
	// O formato que o SQS usa: inbox + caso de uso na mesma transação.
	runner := openRunner(t)
	walletID, playerID := fundWallet(t, runner, "1000.00")
	cmd := processCmd(t, walletID, playerID, wager.KindBet, "15.00")
	now := application.SystemClock{}.Now()

	var res *application.ProcessResult
	err := runner.Do(context.Background(), func(ctx context.Context, r application.Repositories) error {
		ok, err := r.Inbox.Insert(ctx, "test-consumer", "msg-"+cmd.ExternalID, []byte("h"), now)
		if err != nil || !ok {
			return err
		}
		var err2 error
		res, err2 = application.ExecuteInTx(ctx, r, application.SystemClock{}, application.UUIDv7Generator{}, cmd)
		return err2
	})
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if res.Status != wager.StatusProcessed || res.Balance.String() != "985.00" {
		t.Fatalf("resultado = %+v", res)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM inbox_messages WHERE message_id = $1`, "msg-"+cmd.ExternalID); n != 1 {
		t.Fatalf("inbox = %d", n)
	}
}
