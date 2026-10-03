//go:build integration

package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wager"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/postgres"
)

type concurrentOutcome struct {
	result *application.ProcessResult
	err    error
}

// executeTogether solta os comandos no mesmo instante e espera todos terminarem antes de ler os resultados.
func executeTogether(t *testing.T, runner postgres.Runner, cmds []application.ProcessCommand) []concurrentOutcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	start := make(chan struct{})
	outcomes := make([]concurrentOutcome, len(cmds))
	var wg sync.WaitGroup
	wg.Add(len(cmds))
	for i := range cmds {
		go func(i int) {
			defer wg.Done()
			<-start
			outcomes[i].result, outcomes[i].err = application.Execute(ctx, runner,
				application.SystemClock{}, application.UUIDv7Generator{}, providerIdent("provider-a"), cmds[i])
		}(i)
	}
	close(start)
	wg.Wait()
	return outcomes
}

// TestConcurrentSameBet prova que cinquenta entregas simultâneas produzem uma operação e um débito.
func TestConcurrentSameBet(t *testing.T) {
	runner := openRunner(t)
	walletID, playerID := fundWallet(t, runner, "100.00")
	cmd := processCmd(t, walletID, playerID, wager.KindBet, "10.00")
	cmds := make([]application.ProcessCommand, 50)
	for i := range cmds {
		cmds[i] = cmd
	}
	outcomes := executeTogether(t, runner, cmds)
	var transactionID uuid.UUID
	newCount, replayCount := 0, 0
	for i, outcome := range outcomes {
		if outcome.err != nil || outcome.result == nil {
			t.Fatalf("execução %d: resultado %+v, erro %v", i, outcome.result, outcome.err)
		}
		result := outcome.result
		if result.Status != wager.StatusProcessed || result.Balance.String() != "90.00" || result.WalletVersion != 2 {
			t.Fatalf("execução %d: %+v", i, result)
		}
		if transactionID == uuid.Nil {
			transactionID = result.TransactionID
		} else if result.TransactionID != transactionID {
			t.Fatalf("execução %d gerou outra transação: %s", i, result.TransactionID)
		}
		if result.IdempotentReplay {
			replayCount++
		} else {
			newCount++
		}
	}
	if newCount != 1 || replayCount != 49 {
		t.Fatalf("novas=%d, replays=%d", newCount, replayCount)
	}
	if balance := countBalance(t, walletID); balance != "90.00" {
		t.Fatalf("saldo = %s", balance)
	}
	if count := countRows(t, `SELECT COUNT(*) FROM wager_transactions WHERE provider_id = $1 AND idempotency_key = $2`, cmd.ProviderID, cmd.IdempotencyKey); count != 1 {
		t.Fatalf("operações = %d", count)
	}
	if count := countRows(t, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE transaction_id = $1`, transactionID); count != 1 {
		t.Fatalf("débitos = %d", count)
	}
	if count := countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE correlation_id = $1`, cmd.CorrelationID); count != 2 {
		t.Fatalf("eventos = %d", count)
	}
}

// TestConcurrentInsufficientFunds prova que duas apostas de 80 sobre 100 não produzem saldo negativo nem débito duplo.
func TestConcurrentInsufficientFunds(t *testing.T) {
	runner := openRunner(t)
	walletID, playerID := fundWallet(t, runner, "100.00")
	cmds := []application.ProcessCommand{
		processCmd(t, walletID, playerID, wager.KindBet, "80.00"),
		processCmd(t, walletID, playerID, wager.KindBet, "80.00"),
	}
	outcomes := executeTogether(t, runner, cmds)
	processed, rejected := 0, 0
	for i, outcome := range outcomes {
		if outcome.err != nil || outcome.result == nil {
			t.Fatalf("aposta %d: resultado %+v, erro %v", i, outcome.result, outcome.err)
		}
		result := outcome.result
		if result.IdempotentReplay {
			t.Fatalf("aposta %d virou replay: %+v", i, result)
		}
		switch result.Status {
		case wager.StatusProcessed:
			processed++
			if result.Balance.String() != "20.00" || result.WalletVersion != 2 {
				t.Fatalf("processada %d: %+v", i, result)
			}
			if count := countRows(t, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE transaction_id = $1`, result.TransactionID); count != 1 {
				t.Fatalf("débitos da processada = %d", count)
			}
		case wager.StatusRejected:
			rejected++
			if result.FailureCode != wager.CodeInsufficientFunds || result.Balance.String() != "20.00" {
				t.Fatalf("rejeitada %d: %+v", i, result)
			}
			if count := countRows(t, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE transaction_id = $1`, result.TransactionID); count != 0 {
				t.Fatalf("débitos da rejeitada = %d", count)
			}
		default:
			t.Fatalf("estado inesperado da aposta %d: %+v", i, result)
		}
	}
	if processed != 1 || rejected != 1 || countBalance(t, walletID) != "20.00" {
		t.Fatalf("processadas=%d, rejeitadas=%d, saldo=%s", processed, rejected, countBalance(t, walletID))
	}
	if count := countRows(t, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'`, walletID); count != 1 {
		t.Fatalf("débitos totais = %d", count)
	}
	if count := countRows(t, `SELECT COUNT(*) FROM wager_transactions WHERE id IN ($1, $2)`, outcomes[0].result.TransactionID, outcomes[1].result.TransactionID); count != 2 {
		t.Fatalf("operações = %d", count)
	}
}

// TestIndependentWalletsProgress confere que a trava de uma carteira não impede a outra de avançar.
func TestIndependentWalletsProgress(t *testing.T) {
	runner := openRunner(t)
	walletA, playerA := fundWallet(t, runner, "100.00")
	walletB, playerB := fundWallet(t, runner, "100.00")
	cmdA := processCmd(t, walletA, playerA, wager.KindBet, "10.00")
	cmdB := processCmd(t, walletB, playerB, wager.KindBet, "10.00")
	conn := connect(t, ownerURL(t))
	observer := connect(t, ownerURL(t))
	ctx := context.Background()
	lock, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("abrindo trava: %v", err)
	}
	defer lock.Rollback(ctx)
	var locked uuid.UUID
	if err := lock.QueryRow(ctx, `SELECT id FROM wallets WHERE id = $1 FOR UPDATE`, walletA).Scan(&locked); err != nil || locked != walletA {
		t.Fatalf("travando carteira A: %s, %v", locked, err)
	}

	aCtx, cancelA := context.WithTimeout(ctx, 15*time.Second)
	defer cancelA()
	aDone := make(chan concurrentOutcome, 1)
	go func() {
		res, err := application.Execute(aCtx, runner, application.SystemClock{}, application.UUIDv7Generator{}, providerIdent("provider-a"), cmdA)
		aDone <- concurrentOutcome{result: res, err: err}
	}()
	// A inserção pode esperar pela chave estrangeira antes do FOR UPDATE; os dois pontos provam que A está bloqueada.
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		err := observer.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			  AND (query LIKE '%INSERT INTO wager_transactions%'
			    OR query LIKE '%FROM wallets w WHERE w.id = $1 FOR UPDATE%'))`).Scan(&waiting)
		if err != nil {
			lock.Rollback(ctx)
			<-aDone
			t.Fatalf("observando trava: %v", err)
		}
		if waiting {
			break
		}
		select {
		case outcome := <-aDone:
			lock.Rollback(ctx)
			t.Fatalf("aposta A terminou antes de esperar pela trava: %+v, %v", outcome.result, outcome.err)
		default:
		}
		if time.Now().After(deadline) {
			lock.Rollback(ctx)
			<-aDone
			t.Fatal("aposta A não ficou bloqueada pela trava da carteira")
		}
		time.Sleep(20 * time.Millisecond)
	}
	bCtx, cancelB := context.WithTimeout(ctx, 5*time.Second)
	defer cancelB()
	bResult, bErr := application.Execute(bCtx, runner, application.SystemClock{}, application.UUIDv7Generator{}, providerIdent("provider-a"), cmdB)
	select {
	case outcome := <-aDone:
		lock.Rollback(ctx)
		t.Fatalf("A terminou enquanto a trava estava ativa: %+v, %v", outcome.result, outcome.err)
	default:
	}
	if err := lock.Rollback(ctx); err != nil {
		t.Fatalf("liberando carteira A: %v", err)
	}
	aOutcome := <-aDone
	if bErr != nil || bResult == nil || bResult.Status != wager.StatusProcessed {
		t.Fatalf("B não avançou com A travada: %+v, %v", bResult, bErr)
	}
	if aOutcome.err != nil || aOutcome.result == nil || aOutcome.result.Status != wager.StatusProcessed {
		t.Fatalf("A não avançou após liberar a trava: %+v, %v", aOutcome.result, aOutcome.err)
	}
	if balanceA, balanceB := countBalance(t, walletA), countBalance(t, walletB); balanceA != "90.00" || balanceB != "90.00" {
		t.Fatalf("saldos A=%s B=%s", balanceA, balanceB)
	}
	for _, transactionID := range []uuid.UUID{aOutcome.result.TransactionID, bResult.TransactionID} {
		if count := countRows(t, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE transaction_id = $1`, transactionID); count != 1 {
			t.Fatalf("lançamentos de %s = %d", transactionID, count)
		}
	}
}
