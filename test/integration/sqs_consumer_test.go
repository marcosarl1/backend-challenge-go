//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wager"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/postgres"
	infrasqs "github.com/marcosarl1/backend-challenge-go/internal/infra/sqs"
)

func consumerDeps(t *testing.T) (postgres.Runner, string, string, *infrasqs.Client) {
	t.Helper()
	ctx := context.Background()
	pool, err := postgres.Connect(ctx, ownerURL(t))
	if err != nil {
		t.Fatalf("conectando: %v", err)
	}
	t.Cleanup(pool.Close)
	client, err := infrasqs.NewClient(ctx, sqsURL(t), "us-east-1")
	if err != nil {
		t.Fatalf("fila: %v", err)
	}
	mainURL := isolatedQueue(t, "consumer-main")
	dlqURL := isolatedQueue(t, "consumer-dlq")
	return postgres.NewRunner(postgres.NewUnitOfWork(pool)), mainURL, dlqURL, client
}

func envelopeFor(t *testing.T, messageID, walletID, playerID, ext, key, kind, amount string, ref string) string {
	t.Helper()
	data := map[string]any{
		"providerId": "provider-a", "externalTransactionId": ext,
		"idempotencyKey": key, "playerId": playerID, "walletId": walletID,
		"roundId": "round-1", "gameId": "jogo", "kind": kind,
		"money": map[string]any{"amount": amount, "currency": "BRL"},
	}
	if ref != "" {
		data["referenceExternalTransactionId"] = ref
	}
	envelope := map[string]any{
		"messageId": messageID, "type": "WagerTransactionRequested",
		"occurredAt": "2026-09-08T12:00:00Z", "data": data,
	}
	raw, _ := json.Marshal(envelope)
	return string(raw)
}

func runConsumer(t *testing.T, runner postgres.Runner, client *infrasqs.Client, mainURL, dlqURL string, queues infrasqs.QueueOps, seconds int) {
	t.Helper()
	consumer := infrasqs.NewConsumer(queues, mainURL, dlqURL, "test-consumer",
		runner, application.SystemClock{}, application.UUIDv7Generator{}, 2, 2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(seconds)*time.Second)
	defer cancel()
	if err := consumer.Run(ctx); err != nil {
		t.Fatalf("consumidor: %v", err)
	}
}

func drain(t *testing.T, client *infrasqs.Client, url string) []infrasqs.Received {
	t.Helper()
	var all []infrasqs.Received
	for range 3 {
		got, err := client.Receive(context.Background(), url, 10, 2)
		if err != nil {
			t.Fatalf("drenando: %v", err)
		}
		all = append(all, got...)
		if len(got) == 0 {
			break
		}
	}
	return all
}

func TestConsumerProcessesBet(t *testing.T) {
	runner, mainURL, dlqURL, client := consumerDeps(t)
	ctx := context.Background()
	walletID, playerID := fundWallet(t, runner, "1000.00")
	msgID := "sqs-bet-" + uuid.NewString()
	ext := "sqs-ext-" + uuid.NewString()

	body := envelopeFor(t, msgID, walletID.String(), playerID.String(), ext, "provider-a:"+ext, "BET", "25.00", "")
	if _, err := client.Send(ctx, mainURL, body, walletID.String(), msgID); err != nil {
		t.Fatalf("enviando: %v", err)
	}
	runConsumer(t, runner, client, mainURL, dlqURL, client, 8)

	if balance := countBalance(t, walletID); balance != "975.00" {
		t.Fatalf("saldo = %s", balance)
	}
	// Reentrega com o mesmo conteúdo: confirma sem reexecutar (1 débito só).
	if _, err := client.Send(ctx, mainURL, body, walletID.String(), msgID+"-redelivery"); err != nil {
		t.Fatalf("reentregando: %v", err)
	}
	runConsumer(t, runner, client, mainURL, dlqURL, client, 8)
	if balance := countBalance(t, walletID); balance != "975.00" {
		t.Fatalf("reexecutou: %s", balance)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, walletID.String()); n != 2 {
		t.Fatalf("lançamentos = %d", n)
	}
}

// TestProviderQueueIsolation confirma que a fila vinculada a B não processa um envelope de A.
func TestProviderQueueIsolation(t *testing.T) {
	runner, queueURL, dlqURL, client := consumerDeps(t)
	walletID, playerID := fundWallet(t, runner, "100.00")
	validID := "provider-b-valid-" + uuid.NewString()
	wrongID := "provider-b-wrong-" + uuid.NewString()
	validExt := "provider-b-ext-" + uuid.NewString()
	wrongExt := "provider-a-ext-" + uuid.NewString()
	valid := strings.Replace(envelopeFor(t, validID, walletID.String(), playerID.String(), validExt,
		"provider-b:"+validExt, "BET", "25.00", ""), "provider-a", "provider-b", 1)
	wrong := envelopeFor(t, wrongID, walletID.String(), playerID.String(), wrongExt,
		"provider-a:"+wrongExt, "BET", "25.00", "")
	for _, message := range []struct{ body, id string }{{valid, validID}, {wrong, wrongID}} {
		if _, err := client.Send(context.Background(), queueURL, message.body, walletID.String(), message.id); err != nil {
			t.Fatalf("enviando: %v", err)
		}
	}
	consumer := infrasqs.NewProviderConsumerWithTracing(client, queueURL, dlqURL,
		"test-consumer:provider-b", "provider-b", runner, application.SystemClock{},
		application.UUIDv7Generator{}, 2, 2, nil, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := consumer.Run(ctx); err != nil {
		t.Fatalf("consumidor: %v", err)
	}
	if balance := countBalance(t, walletID); balance != "75.00" {
		t.Fatalf("saldo = %s", balance)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM wager_transactions WHERE external_transaction_id = $1`, wrongExt); n != 0 {
		t.Fatalf("transação de outro provedor persistida: %d", n)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM inbox_messages WHERE message_id = $1`, wrongID); n != 0 {
		t.Fatalf("mensagem de outro provedor na inbox: %d", n)
	}
	messages := drain(t, client, dlqURL)
	if len(messages) != 1 || !strings.Contains(messages[0].Body, wrongID) {
		t.Fatalf("DLQ = %+v", messages)
	}
}

func TestConsumerInvalidGoesToDLQ(t *testing.T) {
	runner, mainURL, dlqURL, client := consumerDeps(t)
	ctx := context.Background()
	msgID := "sqs-lixo-" + uuid.NewString()

	if _, err := client.Send(ctx, mainURL, `{"messageId":`+msgID+`}`, "grupo", msgID); err != nil {
		t.Fatalf("enviando: %v", err)
	}
	// Mesmo messageId, conteúdo diferente na reentrega: veneno.
	venomID := "sqs-veneno-" + uuid.NewString()
	first := envelopeFor(t, venomID, uuid.NewString(), uuid.NewString(), "ext-v1", "k-v1", "BET", "25.00", "")
	second := strings.Replace(first, `"amount":"25.00"`, `"amount":"30.00"`, 1)
	if _, err := client.Send(ctx, mainURL, first, "grupo", venomID); err != nil {
		t.Fatalf("enviando: %v", err)
	}
	runConsumer(t, runner, client, mainURL, dlqURL, client, 8)
	if _, err := client.Send(ctx, mainURL, second, "grupo", venomID+"-re"); err != nil {
		t.Fatalf("reentregando: %v", err)
	}
	runConsumer(t, runner, client, mainURL, dlqURL, client, 8)

	found := map[string]bool{}
	for _, m := range drain(t, client, dlqURL) {
		mine := strings.Contains(m.Body, msgID) || strings.Contains(m.Body, venomID)
		if mine && !strings.Contains(m.Body, "reason") {
			t.Fatalf("DLQ sem motivo: %s", m.Body)
		}
		if strings.Contains(m.Body, msgID) {
			found["lixo"] = true
		}
		if strings.Contains(m.Body, venomID) {
			found["veneno"] = true
		}
		if err := client.Delete(ctx, dlqURL, m.ReceiptHandle); err != nil {
			t.Fatalf("limpando DLQ: %v", err)
		}
	}
	if !found["lixo"] || !found["veneno"] {
		t.Fatalf("DLQ = %v", found)
	}
}

func TestConsumerBusinessRejectionDeletes(t *testing.T) {
	runner, mainURL, dlqURL, client := consumerDeps(t)
	ctx := context.Background()
	walletID, playerID := fundWallet(t, runner, "20.00")
	msgID := "sqs-semsaldo-" + uuid.NewString()
	ext := "sqs-ext-" + uuid.NewString()

	body := envelopeFor(t, msgID, walletID.String(), playerID.String(), ext, "provider-a:"+ext, "BET", "80.00", "")
	if _, err := client.Send(ctx, mainURL, body, walletID.String(), msgID); err != nil {
		t.Fatalf("enviando: %v", err)
	}
	runConsumer(t, runner, client, mainURL, dlqURL, client, 8)

	var status, code string
	conn := connect(t, ownerURL(t))
	if err := conn.QueryRow(ctx, `SELECT status, failure_code FROM wager_transactions WHERE external_transaction_id = $1`,
		ext).Scan(&status, &code); err != nil || status != string(wager.StatusRejected) || code != string(wager.CodeInsufficientFunds) {
		t.Fatalf("estado=%s código=%s, %v", status, code, err)
	}
	// Rejeição confirmada sai da fila sem ir para a DLQ: drena e confere que a nossa mensagem não está em nenhum dos dois lugares.
	for _, m := range drain(t, client, mainURL) {
		if strings.Contains(m.Body, msgID) {
			t.Fatal("rejeitada voltou para a entrada")
		}
		if err := client.Delete(ctx, mainURL, m.ReceiptHandle); err != nil {
			t.Fatalf("limpando: %v", err)
		}
	}
	for _, m := range drain(t, client, dlqURL) {
		if strings.Contains(m.Body, msgID) {
			t.Fatal("rejeitada foi para a DLQ")
		}
		if err := client.Delete(ctx, dlqURL, m.ReceiptHandle); err != nil {
			t.Fatalf("limpando: %v", err)
		}
	}
}

// flakyQueue falha o primeiro Delete (simula queda entre commit e confirmação).
type flakyQueue struct {
	infrasqs.QueueOps
	failOnce     bool
	failedHandle string
}

func (f *flakyQueue) Delete(ctx context.Context, url, handle string) error {
	if f.failOnce {
		f.failOnce = false
		f.failedHandle = handle
		return errors.New("queda antes do delete")
	}
	return f.QueueOps.Delete(ctx, url, handle)
}

func TestConsumerCrashAfterCommit(t *testing.T) {
	runner, mainURL, dlqURL, client := consumerDeps(t)
	ctx := context.Background()
	walletID, playerID := fundWallet(t, runner, "1000.00")
	msgID := "sqs-crash-" + uuid.NewString()
	ext := "sqs-ext-" + uuid.NewString()

	body := envelopeFor(t, msgID, walletID.String(), playerID.String(), ext, "provider-a:"+ext, "BET", "25.00", "")
	if _, err := client.Send(ctx, mainURL, body, walletID.String(), msgID); err != nil {
		t.Fatalf("enviando: %v", err)
	}
	flaky := &flakyQueue{QueueOps: client, failOnce: true}
	runConsumer(t, runner, client, mainURL, dlqURL, flaky, 8)
	if flaky.failedHandle == "" {
		t.Fatal("o delete não falhou como planejado")
	}
	// A mensagem volta (visibilidade liberada) e a retomada confirma sem reexecutar: 1 débito só.
	if err := client.Release(ctx, mainURL, flaky.failedHandle, 0); err != nil {
		t.Fatalf("soltando: %v", err)
	}
	runConsumer(t, runner, client, mainURL, dlqURL, client, 8)
	if balance := countBalance(t, walletID); balance != "975.00" {
		t.Fatalf("saldo = %s", balance)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND transaction_id IN
		(SELECT id FROM wager_transactions WHERE external_transaction_id = $2)`, walletID.String(), ext); n != 1 {
		t.Fatalf("lançamentos = %d", n)
	}
}
