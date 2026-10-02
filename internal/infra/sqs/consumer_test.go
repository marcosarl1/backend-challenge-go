package sqs

import (
	"errors"
	"testing"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
)

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

func TestParseEnvelope(t *testing.T) {
	raw := `{"messageId":"m-1","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z",` +
		`"data":{"providerId":"provider-a","externalTransactionId":"t-1","idempotencyKey":"k-1",` +
		`"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37",` +
		`"roundId":"r-1","gameId":"jogo","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}}`
	cmd, hash, err := parseEnvelope([]byte(raw))
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
