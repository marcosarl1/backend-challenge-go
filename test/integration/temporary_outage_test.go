//go:build integration

package integration

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/marcosarl1/backend-challenge-go/internal/infra/postgres"
	infrasqs "github.com/marcosarl1/backend-challenge-go/internal/infra/sqs"
)

// pauseOutageContainer pausa o serviço e garante sua retomada mesmo se o teste falhar.
func pauseOutageContainer(t *testing.T, container testcontainers.Container) func() {
	t.Helper()
	docker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("cliente Docker: %v", err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := docker.ContainerPause(ctx, container.GetContainerID()); err != nil {
		t.Fatalf("pausando container: %v", err)
	}
	var once sync.Once
	resume := func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := docker.ContainerUnpause(ctx, container.GetContainerID()); err != nil {
				t.Errorf("retomando container: %v", err)
			}
		})
	}
	t.Cleanup(resume)
	return resume
}

// TestTemporaryPostgresAndSQSOutage verifica retry sem perda nem débito ou evento duplicado.
func TestTemporaryPostgresAndSQSOutage(t *testing.T) {
	ctx := context.Background()
	db, err := postgrescontainer.Run(ctx, "postgres:17.11-alpine",
		postgrescontainer.WithDatabase("wagering"), postgrescontainer.WithUsername("wagering"),
		postgrescontainer.WithPassword("wagering"), postgrescontainer.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("Postgres isolado: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(db) })
	dbURL, err := db.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(ctx, dbURL); err != nil {
		t.Fatal(err)
	}
	sqs, err := testcontainers.Run(ctx, "ministackorg/ministack:1.5.20",
		testcontainers.WithEnv(map[string]string{"AWS_REGION": "us-east-1"}),
		testcontainers.WithExposedPorts("4566/tcp"),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/_localstack/health").WithPort("4566/tcp").WithStartupTimeout(90*time.Second)))
	if err != nil {
		t.Fatalf("SQS isolado: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(sqs) })
	sqsEndpoint, err := sqs.PortEndpoint(ctx, "4566/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}
	if err := createQueues(ctx, sqsEndpoint); err != nil {
		t.Fatal(err)
	}
	queue, err := infrasqs.NewClient(ctx, sqsEndpoint, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	mainURL, err := queue.ResolveQueue(ctx, infrasqs.MainQueue)
	if err != nil {
		t.Fatal(err)
	}
	eventsURL, err := queue.ResolveQueue(ctx, infrasqs.EventsQueue)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	count := func(query string, args ...any) int {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var n int
		if err := pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatalf("contando linhas: %v", err)
		}
		return n
	}
	base, _ := startAppProcessControlled(t, "DATABASE_URL="+dbURL, "SQS_ENDPOINT="+sqsEndpoint,
		"CONSUMER_POLL=1s", "OUTBOX_INTERVAL=200ms", "RETRY_INTERVAL=200ms")
	providerToken := clientToken(t, keycloakURL(t), "provider-a", "provider-a-secret")
	internalToken := clientToken(t, keycloakURL(t), "wallet-internal", "wallet-internal-secret")
	playerID := uuid.NewString()
	walletID := openWalletHTTP(t, base, internalToken, playerID, "100.00")["id"].(string)

	// A fila segura a aposta enquanto o banco está indisponível; a retomada faz um débito só.
	resumeDB := pauseOutageContainer(t, db)
	messageID := "outage-pg-" + uuid.NewString()
	externalID := "outage-pg-bet-" + uuid.NewString()
	body := envelopeFor(t, messageID, walletID, playerID, externalID,
		"provider-a:"+externalID, "BET", "25.00", "")
	if _, err := queue.Send(ctx, mainURL, body, walletID, messageID); err != nil {
		t.Fatalf("enviando durante pausa do banco: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	resumeDB()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if count(`SELECT COUNT(*) FROM wager_transactions WHERE external_transaction_id = $1 AND status = 'PROCESSED'`, externalID) == 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if n := count(`SELECT COUNT(*) FROM wager_transactions WHERE external_transaction_id = $1 AND status = 'PROCESSED'`, externalID); n != 1 {
		t.Fatalf("aposta após retomada do banco = %d", n)
	}
	if n := count(`SELECT COUNT(*) FROM wallet_ledger_entries WHERE transaction_id IN
		(SELECT id FROM wager_transactions WHERE external_transaction_id = $1)`, externalID); n != 1 {
		t.Fatalf("débitos após retomada do banco = %d", n)
	}
	if n := count(`SELECT COUNT(*) FROM inbox_messages WHERE message_id = $1`, messageID); n != 1 {
		t.Fatalf("mensagens na inbox = %d", n)
	}

	// O banco confirma a aposta mesmo sem SQS; a outbox publica seus dois eventos na retomada.
	resumeSQS := pauseOutageContainer(t, sqs)
	secondID := "outage-sqs-bet-" + uuid.NewString()
	secondBody := betBody(playerID, walletID, secondID)
	secondBody["money"] = map[string]any{"amount": "10.00", "currency": "BRL"}
	status, result, _ := doJSON(t, http.MethodPost, base+"/wagering/transactions", providerToken,
		map[string]string{"Idempotency-Key": "provider-a:" + secondID}, secondBody)
	if status != http.StatusOK || result["status"] != "PROCESSED" {
		t.Fatalf("aposta durante pausa da fila = %d %v", status, result)
	}
	var correlationID string
	if err := pool.QueryRow(ctx, `SELECT correlation_id FROM wager_transactions WHERE id = $1`, result["transactionId"]).Scan(&correlationID); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT COUNT(*) FROM outbox_events WHERE correlation_id = $1 AND published_at IS NULL`, correlationID); n != 2 {
		t.Fatalf("eventos duráveis durante pausa da fila = %d", n)
	}
	resumeSQS()
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if count(`SELECT COUNT(*) FROM outbox_events WHERE correlation_id = $1 AND published_at IS NOT NULL`, correlationID) == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if n := count(`SELECT COUNT(*) FROM outbox_events WHERE correlation_id = $1 AND published_at IS NOT NULL`, correlationID); n != 2 {
		t.Fatalf("eventos publicados após retomada da fila = %d", n)
	}
	if n := count(`SELECT COUNT(*) FROM wallet_ledger_entries WHERE transaction_id = $1`, result["transactionId"]); n != 1 {
		t.Fatalf("débitos da aposta HTTP = %d", n)
	}
	if n := count(`SELECT COUNT(*) FROM wager_transactions WHERE external_transaction_id = $1`, secondID); n != 1 {
		t.Fatalf("apostas após retomada da fila = %d", n)
	}
	var balance int64
	if err := pool.QueryRow(ctx, `SELECT balance_minor FROM wallets WHERE id = $1`, walletID).Scan(&balance); err != nil || balance != 6500 {
		t.Fatalf("saldo final = %d, %v", balance, err)
	}
	seen := map[string]bool{}
	deadline = time.Now().Add(15 * time.Second)
	for len(seen) < 2 && time.Now().Before(deadline) {
		messages, err := queue.Receive(ctx, eventsURL, 10, 2)
		if err != nil {
			t.Fatalf("recebendo eventos: %v", err)
		}
		for _, message := range messages {
			id := recoveryEventID(t, message.Body)
			if n := count(`SELECT COUNT(*) FROM outbox_events WHERE id = $1 AND correlation_id = $2`, id, correlationID); n == 1 {
				if seen[id] {
					t.Fatalf("evento duplicado na fila: %s", id)
				}
				seen[id] = true
			}
			if err := queue.Delete(ctx, eventsURL, message.ReceiptHandle); err != nil {
				t.Fatalf("confirmando evento: %v", err)
			}
		}
	}
	if len(seen) != 2 {
		t.Fatalf("eventos da aposta recebidos = %d", len(seen))
	}
	// Uma nova leitura confirma que a retomada não deixou cópias desses eventos na fila.
	messages, err := queue.Receive(ctx, eventsURL, 10, 2)
	if err != nil {
		t.Fatalf("conferindo duplicatas: %v", err)
	}
	for _, message := range messages {
		if seen[recoveryEventID(t, message.Body)] {
			t.Fatalf("evento repetido após retomada: %s", message.Body)
		}
		if err := queue.Delete(ctx, eventsURL, message.ReceiptHandle); err != nil {
			t.Fatalf("confirmando evento restante: %v", err)
		}
	}
}
