//go:build integration

package integration

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// TestHTTPAuthIsolation confere o escopo do token nas escritas, leituras e replays sem deixar efeitos em acessos negados.
func TestHTTPAuthIsolation(t *testing.T) {
	srv, tokenA, tokenB, internalToken, _ := testServer(t)
	playerID := uuid.NewString()
	opened := openWalletHTTP(t, srv.URL, internalToken, playerID, "1000.00")
	walletID := opened["id"].(string)
	ext := "isolation-" + uuid.NewString()
	key := map[string]string{"Idempotency-Key": "provider-a:" + ext}
	bet := betBody(playerID, walletID, ext)
	status, body, _ := doJSON(t, "POST", srv.URL+"/wagering/transactions", tokenA, key, bet)
	if status != http.StatusOK || body["idempotentReplay"] != false {
		t.Fatalf("aposta A = %d %v", status, body)
	}
	txID := body["transactionId"].(string)
	ledgerBefore := countRows(t, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, walletID)
	outboxBefore := countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE aggregate_id = $1`, walletID)

	// A recebe a resposta gravada; B não pode usar a chave nem o id externo de A para obter um replay.
	status, body, _ = doJSON(t, "POST", srv.URL+"/wagering/transactions", tokenA, key, bet)
	if status != http.StatusOK || body["idempotentReplay"] != true || body["transactionId"] != txID {
		t.Fatalf("replay A = %d %v", status, body)
	}
	status, body, _ = doJSON(t, "POST", srv.URL+"/wagering/transactions", tokenB, key, bet)
	if status != http.StatusForbidden || body["code"] != "FORBIDDEN" {
		t.Fatalf("replay B = %d %v", status, body)
	}
	for _, tt := range []struct {
		name   string
		path   string
		token  string
		status int
	}{
		{"dono por id", "/wagering/transactions/" + txID, tokenA, http.StatusOK},
		{"outro provedor por id", "/wagering/transactions/" + txID, tokenB, http.StatusNotFound},
		{"dono por externo", "/providers/provider-a/wagering/transactions/" + ext, tokenA, http.StatusOK},
		{"outro provedor no caminho A", "/providers/provider-a/wagering/transactions/" + ext, tokenB, http.StatusForbidden},
		{"outro provedor no próprio caminho", "/providers/provider-b/wagering/transactions/" + ext, tokenB, http.StatusNotFound},
		{"serviço interno por id", "/wagering/transactions/" + txID, internalToken, http.StatusOK},
		{"serviço interno por externo", "/providers/provider-a/wagering/transactions/" + ext, internalToken, http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, _, _ := doJSON(t, "GET", srv.URL+tt.path, tt.token, nil, nil)
			if got != tt.status {
				t.Fatalf("status = %d, esperado %d", got, tt.status)
			}
		})
	}

	// Nenhuma credencial ou papel inadequado pode criar uma segunda operação.
	for _, tt := range []struct {
		name   string
		token  string
		status int
	}{
		{"ausente", "", http.StatusUnauthorized},
		{"inválido", "não-é-token", http.StatusUnauthorized},
		{"serviço interno", internalToken, http.StatusForbidden},
		{"provedor B como A", tokenB, http.StatusForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			evilExt := "denied-" + uuid.NewString()
			corr := "corr-" + evilExt
			got, problem, headers := doJSON(t, "POST", srv.URL+"/wagering/transactions", tt.token,
				map[string]string{"Idempotency-Key": "provider-a:" + evilExt, "X-Correlation-Id": corr}, betBody(playerID, walletID, evilExt))
			if got != tt.status {
				t.Fatalf("status = %d, esperado %d: %v", got, tt.status, problem)
			}
			if got == http.StatusUnauthorized && headers.Get("WWW-Authenticate") != "Bearer" {
				t.Fatalf("401 sem desafio Bearer: %v", headers)
			}
			if count := countRows(t, `SELECT COUNT(*) FROM wager_transactions WHERE external_transaction_id = $1`, evilExt); count != 0 {
				t.Fatalf("operação negada persistiu: %d", count)
			}
			if count := countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE correlation_id = $1`, corr); count != 0 {
				t.Fatalf("evento de operação negada persistiu: %d", count)
			}
		})
	}

	// Rotas de carteira pertencem ao serviço interno, inclusive leitura e reconciliação.
	deniedPlayer := uuid.NewString()
	for _, tt := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/wallets", map[string]any{"playerId": deniedPlayer, "initialBalance": map[string]any{"amount": "1.00", "currency": "BRL"}}},
		{"GET", "/wallets/" + walletID, nil},
		{"GET", "/wallets/" + walletID + "/ledger", nil},
		{"POST", "/wallets/" + walletID + "/reconciliation", nil},
	} {
		status, problem, _ := doJSON(t, tt.method, srv.URL+tt.path, tokenA, nil, tt.body)
		if status != http.StatusForbidden || problem["code"] != "FORBIDDEN" {
			t.Fatalf("%s %s = %d %v", tt.method, tt.path, status, problem)
		}
	}
	if count := countRows(t, `SELECT COUNT(*) FROM wallets WHERE player_id = $1`, deniedPlayer); count != 0 {
		t.Fatalf("carteira negada persistiu: %d", count)
	}
	parsedWalletID, err := uuid.Parse(walletID)
	if err != nil {
		t.Fatalf("id da carteira: %v", err)
	}
	if balance := countBalance(t, parsedWalletID); balance != "975.00" {
		t.Fatalf("saldo mudou após negações: %s", balance)
	}
	if count := countRows(t, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, walletID); count != ledgerBefore {
		t.Fatalf("ledger mudou após negações: %d, antes %d", count, ledgerBefore)
	}
	if count := countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE aggregate_id = $1`, walletID); count != outboxBefore {
		t.Fatalf("outbox mudou após negações: %d, antes %d", count, outboxBefore)
	}
	if count := countRows(t, `SELECT COUNT(*) FROM wager_transactions WHERE provider_id = 'provider-a' AND external_transaction_id = $1`, ext); count != 1 {
		t.Fatalf("aposta original duplicada: %d", count)
	}

	// O mesmo id externo de B é outra operação, nunca um replay dos dados de A.
	betB := betBody(playerID, walletID, ext)
	betB["providerId"] = "provider-b"
	status, body, _ = doJSON(t, "POST", srv.URL+"/wagering/transactions", tokenB,
		map[string]string{"Idempotency-Key": "provider-b:" + ext}, betB)
	if status != http.StatusOK || body["idempotentReplay"] != false || body["transactionId"] == txID {
		t.Fatalf("operação própria de B = %d %v", status, body)
	}
	status, own, _ := doJSON(t, "GET", srv.URL+"/providers/provider-b/wagering/transactions/"+ext, tokenB, nil, nil)
	if status != http.StatusOK || own["transactionId"] != body["transactionId"] {
		t.Fatalf("consulta de B = %d %v", status, own)
	}
	status, original, _ := doJSON(t, "GET", srv.URL+"/providers/provider-a/wagering/transactions/"+ext, tokenA, nil, nil)
	if status != http.StatusOK || original["transactionId"] != txID {
		t.Fatalf("consulta de A mudou = %d %v", status, original)
	}
}
