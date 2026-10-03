//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// TestSameBetAcrossHTTPAndSQS confirma a mesma aposta nos dois canais, em ambas as ordens.
func TestSameBetAcrossHTTPAndSQS(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sqsFirst bool
	}{
		{name: "HTTP depois SQS"},
		{name: "SQS depois HTTP", sqsFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, providerToken, _, internalToken, runner := testServer(t)
			_, mainURL, dlqURL, client := consumerDeps(t)
			playerID := uuid.NewString()
			opened := openWalletHTTP(t, srv.URL, internalToken, playerID, "1000.00")
			walletID := opened["id"].(string)
			ext := "cross-" + uuid.NewString()
			key := "provider-a:" + ext
			msgID := "cross-msg-" + uuid.NewString()
			body := envelopeFor(t, msgID, walletID, playerID, ext, key, "BET", "25.00", "")

			// A primeira entrega grava a transação; a segunda deve encontrar o mesmo resultado.
			var httpBody map[string]any
			postHTTP := func() {
				t.Helper()
				status, got, _ := doJSON(t, "POST", srv.URL+"/wagering/transactions", providerToken,
					map[string]string{"Idempotency-Key": key}, betBody(playerID, walletID, ext))
				if status != http.StatusOK || got["status"] != "PROCESSED" {
					t.Fatalf("HTTP = %d %v", status, got)
				}
				httpBody = got
			}
			postSQS := func() {
				t.Helper()
				if _, err := client.Send(context.Background(), mainURL, body, walletID, msgID); err != nil {
					t.Fatalf("enviando: %v", err)
				}
				runConsumer(t, runner, client, mainURL, dlqURL, client, 8)
				if n := countRows(t, `SELECT COUNT(*) FROM inbox_messages WHERE message_id = $1`, msgID); n != 1 {
					t.Fatalf("mensagens na inbox = %d", n)
				}
			}
			if tc.sqsFirst {
				postSQS()
				postHTTP()
			} else {
				postHTTP()
				postSQS()
			}

			var txID, correlationID string
			conn := connect(t, ownerURL(t))
			if err := conn.QueryRow(context.Background(), `SELECT id::text, correlation_id FROM wager_transactions
				WHERE provider_id = 'provider-a' AND external_transaction_id = $1`, ext).Scan(&txID, &correlationID); err != nil {
				t.Fatalf("lendo transação: %v", err)
			}
			if httpBody["transactionId"] != txID || httpBody["idempotentReplay"] != tc.sqsFirst {
				t.Fatalf("resultado HTTP = %v, transação = %s", httpBody, txID)
			}
			if balance := countBalance(t, uuid.MustParse(walletID)); balance != "975.00" {
				t.Fatalf("saldo = %s", balance)
			}
			if n := countRows(t, `SELECT COUNT(*) FROM wager_transactions WHERE provider_id = 'provider-a'
				AND external_transaction_id = $1`, ext); n != 1 {
				t.Fatalf("transações = %d", n)
			}
			if n := countRows(t, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE transaction_id = $1`, txID); n != 1 {
				t.Fatalf("débitos = %d", n)
			}
			if n := countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE correlation_id = $1`, correlationID); n != 2 {
				t.Fatalf("eventos da aposta = %d", n)
			}
		})
	}
}
