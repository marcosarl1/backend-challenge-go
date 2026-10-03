//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestRestartPreservesIdempotencyAndPending confere replay e retomada após trocar o processo real.
func TestRestartPreservesIdempotencyAndPending(t *testing.T) {
	base, stop := startAppProcessControlled(t, "CONSUMER_POLL=1s", "RETRY_INTERVAL=200ms")
	providerToken := clientToken(t, keycloakURL(t), "provider-a", "provider-a-secret")
	internalToken := clientToken(t, keycloakURL(t), "wallet-internal", "wallet-internal-secret")
	playerID := uuid.NewString()
	walletID := openWalletHTTP(t, base, internalToken, playerID, "100.00")["id"].(string)
	betID := "restart-bet-" + uuid.NewString()
	betKey := "provider-a:" + betID
	status, firstBet, _ := doJSON(t, http.MethodPost, base+"/wagering/transactions", providerToken,
		map[string]string{"Idempotency-Key": betKey}, betBody(playerID, walletID, betID))
	if status != http.StatusOK || firstBet["status"] != "PROCESSED" || firstBet["idempotentReplay"] != false {
		t.Fatalf("primeira aposta = %d %v", status, firstBet)
	}
	transactionID := firstBet["transactionId"].(string)

	referenceID := "restart-future-" + uuid.NewString()
	refundID := "restart-refund-" + uuid.NewString()
	status, pending, _ := doJSON(t, http.MethodPost, base+"/wagering/transactions", providerToken,
		map[string]string{"Idempotency-Key": "provider-a:" + refundID}, map[string]any{
			"providerId": "provider-a", "externalTransactionId": refundID,
			"playerId": playerID, "walletId": walletID,
			"roundId": "round-1", "gameId": "jogo", "kind": "REFUND",
			"money":                          map[string]any{"amount": "10.00", "currency": "BRL"},
			"referenceExternalTransactionId": referenceID,
		})
	if status != http.StatusAccepted || pending["status"] != "PENDING_REFERENCE" {
		t.Fatalf("pendência = %d %v", status, pending)
	}
	refundTransactionID := pending["transactionId"].(string)
	stop()

	if n := countRows(t, `SELECT COUNT(*) FROM wager_transactions WHERE id = $1 AND status = 'PENDING_REFERENCE'`, refundTransactionID); n != 1 {
		t.Fatalf("pendência após parada = %d", n)
	}
	base, _ = startAppProcessControlled(t, "CONSUMER_POLL=1s", "RETRY_INTERVAL=200ms")
	status, replay, _ := doJSON(t, http.MethodPost, base+"/wagering/transactions", providerToken,
		map[string]string{"Idempotency-Key": betKey}, betBody(playerID, walletID, betID))
	if status != http.StatusOK || replay["transactionId"] != transactionID || replay["idempotentReplay"] != true {
		t.Fatalf("replay após restart = %d %v", status, replay)
	}
	futureBody := betBody(playerID, walletID, referenceID)
	futureBody["money"] = map[string]any{"amount": "10.00", "currency": "BRL"}
	status, futureBet, _ := doJSON(t, http.MethodPost, base+"/wagering/transactions", providerToken,
		map[string]string{"Idempotency-Key": "provider-a:" + referenceID}, futureBody)
	if status != http.StatusOK || futureBet["status"] != "PROCESSED" {
		t.Fatalf("aposta de referência = %d %v", status, futureBet)
	}
	if _, err := connect(t, ownerURL(t)).Exec(context.Background(), `UPDATE wager_transactions
		SET next_attempt_at = now() - interval '1 second' WHERE id = $1 AND status = 'PENDING_REFERENCE'`, refundTransactionID); err != nil {
		t.Fatalf("adiantando pendência: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if countRows(t, `SELECT COUNT(*) FROM wager_transactions WHERE id = $1 AND status = 'PROCESSED'`, refundTransactionID) == 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM wager_transactions WHERE id = $1 AND status = 'PROCESSED'`, refundTransactionID); n != 1 {
		t.Fatal("reembolso não foi retomado após restart")
	}

	for _, txID := range []string{transactionID, futureBet["transactionId"].(string), refundTransactionID} {
		if n := countRows(t, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE transaction_id = $1`, txID); n != 1 {
			t.Fatalf("lançamentos da transação %s = %d", txID, n)
		}
	}
	if n := countRows(t, `SELECT COUNT(*) FROM wager_transactions WHERE provider_id = 'provider-a'
		AND external_transaction_id = $1`, betID); n != 1 {
		t.Fatalf("apostas após replay = %d", n)
	}
	var balance, ledgerTotal int64
	conn := connect(t, ownerURL(t))
	if err := conn.QueryRow(context.Background(), `SELECT balance_minor FROM wallets WHERE id = $1`, walletID).Scan(&balance); err != nil {
		t.Fatalf("lendo saldo: %v", err)
	}
	if err := conn.QueryRow(context.Background(), `SELECT COALESCE(SUM(CASE direction
		WHEN 'CREDIT' THEN amount_minor ELSE -amount_minor END), 0)
		FROM wallet_ledger_entries WHERE wallet_id = $1`, walletID).Scan(&ledgerTotal); err != nil {
		t.Fatalf("somando ledger: %v", err)
	}
	if balance != 7500 || balance != ledgerTotal {
		t.Fatalf("saldo=%d, créditos menos débitos=%d", balance, ledgerTotal)
	}
}
