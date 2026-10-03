//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type multiBetRequest struct {
	base string
	key  string
	body []byte
}

type multiBetOutcome struct {
	status int
	body   struct {
		TransactionID    string `json:"transactionId"`
		Status           string `json:"status"`
		IdempotentReplay bool   `json:"idempotentReplay"`
		FailureCode      string `json:"failureCode"`
		Balance          struct {
			Amount string `json:"amount"`
		} `json:"balance"`
	}
	err error
}

// postMultiBet envia uma aposta sem usar testing.T na goroutine que faz a requisição.
func postMultiBet(ctx context.Context, client *http.Client, token string, bet multiBetRequest) multiBetOutcome {
	var out multiBetOutcome
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, bet.base+"/wagering/transactions", bytes.NewReader(bet.body))
	if err != nil {
		out.err = err
		return out
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", bet.key)
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		out.err = err
		return out
	}
	defer response.Body.Close()
	out.status = response.StatusCode
	out.err = json.NewDecoder(response.Body).Decode(&out.body)
	return out
}

// postMultiBets solta requisições para processos distintos ao mesmo tempo e espera todas terminarem.
func postMultiBets(t *testing.T, token string, bets []multiBetRequest) []multiBetOutcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 40 * time.Second}
	start := make(chan struct{})
	outcomes := make([]multiBetOutcome, len(bets))
	var wg sync.WaitGroup
	wg.Add(len(bets))
	for i := range bets {
		go func(i int) {
			defer wg.Done()
			<-start
			outcomes[i] = postMultiBet(ctx, client, token, bets[i])
		}(i)
	}
	close(start)
	wg.Wait()
	return outcomes
}

// multiBetBody monta o mesmo contrato HTTP usado pelas apostas normais, com valor escolhido pelo teste.
func multiBetBody(t *testing.T, playerID, walletID, externalID, amount string) []byte {
	t.Helper()
	body := betBody(playerID, walletID, externalID)
	body["money"] = map[string]any{"amount": amount, "currency": "BRL"}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("serializando aposta: %v", err)
	}
	return data
}

// TestMultiInstanceConcurrency repete os cenários financeiros através de três processos que compartilham só o banco.
func TestMultiInstanceConcurrency(t *testing.T) {
	bases := []string{startAppProcess(t), startAppProcess(t), startAppProcess(t)}
	providerToken := clientToken(t, keycloakURL(t), "provider-a", "provider-a-secret")
	internalToken := clientToken(t, keycloakURL(t), "wallet-internal", "wallet-internal-secret")

	// Cada processo autentica e consulta o mesmo banco antes da disputa.
	warm := func(t *testing.T, walletID string) {
		t.Helper()
		for _, base := range bases {
			status, _, _ := doJSON(t, http.MethodGet, base+"/wallets/"+walletID, internalToken, nil, nil)
			if status != http.StatusOK {
				t.Fatalf("instância %s não leu carteira: %d", base, status)
			}
		}
	}

	t.Run("cinquenta entregas da mesma aposta", func(t *testing.T) {
		playerID := uuid.NewString()
		walletID := openWalletHTTP(t, bases[0], internalToken, playerID, "100.00")["id"].(string)
		warm(t, walletID)
		externalID := "multi-same-" + uuid.NewString()
		key := "provider-a:" + externalID
		body := multiBetBody(t, playerID, walletID, externalID, "10.00")
		bets := make([]multiBetRequest, 50)
		for i := range bets {
			bets[i] = multiBetRequest{base: bases[i%len(bases)], key: key, body: body}
		}
		outcomes := postMultiBets(t, providerToken, bets)
		transactionID := ""
		newCount, replayCount := 0, 0
		for i, outcome := range outcomes {
			if outcome.err != nil || outcome.status != http.StatusOK || outcome.body.Status != "PROCESSED" || outcome.body.Balance.Amount != "90.00" {
				t.Fatalf("entrega %d: %+v", i, outcome)
			}
			if transactionID == "" {
				transactionID = outcome.body.TransactionID
			} else if outcome.body.TransactionID != transactionID {
				t.Fatalf("entrega %d gerou outra transação: %s", i, outcome.body.TransactionID)
			}
			if outcome.body.IdempotentReplay {
				replayCount++
			} else {
				newCount++
			}
		}
		if newCount != 1 || replayCount != 49 {
			t.Fatalf("novas=%d, replays=%d", newCount, replayCount)
		}
		if count := countRows(t, `SELECT COUNT(*) FROM wager_transactions WHERE provider_id = 'provider-a' AND idempotency_key = $1`, key); count != 1 {
			t.Fatalf("transações = %d", count)
		}
		if count := countRows(t, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE transaction_id = $1 AND direction = 'DEBIT'`, transactionID); count != 1 {
			t.Fatalf("débitos = %d", count)
		}
		parsedWalletID, err := uuid.Parse(walletID)
		if err != nil {
			t.Fatal(err)
		}
		if balance := countBalance(t, parsedWalletID); balance != "90.00" {
			t.Fatalf("saldo = %s", balance)
		}
	})

	t.Run("duas apostas sobre o mesmo saldo", func(t *testing.T) {
		playerID := uuid.NewString()
		walletID := openWalletHTTP(t, bases[1], internalToken, playerID, "100.00")["id"].(string)
		warm(t, walletID)
		bets := make([]multiBetRequest, 2)
		for i := range bets {
			externalID := "multi-80-" + uuid.NewString()
			bets[i] = multiBetRequest{base: bases[i], key: "provider-a:" + externalID,
				body: multiBetBody(t, playerID, walletID, externalID, "80.00")}
		}
		outcomes := postMultiBets(t, providerToken, bets)
		processed, rejected := 0, 0
		for i, outcome := range outcomes {
			if outcome.err != nil || outcome.body.IdempotentReplay {
				t.Fatalf("aposta %d: %+v", i, outcome)
			}
			switch {
			case outcome.status == http.StatusOK && outcome.body.Status == "PROCESSED":
				processed++
				if outcome.body.Balance.Amount != "20.00" || countRows(t, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE transaction_id = $1`, outcome.body.TransactionID) != 1 {
					t.Fatalf("processada %d: %+v", i, outcome)
				}
			case outcome.status == http.StatusUnprocessableEntity && outcome.body.Status == "REJECTED":
				rejected++
				if outcome.body.FailureCode != "INSUFFICIENT_FUNDS" || countRows(t, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE transaction_id = $1`, outcome.body.TransactionID) != 0 {
					t.Fatalf("rejeitada %d: %+v", i, outcome)
				}
			default:
				t.Fatalf("estado da aposta %d: %+v", i, outcome)
			}
		}
		if processed != 1 || rejected != 1 {
			t.Fatalf("processadas=%d, rejeitadas=%d", processed, rejected)
		}
		if count := countRows(t, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'`, walletID); count != 1 {
			t.Fatalf("débitos = %d", count)
		}
		parsedWalletID, err := uuid.Parse(walletID)
		if err != nil {
			t.Fatal(err)
		}
		if balance := countBalance(t, parsedWalletID); balance != "20.00" {
			t.Fatalf("saldo = %s", balance)
		}
	})

	t.Run("carteiras independentes", func(t *testing.T) {
		playerA, playerB := uuid.NewString(), uuid.NewString()
		walletA := openWalletHTTP(t, bases[0], internalToken, playerA, "100.00")["id"].(string)
		walletB := openWalletHTTP(t, bases[2], internalToken, playerB, "100.00")["id"].(string)
		warm(t, walletA)
		warm(t, walletB)
		conn := connect(t, ownerURL(t))
		observer := connect(t, ownerURL(t))
		ctx := context.Background()
		lock, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Rollback(ctx)
		var locked uuid.UUID
		if err := lock.QueryRow(ctx, `SELECT id FROM wallets WHERE id = $1 FOR UPDATE`, walletA).Scan(&locked); err != nil || locked.String() != walletA {
			t.Fatalf("travando A: %s, %v", locked, err)
		}
		client := &http.Client{Timeout: 15 * time.Second}
		aCtx, cancelA := context.WithTimeout(ctx, 15*time.Second)
		defer cancelA()
		aDone := make(chan multiBetOutcome, 1)
		externalA := "multi-wallet-a-" + uuid.NewString()
		bodyA := multiBetBody(t, playerA, walletA, externalA, "10.00")
		go func() {
			aDone <- postMultiBet(aCtx, client, providerToken, multiBetRequest{base: bases[0], key: "provider-a:" + externalA,
				body: bodyA})
		}()
		// A pode parar na checagem da FK ou no FOR UPDATE; B deve terminar enquanto A ainda espera.
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
				t.Fatalf("observando A: %v", err)
			}
			if waiting {
				break
			}
			select {
			case outcome := <-aDone:
				lock.Rollback(ctx)
				t.Fatalf("A terminou antes da trava: %+v", outcome)
			default:
			}
			if time.Now().After(deadline) {
				lock.Rollback(ctx)
				<-aDone
				t.Fatal("A não ficou bloqueada")
			}
			time.Sleep(20 * time.Millisecond)
		}
		bCtx, cancelB := context.WithTimeout(ctx, 5*time.Second)
		defer cancelB()
		externalB := "multi-wallet-b-" + uuid.NewString()
		bOutcome := postMultiBet(bCtx, client, providerToken, multiBetRequest{base: bases[2], key: "provider-a:" + externalB,
			body: multiBetBody(t, playerB, walletB, externalB, "10.00")})
		select {
		case outcome := <-aDone:
			lock.Rollback(ctx)
			t.Fatalf("A terminou antes de liberar a trava: %+v", outcome)
		default:
		}
		if err := lock.Rollback(ctx); err != nil {
			t.Fatalf("liberando A: %v", err)
		}
		aOutcome := <-aDone
		for name, outcome := range map[string]multiBetOutcome{"A": aOutcome, "B": bOutcome} {
			if outcome.err != nil || outcome.status != http.StatusOK || outcome.body.Status != "PROCESSED" || outcome.body.Balance.Amount != "90.00" {
				t.Fatalf("carteira %s: %+v", name, outcome)
			}
			if count := countRows(t, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE transaction_id = $1`, outcome.body.TransactionID); count != 1 {
				t.Fatalf("carteira %s: lançamentos=%d", name, count)
			}
		}
		for _, walletID := range []string{walletA, walletB} {
			parsed, err := uuid.Parse(walletID)
			if err != nil {
				t.Fatal(err)
			}
			if balance := countBalance(t, parsed); balance != "90.00" {
				t.Fatalf("carteira %s: saldo=%s", walletID, balance)
			}
		}
	})
}
