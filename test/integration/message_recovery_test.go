//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wager"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/postgres"
	infrasqs "github.com/marcosarl1/backend-challenge-go/internal/infra/sqs"
	outboxworker "github.com/marcosarl1/backend-challenge-go/internal/workers/outbox"
)

const recoveryMarker = "RECOVERY_POINT:"

// crashRecoveryChild registra o ponto alcançado e derruba o processo sem executar cleanup.
func crashRecoveryChild(value string) {
	fmt.Fprintln(os.Stdout, recoveryMarker+value)
	_ = os.Stdout.Sync()
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	os.Exit(99)
}

// runRecoveryChild inicia o próprio binário de teste em outro processo e exige uma morte por SIGKILL.
func runRecoveryChild(t *testing.T, mode string, env ...string) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatalf("binário de teste: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-test.run=^TestRecoveryChildProcess$", "-test.v")
	cmd.Env = append(append(os.Environ(), "WAGER_RECOVERY_CHILD="+mode), env...)
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("filho não caiu no ponto %s: %v: %s", mode, err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if value, ok := strings.CutPrefix(line, recoveryMarker); ok {
			return strings.TrimSpace(value)
		}
	}
	t.Fatalf("filho caiu sem informar o ponto %s: %s", mode, output)
	return ""
}

// childRunner abre uma conexão independente, como a de outro processo do serviço.
func childRunner(t *testing.T) postgres.Runner {
	t.Helper()
	pool, err := postgres.Connect(context.Background(), os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("banco do filho: %v", err)
	}
	t.Cleanup(pool.Close)
	return postgres.NewRunner(postgres.NewUnitOfWork(pool))
}

// childQueue abre o mesmo emulador real usado pelo pai.
func childQueue(t *testing.T) *infrasqs.Client {
	t.Helper()
	client, err := infrasqs.NewClient(context.Background(), os.Getenv("TEST_SQS_ENDPOINT"), "us-east-1")
	if err != nil {
		t.Fatalf("fila do filho: %v", err)
	}
	return client
}

// killBeforeDelete deixa o commit terminar e mata o filho na chamada que removeria a mensagem.
type killBeforeDelete struct{ infrasqs.QueueOps }

// Delete simula uma queda abrupta no ponto em que a fila seria confirmada.
func (q killBeforeDelete) Delete(_ context.Context, _, receiptHandle string) error {
	crashRecoveryChild(receiptHandle)
	return nil
}

// TestRecoveryChildProcess executa somente no subprocesso indicado pelo pai.
func TestRecoveryChildProcess(t *testing.T) {
	mode := os.Getenv("WAGER_RECOVERY_CHILD")
	if mode == "" {
		t.Skip("auxiliar dos testes de crash")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	runner := childRunner(t)
	switch mode {
	case "before_delete":
		client := childQueue(t)
		consumer := infrasqs.NewConsumer(killBeforeDelete{QueueOps: client},
			os.Getenv("WAGER_RECOVERY_MAIN_QUEUE"), os.Getenv("WAGER_RECOVERY_DLQ_QUEUE"),
			"test-consumer", runner, application.SystemClock{}, application.UUIDv7Generator{}, 1, 1)
		if err := consumer.Run(ctx); err != nil {
			t.Fatalf("consumidor do filho: %v", err)
		}
	case "after_commit":
		walletID, err := uuid.Parse(os.Getenv("WAGER_RECOVERY_WALLET_ID"))
		if err != nil {
			t.Fatal(err)
		}
		playerID, err := uuid.Parse(os.Getenv("WAGER_RECOVERY_PLAYER_ID"))
		if err != nil {
			t.Fatal(err)
		}
		amount, err := money.Parse("25.00", "BRL")
		if err != nil {
			t.Fatal(err)
		}
		cmd := application.ProcessCommand{
			ProviderID: "provider-a", ExternalID: os.Getenv("WAGER_RECOVERY_EXTERNAL_ID"),
			IdempotencyKey: os.Getenv("WAGER_RECOVERY_KEY"), PlayerID: playerID, WalletID: walletID,
			RoundID: "round-1", GameID: "jogo", Kind: wager.KindBet,
			Amount: amount, CorrelationID: os.Getenv("WAGER_RECOVERY_CORRELATION"),
		}
		result, err := application.Execute(ctx, runner, application.SystemClock{}, application.UUIDv7Generator{}, providerIdent("provider-a"), cmd)
		if err != nil || result == nil || result.Status != wager.StatusProcessed {
			t.Fatalf("operação do filho: %+v, %v", result, err)
		}
		crashRecoveryChild(result.TransactionID.String())
	case "after_publish":
		client := childQueue(t)
		publisher := outboxworker.NewPublisher(outboxworker.ClientSender{Client: client},
			os.Getenv("WAGER_RECOVERY_EVENTS_QUEUE"), runner, application.SystemClock{}, "recovery-child")
		publisher.AfterPublish = func(id uuid.UUID) error {
			crashRecoveryChild(id.String())
			return nil
		}
		if _, err := publisher.RunOnce(ctx); err != nil {
			t.Fatalf("publicador do filho: %v", err)
		}
	default:
		t.Fatalf("modo de crash desconhecido: %q", mode)
	}
	t.Fatalf("filho saiu sem atingir o ponto de crash %s", mode)
}

// TestRecoveryAfterCommitBeforeDelete mata o consumidor depois do commit e confirma a reentrega sem segundo débito.
func TestRecoveryAfterCommitBeforeDelete(t *testing.T) {
	runner, mainURL, dlqURL, client := consumerDeps(t)
	ctx := context.Background()
	walletID, playerID := fundWallet(t, runner, "1000.00")
	messageID := "recovery-" + uuid.NewString()
	externalID := "recovery-ext-" + uuid.NewString()
	body := envelopeFor(t, messageID, walletID.String(), playerID.String(), externalID,
		"provider-a:"+externalID, "BET", "25.00", "")
	if _, err := client.Send(ctx, mainURL, body, walletID.String(), messageID); err != nil {
		t.Fatalf("enviando: %v", err)
	}
	receipt := runRecoveryChild(t, "before_delete", "WAGER_RECOVERY_MAIN_QUEUE="+mainURL,
		"WAGER_RECOVERY_DLQ_QUEUE="+dlqURL)
	if balance := countBalance(t, walletID); balance != "975.00" {
		t.Fatalf("commit do filho não persistiu: saldo=%s", balance)
	}
	if count := countRows(t, `SELECT COUNT(*) FROM inbox_messages WHERE consumer_name = 'test-consumer' AND message_id = $1`, messageID); count != 1 {
		t.Fatalf("inbox depois do crash = %d", count)
	}
	if err := client.Release(ctx, mainURL, receipt, 0); err != nil {
		t.Fatalf("liberando mensagem após crash: %v", err)
	}
	runConsumer(t, runner, client, mainURL, dlqURL, client, 8)
	if count := countRows(t, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND transaction_id IN
		(SELECT id FROM wager_transactions WHERE external_transaction_id = $2)`, walletID, externalID); count != 1 {
		t.Fatalf("débitos após reentrega = %d", count)
	}
	if count := countRows(t, `SELECT COUNT(*) FROM inbox_messages WHERE consumer_name = 'test-consumer' AND message_id = $1`, messageID); count != 1 {
		t.Fatalf("inbox após reentrega = %d", count)
	}
	if balance := countBalance(t, walletID); balance != "975.00" {
		t.Fatalf("saldo após reentrega = %s", balance)
	}
	if received, err := client.Receive(ctx, mainURL, 1, 2); err != nil || len(received) != 0 {
		t.Fatalf("mensagem não foi confirmada: %+v, %v", received, err)
	}
}

// recoveryEventID lê o identificador estável do evento publicado na fila.
func recoveryEventID(t *testing.T, body string) string {
	t.Helper()
	var event struct {
		EventID string `json:"eventId"`
	}
	if err := json.Unmarshal([]byte(body), &event); err != nil || event.EventID == "" {
		t.Fatalf("evento sem id: %s, %v", body, err)
	}
	return event.EventID
}

// receiveRecoveryEvents consome exatamente os eventos esperados de uma fila isolada.
func receiveRecoveryEvents(t *testing.T, client *infrasqs.Client, url string, want int) map[string]bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	seen := make(map[string]bool, want)
	for len(seen) < want && ctx.Err() == nil {
		messages, err := client.Receive(ctx, url, 10, 2)
		if err != nil {
			t.Fatalf("recebendo eventos: %v", err)
		}
		for _, message := range messages {
			id := recoveryEventID(t, message.Body)
			if seen[id] {
				t.Fatalf("evento duplicado na fila: %s", id)
			}
			seen[id] = true
			if err := client.Delete(ctx, url, message.ReceiptHandle); err != nil {
				t.Fatalf("confirmando evento: %v", err)
			}
		}
	}
	if len(seen) != want {
		t.Fatalf("eventos recebidos=%d, esperados=%d", len(seen), want)
	}
	return seen
}

// TestRecoveryAfterCommitBeforePublish mata o processo logo após confirmar a aposta e retoma a outbox em outro publicador.
func TestRecoveryAfterCommitBeforePublish(t *testing.T) {
	runner := openRunner(t)
	walletID, playerID := fundWallet(t, runner, "100.00")
	cleanPendingOutbox(t)
	externalID := "commit-publish-" + uuid.NewString()
	correlation := "corr-" + externalID
	transactionID := runRecoveryChild(t, "after_commit",
		"WAGER_RECOVERY_WALLET_ID="+walletID.String(),
		"WAGER_RECOVERY_PLAYER_ID="+playerID.String(),
		"WAGER_RECOVERY_EXTERNAL_ID="+externalID,
		"WAGER_RECOVERY_KEY=provider-a:"+externalID,
		"WAGER_RECOVERY_CORRELATION="+correlation)
	if balance := countBalance(t, walletID); balance != "75.00" {
		t.Fatalf("saldo após commit = %s", balance)
	}
	if count := countRows(t, `SELECT COUNT(*) FROM wallet_ledger_entries WHERE transaction_id = $1`, transactionID); count != 1 {
		t.Fatalf("débito após commit = %d", count)
	}
	if count := countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE correlation_id = $1 AND published_at IS NULL`, correlation); count != 2 {
		t.Fatalf("eventos duráveis não publicados = %d", count)
	}
	queueURL := isolatedQueue(t, "recovery-commit-publish")
	client, err := infrasqs.NewClient(context.Background(), sqsURL(t), "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	publisher := outboxworker.NewPublisher(outboxworker.ClientSender{Client: client}, queueURL,
		openRunner(t), application.SystemClock{}, "recovery-after-commit")
	if n, err := publisher.RunOnce(context.Background()); err != nil || n != 2 {
		t.Fatalf("retomando outbox: publicados=%d, erro=%v", n, err)
	}
	if count := countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE correlation_id = $1 AND published_at IS NOT NULL`, correlation); count != 2 {
		t.Fatalf("eventos confirmados = %d", count)
	}
	if seen := receiveRecoveryEvents(t, client, queueURL, 2); len(seen) != 2 {
		t.Fatalf("eventos entregues = %d", len(seen))
	}
}

// TestRecoveryAfterPublishBeforeMark mata o publicador depois do envio e verifica a republicação com o mesmo eventId.
func TestRecoveryAfterPublishBeforeMark(t *testing.T) {
	cleanPendingOutbox(t)
	runner := openRunner(t)
	walletID, _ := fundWallet(t, runner, "100.00")
	queueURL := isolatedQueue(t, "recovery-publish-mark")
	firstID := runRecoveryChild(t, "after_publish", "WAGER_RECOVERY_EVENTS_QUEUE="+queueURL)
	if _, err := uuid.Parse(firstID); err != nil {
		t.Fatalf("eventId do filho: %q, %v", firstID, err)
	}
	conn := connect(t, ownerURL(t))
	ctx := context.Background()
	var attempts int
	var published bool
	if err := conn.QueryRow(ctx, `SELECT attempts, published_at IS NOT NULL FROM outbox_events WHERE id = $1`, firstID).
		Scan(&attempts, &published); err != nil || attempts != 1 || published {
		t.Fatalf("evento após crash: tentativas=%d, publicado=%v, erro=%v", attempts, published, err)
	}
	client, err := infrasqs.NewClient(ctx, sqsURL(t), "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	messages, err := client.Receive(ctx, queueURL, 10, 2)
	if err != nil || len(messages) != 1 || recoveryEventID(t, messages[0].Body) != firstID {
		t.Fatalf("primeiro envio não chegou: %+v, %v", messages, err)
	}
	if err := client.Delete(ctx, queueURL, messages[0].ReceiptHandle); err != nil {
		t.Fatal(err)
	}
	// O lote inteiro foi reservado antes da queda; a expiração substitui a espera de 30 segundos para os dois eventos.
	tag, err := conn.Exec(ctx, `UPDATE outbox_events SET lease_until = now() - interval '1 second'
		WHERE ordering_key = $1 AND lease_owner = 'recovery-child' AND published_at IS NULL`, walletID.String())
	if err != nil || tag.RowsAffected() != 2 {
		t.Fatalf("expirando lote: linhas=%d, erro=%v", tag.RowsAffected(), err)
	}
	publisher := outboxworker.NewPublisher(outboxworker.ClientSender{Client: client}, queueURL,
		openRunner(t), application.SystemClock{}, "recovery-after-publish")
	if n, err := publisher.RunOnce(ctx); err != nil || n != 2 {
		t.Fatalf("republicando: publicados=%d, erro=%v", n, err)
	}
	if err := conn.QueryRow(ctx, `SELECT attempts, published_at IS NOT NULL FROM outbox_events WHERE id = $1`, firstID).
		Scan(&attempts, &published); err != nil || attempts != 2 || !published {
		t.Fatalf("evento retomado: tentativas=%d, publicado=%v, erro=%v", attempts, published, err)
	}
	if count := countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE ordering_key = $1 AND published_at IS NOT NULL`, walletID.String()); count != 2 {
		t.Fatalf("eventos confirmados da carteira = %d", count)
	}
	// A fila pode suprimir ou entregar de novo o primeiro id; o segundo evento precisa chegar em ambos os casos.
	receiveCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	secondSeen := false
	for !secondSeen && receiveCtx.Err() == nil {
		messages, err := client.Receive(receiveCtx, queueURL, 10, 2)
		if err != nil {
			t.Fatalf("recebendo retomada: %v", err)
		}
		for _, message := range messages {
			if id := recoveryEventID(t, message.Body); id != firstID {
				secondSeen = true
			}
			if err := client.Delete(receiveCtx, queueURL, message.ReceiptHandle); err != nil {
				t.Fatalf("confirmando retomada: %v", err)
			}
		}
	}
	if !secondSeen {
		t.Fatal("segundo evento não chegou após a retomada")
	}
}

// TestTwoPublishersWithRealQueue verifica a disputa entre donos independentes sobre PostgreSQL e SQS reais.
func TestTwoPublishersWithRealQueue(t *testing.T) {
	cleanPendingOutbox(t)
	runner := openRunner(t)
	walletID, playerID := fundWallet(t, runner, "100.00")
	for range 6 {
		runProcess(t, runner, processCmd(t, walletID, playerID, wager.KindBet, "1.00"))
	}
	queueURL := isolatedQueue(t, "recovery-two-publishers")
	client, err := infrasqs.NewClient(context.Background(), sqsURL(t), "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	publishers := []*outboxworker.Publisher{
		outboxworker.NewPublisher(outboxworker.ClientSender{Client: client}, queueURL, openRunner(t), application.SystemClock{}, "recovery-pub-a"),
		outboxworker.NewPublisher(outboxworker.ClientSender{Client: client}, queueURL, openRunner(t), application.SystemClock{}, "recovery-pub-b"),
	}
	var wg sync.WaitGroup
	var counts [2]int
	var errs [2]error
	start := make(chan struct{})
	for i, publisher := range publishers {
		wg.Add(1)
		go func(i int, publisher *outboxworker.Publisher) {
			defer wg.Done()
			<-start
			counts[i], errs[i] = publisher.RunOnce(context.Background())
		}(i, publisher)
	}
	close(start)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || counts[0] == 0 || counts[1] == 0 || counts[0]+counts[1] != 14 {
		t.Fatalf("publicadores: lotes=%v, erros=%v", counts, errs)
	}
	if count := countRows(t, `SELECT COUNT(*) FROM outbox_events WHERE ordering_key = $1 AND published_at IS NOT NULL AND attempts = 1`, walletID.String()); count != 14 {
		t.Fatalf("eventos publicados uma vez = %d", count)
	}
	if seen := receiveRecoveryEvents(t, client, queueURL, 14); len(seen) != 14 {
		t.Fatalf("eventos na fila = %d", len(seen))
	}
}
