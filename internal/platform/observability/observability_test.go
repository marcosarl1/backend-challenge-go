package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestJSONHandlerAddsContextAndRedactsSecrets(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(NewJSONHandler(&output)).With("clientSecret", "fixed-secret")
	ctx := WithFields(context.Background(),
		slog.String("correlationId", "corr-1"),
		slog.String("providerId", "provider-a"),
		slog.String("walletId", "wallet-1"),
		slog.String("transactionId", "tx-1"),
		slog.String("messageId", "msg-1"),
		slog.String("idempotencyKey", "hidden-key"),
	)
	logger.InfoContext(ctx, "request processed",
		"authorization", "Bearer top-secret",
		"payload", `{"amount":"999.00"}`,
		"money", "999.00 BRL",
		"status", "PROCESSED",
	)

	line := output.String()
	for _, secret := range []string{"top-secret", "fixed-secret", "hidden-key", "999.00", "Bearer"} {
		if strings.Contains(line, secret) {
			t.Fatalf("log contém dado sensível %q: %s", secret, line)
		}
	}
	var fields map[string]any
	if err := json.Unmarshal(output.Bytes(), &fields); err != nil {
		t.Fatalf("JSON inválido: %v", err)
	}
	for key, want := range map[string]string{
		"correlationId": "corr-1", "providerId": "provider-a", "walletId": "wallet-1",
		"transactionId": "tx-1", "messageId": "msg-1", "status": "PROCESSED",
	} {
		if fields[key] != want {
			t.Errorf("%s = %v, queria %q", key, fields[key], want)
		}
	}
	for _, key := range []string{"authorization", "payload", "money", "clientSecret", "idempotencyKey"} {
		if fields[key] != "[REDACTED]" {
			t.Errorf("%s = %v, esperava redação", key, fields[key])
		}
	}
}

func TestJSONHandlerRedactsNestedFields(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(NewJSONHandler(&output))
	logger.Info("request", slog.Group("request", "password", "private", "path", "/health/live"))
	if strings.Contains(output.String(), "private") {
		t.Fatalf("log contém segredo: %s", output.String())
	}
	if !strings.Contains(output.String(), `"path":"/health/live"`) {
		t.Fatalf("campo não sensível foi removido: %s", output.String())
	}
}
