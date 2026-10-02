package sqs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/platform/observability"
)

const validEnvelope = `{"messageId":"m-1","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z",` +
	`"data":{"providerId":"provider-a","externalTransactionId":"t-1","idempotencyKey":"k-1",` +
	`"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37",` +
	`"roundId":"r-1","gameId":"jogo","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}}`

func TestBackoffDelay(t *testing.T) {
	if got := backoffDelay(1); got != 1 {
		t.Fatalf("1 = %d", got)
	}
	if got := backoffDelay(3); got != 4 {
		t.Fatalf("3 = %d", got)
	}
	if got := backoffDelay(20); got != 60 {
		t.Fatalf("teto = %d", got)
	}
	if got := backoffDelay(0); got != 1 {
		t.Fatalf("zero = %d", got)
	}
}

type releaseQueue struct {
	ctx   context.Context
	err   error
	delay int
}

func (q *releaseQueue) Receive(context.Context, string, int, int) ([]Received, error) {
	return nil, nil
}
func (q *releaseQueue) Delete(context.Context, string, string) error { return nil }
func (q *releaseQueue) Release(ctx context.Context, _, _ string, delay int) error {
	q.ctx, q.err, q.delay = ctx, ctx.Err(), delay
	return nil
}
func (q *releaseQueue) SendDLQ(context.Context, string, string, string, string) error { return nil }

func TestReleaseUsesIndependentContextAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	queues := &releaseQueue{}
	consumer := &Consumer{queues: queues, queueURL: "queue"}
	if err := consumer.release(ctx, Received{ReceiptHandle: "receipt"}, 0); err != nil {
		t.Fatalf("release() = %v", err)
	}
	if queues.err != nil {
		t.Fatalf("contexto de liberação cancelado: %v", queues.err)
	}
	if queues.delay != 0 {
		t.Fatalf("atraso = %d, esperado 0", queues.delay)
	}
}

func TestParseEnvelope(t *testing.T) {
	cmd, hash, err := parseEnvelope([]byte(validEnvelope))
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if cmd.MessageID != "m-1" || cmd.Command.CorrelationID != "m-1" || len(hash) != 32 {
		t.Fatalf("parse = %+v", cmd)
	}
	for name, body := range map[string]string{
		"lixo":          `não-json`,
		"tipo errado":   `{"messageId":"m","type":"Outro","occurredAt":"2026-09-08T12:00:00Z","data":{}}`,
		"sem messageId": `{"type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z","data":{}}`,
		"data ruim":     `{"messageId":"m","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z","data":{"providerId":"a"}}`,
		"campo a mais":  `{"messageId":"m","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z","data":{},"extra":1}`,
		"dinheiro ruim": `{"messageId":"m","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z","data":{"providerId":"a","externalTransactionId":"t","idempotencyKey":"k","playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37","roundId":"r","gameId":"g","kind":"BET","money":{"amount":"25.0","currency":"BRL"}}}`,
		"uuid ruim":     `{"messageId":"m","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z","data":{"providerId":"a","externalTransactionId":"t","idempotencyKey":"k","playerId":"x","walletId":"y","roundId":"r","gameId":"g","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}}`,
	} {
		if _, _, err := parseEnvelope([]byte(body)); !errors.Is(err, application.ErrInvalidInput) {
			t.Fatalf("%s erro = %v", name, err)
		}
	}
}

type failingUnitOfWork struct{}

func (failingUnitOfWork) Do(context.Context, func(context.Context, application.Repositories) error) error {
	return errors.New("banco indisponível")
}

func TestConsumerRetryLogOmitsMessageBody(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(observability.NewJSONHandler(&output))
	consumer := NewConsumerWithLogger(&releaseQueue{}, "queue", "dlq", "consumer", failingUnitOfWork{}, nil, nil, 1, 1, logger)
	consumer.handle(context.Background(), Received{MessageID: "sqs-1", ReceiptHandle: "receipt", Body: validEnvelope, ReceiveCount: 1})

	line := output.String()
	for _, value := range []string{`"messageId":"m-1"`, `"correlationId":"m-1"`, `"providerId":"provider-a"`, `"walletId":"0192f291-27dd-7d3f-8071-5f8685deef37"`} {
		if !strings.Contains(line, value) {
			t.Errorf("log sem %s: %s", value, line)
		}
	}
	for _, sensitive := range []string{`"body"`, `"money"`, `"amount"`, "25.00", "k-1"} {
		if strings.Contains(line, sensitive) {
			t.Errorf("log contém dado sensível %s: %s", sensitive, line)
		}
	}
}

func TestConsumerDLQLogOmitsInvalidBody(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(observability.NewJSONHandler(&output))
	consumer := NewConsumerWithLogger(&releaseQueue{}, "queue", "dlq", "consumer", nil, nil, nil, 1, 1, logger)
	consumer.handle(context.Background(), Received{MessageID: "sqs-1", ReceiptHandle: "receipt", Body: `Bearer private-token {"amount":"25.00"}`})

	line := output.String()
	if !strings.Contains(line, `"messageId":"sqs-1"`) {
		t.Fatalf("log sem messageId: %s", line)
	}
	for _, sensitive := range []string{"private-token", "25.00", `"body"`} {
		if strings.Contains(line, sensitive) {
			t.Errorf("log contém dado sensível %s: %s", sensitive, line)
		}
	}
}

func TestIsPermanent(t *testing.T) {
	if !isPermanent(application.ErrIdempotencyMismatch) {
		t.Fatal("mismatch deveria ser permanente")
	}
	if !isPermanent(application.ErrNotFound) {
		t.Fatal("não-encontrado deveria ser permanente")
	}
	if isPermanent(errors.New("banco caiu")) {
		t.Fatal("desconhecido deveria repetir")
	}
}
