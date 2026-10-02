package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/auth"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/httpapi"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/postgres"
)

func testServer(t *testing.T) (*httptest.Server, string, string, string, postgres.Runner) {
	t.Helper()
	dbURL := ownerURL(t)
	base := keycloakURL(t)
	ctx := context.Background()
	pool, err := postgres.Connect(ctx, dbURL)
	if err != nil {
		t.Fatalf("conectando: %v", err)
	}
	t.Cleanup(pool.Close)
	uow := postgres.NewUnitOfWork(pool)
	runner := postgres.NewRunner(uow)
	verifier, err := auth.NewVerifier(ctx, base+"/realms/wagering",
		base+"/realms/wagering/protocol/openid-connect/certs", "wagering-api")
	if err != nil {
		t.Fatalf("verificador: %v", err)
	}
	srv := httptest.NewServer(httpapi.New(runner, verifier, application.SystemClock{}, application.UUIDv7Generator{}).Handler())
	t.Cleanup(srv.Close)
	return srv,
		clientToken(t, base, "provider-a", "provider-a-secret"),
		clientToken(t, base, "provider-b", "provider-b-secret"),
		clientToken(t, base, "wallet-internal", "wallet-internal-secret"),
		runner
}

func doJSON(t *testing.T, method, url, token string, headers map[string]string, body any) (int, map[string]any, http.Header) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		if s, ok := body.(string); ok {
			reader = strings.NewReader(s)
		} else {
			data, _ := json.Marshal(body)
			reader = bytes.NewReader(data)
		}
	}
	req, _ := http.NewRequest(method, url, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("chamando: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var decoded map[string]any
	_ = json.Unmarshal(data, &decoded)
	return resp.StatusCode, decoded, resp.Header
}

func betBody(playerID, walletID, ext string) map[string]any {
	return map[string]any{
		"providerId": "provider-a", "externalTransactionId": ext,
		"playerId": playerID, "walletId": walletID,
		"roundId": "round-1", "gameId": "jogo", "kind": "BET",
		"money": map[string]any{"amount": "25.00", "currency": "BRL"},
	}
}

func openWalletHTTP(t *testing.T, srv, token, playerID, amount string) map[string]any {
	t.Helper()
	status, body, _ := doJSON(t, "POST", srv+"/wallets", token, nil, map[string]any{
		"playerId":       playerID,
		"initialBalance": map[string]any{"amount": amount, "currency": "BRL"},
	})
	if status != http.StatusCreated {
		t.Fatalf("abertura = %d %v", status, body)
	}
	return body
}

func TestContractHappyPaths(t *testing.T) {
	srv, providerToken, _, internalToken, runner := testServer(t)
	playerID := uuid.NewString()
	opened := openWalletHTTP(t, srv.URL, internalToken, playerID, "1000.00")
	walletID := opened["id"].(string)

	// 200: aposta processada.
	ext := "http-" + uuid.NewString()
	status, body, _ := doJSON(t, "POST", srv.URL+"/wagering/transactions", providerToken,
		map[string]string{"Idempotency-Key": "provider-a:" + ext}, betBody(playerID, walletID, ext))
	if status != http.StatusOK || body["status"] != "PROCESSED" || body["idempotentReplay"] != false {
		t.Fatalf("aposta = %d %v", status, body)
	}
	txID := body["transactionId"].(string)

	// 200: leitura da transação e da carteira.
	if status, _, _ := doJSON(t, "GET", srv.URL+"/wagering/transactions/"+txID, providerToken, nil, nil); status != http.StatusOK {
		t.Fatalf("leitura = %d", status)
	}
	if status, _, _ := doJSON(t, "GET", srv.URL+"/wallets/"+walletID, internalToken, nil, nil); status != http.StatusOK {
		t.Fatalf("carteira = %d", status)
	}
	// 200: reconciliação consistente.
	if status, body, _ := doJSON(t, "POST", srv.URL+"/wallets/"+walletID+"/reconciliation", internalToken, nil, nil); status != http.StatusOK || body["consistent"] != true {
		t.Fatalf("conciliação = %d %v", status, body)
	}
	// 202: reembolso antes da aposta.
	status, body, headers := doJSON(t, "POST", srv.URL+"/wagering/transactions", providerToken,
		map[string]string{"Idempotency-Key": "provider-a:rb-" + ext},
		map[string]any{
			"providerId": "provider-a", "externalTransactionId": "rb-" + ext,
			"playerId": playerID, "walletId": walletID,
			"roundId": "round-1", "gameId": "jogo", "kind": "REFUND",
			"money":                          map[string]any{"amount": "25.00", "currency": "BRL"},
			"referenceExternalTransactionId": "futura-" + ext,
		})
	if status != http.StatusAccepted || body["status"] != "PENDING_REFERENCE" {
		t.Fatalf("reembolso = %d %v", status, body)
	}
	if headers.Get("Location") == "" || headers.Get("Retry-After") == "" {
		t.Fatal("202 sem Location/Retry-After")
	}
	// Fecha o ciclo: a aposta chega e a retomada conclui o reembolso.
	status, _, _ = doJSON(t, "POST", srv.URL+"/wagering/transactions", providerToken,
		map[string]string{"Idempotency-Key": "provider-a:futura-" + ext}, betBody(playerID, walletID, "futura-"+ext))
	if status != http.StatusOK {
		t.Fatalf("aposta futura = %d", status)
	}
	if _, err := connect(t, ownerURL(t)).Exec(context.Background(), `UPDATE wager_transactions
		SET next_attempt_at = now() - interval '1 second'
		WHERE external_transaction_id = $1`, "rb-"+ext); err != nil {
		t.Fatalf("adiantando: %v", err)
	}
	n, err := application.RetryPending(context.Background(), runner, application.SystemClock{}, application.UUIDv7Generator{}, 10)
	if err != nil || n < 1 {
		t.Fatalf("retomadas = %d, %v", n, err)
	}
	status, body, _ = doJSON(t, "GET", srv.URL+"/providers/provider-a/wagering/transactions/rb-"+ext, providerToken, nil, nil)
	if status != http.StatusOK || body["status"] != "PROCESSED" {
		t.Fatalf("reembolso = %d %v", status, body)
	}
}

func TestContractErrors(t *testing.T) {
	srv, providerToken, providerBToken, internalToken, _ := testServer(t)
	playerID := uuid.NewString()
	opened := openWalletHTTP(t, srv.URL, internalToken, playerID, "1000.00")
	walletID := opened["id"].(string)

	for _, tt := range []struct {
		name   string
		method string
		path   string
		token  string
		head   map[string]string
		body   any
		status int
		code   string
	}{
		{"sem auth", "GET", "/wallets/" + walletID, "", nil, nil, 401, "UNAUTHENTICATED"},
		{"token ruim", "GET", "/wallets/" + walletID, "ruim", nil, nil, 401, "UNAUTHENTICATED"},
		{"provedor abre carteira", "POST", "/wallets", providerToken, nil,
			map[string]any{"playerId": uuid.NewString(), "initialBalance": map[string]any{"amount": "1.00", "currency": "BRL"}}, 403, "FORBIDDEN"},
		{"json inválido", "POST", "/wallets", internalToken, nil, "não-json", 400, "INVALID_REQUEST"},
		{"campo desconhecido", "POST", "/wallets", internalToken, nil,
			map[string]any{"playerId": playerID, "initialBalance": map[string]any{"amount": "1.00", "currency": "BRL"}, "extra": 1}, 400, "INVALID_REQUEST"},
		{"dinheiro ruim", "POST", "/wallets", internalToken, nil,
			map[string]any{"playerId": playerID, "initialBalance": map[string]any{"amount": "25.0", "currency": "BRL"}}, 400, "INVALID_REQUEST"},
		{"uuid ruim", "GET", "/wallets/abc", internalToken, nil, nil, 400, "INVALID_REQUEST"},
		{"sem chave", "POST", "/wagering/transactions", providerToken, nil, betBody(playerID, walletID, "x"), 400, "INVALID_REQUEST"},
		{"tx fantasma", "GET", "/wagering/transactions/" + uuid.NewString(), providerToken, nil, nil, 404, "NOT_FOUND"},
	} {
		status, body, _ := doJSON(t, tt.method, srv.URL+tt.path, tt.token, tt.head, tt.body)
		if status != tt.status || body["code"] != tt.code {
			t.Fatalf("%s = %d %v", tt.name, status, body)
		}
		if body["correlationId"] == "" {
			t.Fatalf("%s sem correlationId", tt.name)
		}
	}

	// 409: chave com outro conteúdo.
	ext := "dup-" + uuid.NewString()
	key := map[string]string{"Idempotency-Key": "provider-a:" + ext}
	if status, _, _ := doJSON(t, "POST", srv.URL+"/wagering/transactions", providerToken, key, betBody(playerID, walletID, ext)); status != http.StatusOK {
		t.Fatalf("primeira = %d", status)
	}
	other := betBody(playerID, walletID, ext)
	other["money"] = map[string]any{"amount": "30.00", "currency": "BRL"}
	if status, body, _ := doJSON(t, "POST", srv.URL+"/wagering/transactions", providerToken, key, other); status != http.StatusConflict || body["code"] != "IDEMPOTENCY_KEY_PAYLOAD_MISMATCH" {
		t.Fatalf("mismatch = %d %v", status, body)
	}
	// 409: id externo com outra chave.
	otherKey := map[string]string{"Idempotency-Key": "provider-a:outra-" + ext}
	if status, body, _ := doJSON(t, "POST", srv.URL+"/wagering/transactions", providerToken, otherKey, betBody(playerID, walletID, ext)); status != http.StatusConflict || body["code"] != "EXTERNAL_TRANSACTION_ID_CONFLICT" {
		t.Fatalf("reuso = %d %v", status, body)
	}
	// 409: carteira duplicada.
	if status, body, _ := doJSON(t, "POST", srv.URL+"/wallets", internalToken, nil,
		map[string]any{"playerId": playerID, "initialBalance": map[string]any{"amount": "1.00", "currency": "BRL"}}); status != http.StatusConflict || body["code"] != "WALLET_ALREADY_EXISTS" {
		t.Fatalf("duplicada = %d %v", status, body)
	}
	// 422: sem saldo (e de novo no replay).
	noFunds := betBody(playerID, walletID, "sem-saldo-"+uuid.NewString())
	noFunds["money"] = map[string]any{"amount": "1500.00", "currency": "BRL"}
	status, body, _ := doJSON(t, "POST", srv.URL+"/wagering/transactions", providerToken,
		map[string]string{"Idempotency-Key": "provider-a:" + noFunds["externalTransactionId"].(string)}, noFunds)
	if status != http.StatusUnprocessableEntity || body["failureCode"] != "INSUFFICIENT_FUNDS" {
		t.Fatalf("rejeição = %d %v", status, body)
	}
	status, body, _ = doJSON(t, "POST", srv.URL+"/wagering/transactions", providerToken,
		map[string]string{"Idempotency-Key": "provider-a:" + noFunds["externalTransactionId"].(string)}, noFunds)
	if status != http.StatusUnprocessableEntity || body["idempotentReplay"] != true {
		t.Fatalf("replay = %d %v", status, body)
	}
	// 404: transação de outro provedor some (nem a existência vaza).
	otherTx := betBody(playerID, walletID, "alheia-"+uuid.NewString())
	status, _, _ = doJSON(t, "POST", srv.URL+"/wagering/transactions", providerToken,
		map[string]string{"Idempotency-Key": "provider-a:" + otherTx["externalTransactionId"].(string)}, otherTx)
	if status != http.StatusOK {
		t.Fatalf("alheia = %d", status)
	}
	if status, _, _ := doJSON(t, "GET", srv.URL+"/providers/provider-a/wagering/transactions/"+otherTx["externalTransactionId"].(string),
		providerBToken, nil, nil); status != http.StatusForbidden {
		t.Fatalf("caminho alheio = %d", status)
	}
	// Id direto de outro provedor: 404, sem vazar existência.
	status, body, _ = doJSON(t, "GET", srv.URL+"/wagering/transactions/"+txIDOf(t, srv.URL, providerToken, otherTx),
		providerBToken, nil, nil)
	if status != http.StatusNotFound {
		t.Fatalf("id alheio = %d %v", status, body)
	}
}

func txIDOf(t *testing.T, base, token string, created map[string]any) string {
	t.Helper()
	status, body, _ := doJSON(t, "GET", base+"/providers/provider-a/wagering/transactions/"+created["externalTransactionId"].(string),
		token, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("busca = %d %v", status, body)
	}
	id, _ := body["transactionId"].(string)
	return id
}
