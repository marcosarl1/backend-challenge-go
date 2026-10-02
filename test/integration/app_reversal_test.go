package integration

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wager"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/postgres"
)

func processRefCmd(t *testing.T, walletID, playerID uuid.UUID, kind wager.Kind, amount, refExt string) application.ProcessCommand {
	t.Helper()
	uniq := uuid.NewString()
	amt, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	return application.ProcessCommand{
		ProviderID: "provider-a", ExternalID: "ext-" + uniq,
		IdempotencyKey: "k-" + uniq, PlayerID: playerID, WalletID: walletID,
		RoundID: "round-1", GameID: "jogo", Kind: kind,
		Amount: amt, ReferenceExternalID: refExt, CorrelationID: "corr-" + uniq,
	}
}

// buildPendingBet forja uma aposta ainda pendente (conclusão assíncrona em
// andamento em outro caminho).
func buildPendingBet(playerID, walletID uuid.UUID, externalID string) (*wager.WagerTransaction, error) {
	id, _ := uuid.NewV7()
	amt, _ := money.Parse("40.00", "BRL")
	return wager.NewExternal(wager.ExternalParams{
		ID: id, ProviderID: "provider-a", ExternalID: externalID,
		IdempotencyKey: "k-" + externalID, PayloadHash: []byte("hash............................"),
		WalletID: walletID, PlayerID: playerID, RoundID: "round-1", GameID: "jogo",
		Kind: wager.KindBet, Amount: amt,
	}, application.SystemClock{}.Now())
}

// cleanPendingTx remove esperas de outras execuções (só pendentes, sem ledger nem referência resolvida: nada as amarra).
func cleanPendingTx(t *testing.T) {
	t.Helper()
	conn := connect(t, ownerURL(t))
	if _, err := conn.Exec(context.Background(), `DELETE FROM wager_transactions
		WHERE status IN ('PENDING', 'PENDING_REFERENCE')`); err != nil {
		t.Fatalf("limpando pendências: %v", err)
	}
}

func retryPending(t *testing.T, runner postgres.Runner) int {
	t.Helper()
	n, err := application.RetryPending(context.Background(), runner, application.SystemClock{}, application.UUIDv7Generator{}, 10)
	if err != nil {
		t.Fatalf("retomando: %v", err)
	}
	return n
}

// makeDue adianta o vencimento (simula o tempo passando até o worker acordar).
func makeDue(t *testing.T, txID uuid.UUID) {
	t.Helper()
	conn := connect(t, ownerURL(t))
	if _, err := conn.Exec(context.Background(), `UPDATE wager_transactions
		SET next_attempt_at = now() - interval '1 second' WHERE id = $1`, txID.String()); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
}

// makeDueExt adianta por id externo.
func makeDueExt(t *testing.T, externalID string) {
	t.Helper()
	conn := connect(t, ownerURL(t))
	if _, err := conn.Exec(context.Background(), `UPDATE wager_transactions
		SET next_attempt_at = now() - interval '1 second' WHERE external_transaction_id = $1`, externalID); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
}

func TestRefundBeforeBetResolvesLater(t *testing.T) {
	cleanPendingTx(t)
	refUniq := uuid.NewString()
	runner := openRunner(t)
	walletID, playerID := fundWallet(t, runner, "1000.00")

	// O reembolso chega antes da aposta: espera com evento de pendência.
	refund := runProcess(t, runner, processRefCmd(t, walletID, playerID, wager.KindRefund, "40.00", "bet-futura-"+refUniq))
	if refund.Status != wager.StatusPendingReference {
		t.Fatalf("estado = %s", refund.Status)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE aggregate_id = $1 AND event_type = 'WagerTransactionPendingReference'`,
		refund.TransactionID.String()); n != 1 {
		t.Fatalf("eventos de espera = %d", n)
	}

	// A aposta chega (com outro id, mesma rodada e valor).
	bet := processCmd(t, walletID, playerID, wager.KindBet, "40.00")
	bet.ExternalID = "bet-futura-" + refUniq
	bet.IdempotencyKey = "k-bet-futura-" + refUniq
	betRes := runProcess(t, runner, bet)
	if betRes.Status != wager.StatusProcessed {
		t.Fatalf("aposta = %+v", betRes)
	}

	// A retomada conclui o reembolso e devolve o crédito.
	makeDue(t, refund.TransactionID)
	if n := retryPending(t, runner); n < 1 {
		t.Fatalf("retomadas = %d", n)
	}
	ctx := context.Background()
	var status, balance string
	conn := connect(t, ownerURL(t))
	if err := conn.QueryRow(ctx, `SELECT t.status, w.balance_minor FROM wager_transactions t
		JOIN wallets w ON w.id = t.wallet_id WHERE t.id = $1`,
		refund.TransactionID.String()).Scan(&status, &balance); err != nil {
		t.Fatalf("lendo: %v", err)
	}
	if status != string(wager.StatusProcessed) || balance != "100000" {
		t.Fatalf("estado=%s saldo=%s", status, balance)
	}
}

func TestRefundAgainstPendingReference(t *testing.T) {
	cleanPendingTx(t)
	refUniq := uuid.NewString()
	runner := openRunner(t)
	ctx := context.Background()
	walletID, playerID := fundWallet(t, runner, "1000.00")

	// Aposta gravada ainda pendente (conclusão assíncrona em andamento).
	betTx, err := buildPendingBet(playerID, walletID, "bet-pendente-"+refUniq)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if err := runner.Do(ctx, func(ctx context.Context, r application.Repositories) error {
		_, err := r.Wagers.Insert(ctx, betTx, "corr-pend")
		return err
	}); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	refund := runProcess(t, runner, processRefCmd(t, walletID, playerID, wager.KindRefund, "40.00", "bet-pendente-"+refUniq))
	if refund.Status != wager.StatusPendingReference {
		t.Fatalf("estado = %s", refund.Status)
	}

	// A aposta conclui por outro caminho; a retomada aplica o reembolso.
	if err := runner.Do(ctx, func(ctx context.Context, r application.Repositories) error {
		found, err := r.Wagers.FindByProviderExternal(ctx, "provider-a", "bet-pendente-"+refUniq)
		if err != nil {
			return err
		}
		bal, _ := money.Parse("960.00", "BRL")
		if err := found.MarkProcessed(bal, 2, application.SystemClock{}.Now()); err != nil {
			return err
		}
		return r.Wagers.Save(ctx, found)
	}); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	makeDue(t, refund.TransactionID)
	if n := retryPending(t, runner); n < 1 {
		t.Fatalf("retomadas = %d", n)
	}
	var status string
	conn := connect(t, ownerURL(t))
	if err := conn.QueryRow(ctx, `SELECT status FROM wager_transactions WHERE id = $1`,
		refund.TransactionID.String()).Scan(&status); err != nil || status != string(wager.StatusProcessed) {
		t.Fatalf("estado=%s, %v", status, err)
	}
}

func TestRefundAgainstRejectedReference(t *testing.T) {
	refUniq := uuid.NewString()
	runner := openRunner(t)
	walletID, playerID := fundWallet(t, runner, "20.00")

	// Aposta sem saldo: rejeitada definitiva.
	bet := processCmd(t, walletID, playerID, wager.KindBet, "80.00")
	bet.ExternalID = "bet-sem-saldo-" + refUniq
	bet.IdempotencyKey = "k-bet-sem-saldo-" + refUniq
	betRes := runProcess(t, runner, bet)
	if betRes.Status != wager.StatusRejected {
		t.Fatalf("aposta = %+v", betRes)
	}

	refund := runProcess(t, runner, processRefCmd(t, walletID, playerID, wager.KindRefund, "80.00", "bet-sem-saldo-"+refUniq))
	if refund.Status != wager.StatusRejected || refund.FailureCode != wager.CodeReferenceNotProcessed {
		t.Fatalf("reembolso = %+v", refund)
	}
}

func TestDoubleReversalFails(t *testing.T) {
	refUniq := uuid.NewString()
	runner := openRunner(t)
	walletID, playerID := fundWallet(t, runner, "1000.00")

	bet := processCmd(t, walletID, playerID, wager.KindBet, "40.00")
	bet.ExternalID = "bet-alvo-" + refUniq
	bet.IdempotencyKey = "k-bet-alvo-" + refUniq
	runProcess(t, runner, bet)

	first := runProcess(t, runner, processRefCmd(t, walletID, playerID, wager.KindRefund, "40.00", "bet-alvo-"+refUniq))
	if first.Status != wager.StatusProcessed {
		t.Fatalf("primeiro = %+v", first)
	}
	// Segunda reversão do mesmo tipo...
	second := runProcess(t, runner, processRefCmd(t, walletID, playerID, wager.KindRefund, "40.00", "bet-alvo-"+refUniq))
	if second.Status != wager.StatusRejected || second.FailureCode != wager.CodeAlreadyReversed {
		t.Fatalf("segundo = %+v", second)
	}
	// ...e do outro tipo sobre a mesma aposta também.
	third := runProcess(t, runner, processRefCmd(t, walletID, playerID, wager.KindRollback, "40.00", "bet-alvo-"+refUniq))
	if third.Status != wager.StatusRejected || third.FailureCode != wager.CodeAlreadyReversed {
		t.Fatalf("estorno = %+v", third)
	}
}

func TestRollbackWinWithoutFunds(t *testing.T) {
	refUniq := uuid.NewString()
	runner := openRunner(t)
	walletID, playerID := fundWallet(t, runner, "100.00")

	bet := processCmd(t, walletID, playerID, wager.KindBet, "100.00")
	bet.ExternalID = "bet-tudo-" + refUniq
	bet.IdempotencyKey = "k-bet-tudo-" + refUniq
	runProcess(t, runner, bet)

	win := processCmd(t, walletID, playerID, wager.KindWin, "30.00")
	win.ExternalID = "win-30-" + refUniq
	win.IdempotencyKey = "k-win-30-" + refUniq
	runProcess(t, runner, win)

	spent := processCmd(t, walletID, playerID, wager.KindBet, "30.00")
	runProcess(t, runner, spent)

	// Estornar o prêmio debitaria 30.00 com saldo zero: código próprio.
	rb := runProcess(t, runner, processRefCmd(t, walletID, playerID, wager.KindRollback, "30.00", "win-30-"+refUniq))
	if rb.Status != wager.StatusRejected || rb.FailureCode != wager.CodeReversalInsufficientFunds {
		t.Fatalf("estorno = %+v", rb)
	}
}

func TestPendingExpires(t *testing.T) {
	cleanPendingTx(t)
	refUniq := uuid.NewString()
	runner := openRunner(t)
	ctx := context.Background()
	walletID, playerID := fundWallet(t, runner, "1000.00")

	refund := runProcess(t, runner, processRefCmd(t, walletID, playerID, wager.KindRefund, "40.00", "bet-nunca-"+refUniq))
	if refund.Status != wager.StatusPendingReference {
		t.Fatalf("estado = %s", refund.Status)
	}
	// O prazo passa sem a referência chegar.
	conn := connect(t, ownerURL(t))
	if _, err := conn.Exec(ctx, `UPDATE wager_transactions
		SET next_attempt_at = now() - interval '1 hour', expires_at = now() - interval '1 minute'
		WHERE id = $1`, refund.TransactionID.String()); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if n := retryPending(t, runner); n < 1 {
		t.Fatalf("retomadas = %d", n)
	}
	var status, code string
	if err := conn.QueryRow(ctx, `SELECT status, failure_code FROM wager_transactions WHERE id = $1`,
		refund.TransactionID.String()).Scan(&status, &code); err != nil {
		t.Fatalf("lendo: %v", err)
	}
	if status != string(wager.StatusRejected) || code != string(wager.CodeReferenceNotFound) {
		t.Fatalf("estado=%s código=%s", status, code)
	}
}

func TestWinWithReferenceBeforeBet(t *testing.T) {
	cleanPendingTx(t)
	refUniq := uuid.NewString()
	runner := openRunner(t)
	walletID, playerID := fundWallet(t, runner, "1000.00")

	win := runProcess(t, runner, processRefCmd(t, walletID, playerID, wager.KindWin, "25.00", "bet-win-"+refUniq))
	if win.Status != wager.StatusPendingReference {
		t.Fatalf("estado = %s", win.Status)
	}
	bet := processCmd(t, walletID, playerID, wager.KindBet, "25.00")
	bet.ExternalID = "bet-win-" + refUniq
	bet.IdempotencyKey = "k-bet-win-" + refUniq
	runProcess(t, runner, bet)

	makeDue(t, win.TransactionID)
	if n := retryPending(t, runner); n < 1 {
		t.Fatalf("retomadas = %d", n)
	}
	var status, balance string
	conn := connect(t, ownerURL(t))
	if err := conn.QueryRow(context.Background(), `SELECT t.status, w.balance_minor FROM wager_transactions t
		JOIN wallets w ON w.id = t.wallet_id WHERE t.id = $1`,
		win.TransactionID.String()).Scan(&status, &balance); err != nil {
		t.Fatalf("lendo: %v", err)
	}
	if status != string(wager.StatusProcessed) || balance != "100000" {
		t.Fatalf("estado=%s saldo=%s", status, balance)
	}
}
