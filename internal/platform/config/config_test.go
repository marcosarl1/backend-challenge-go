package config

import (
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		HTTPAddr: ":8081", DatabaseURL: "postgres://user:pass@localhost:5432/wagering",
		SQSEndpoint: "http://localhost:4566", SQSRegion: "us-east-1",
		OIDCIssuer:  "http://localhost:8080/realms/wagering",
		OIDCJWKSURL: "http://localhost:8080/realms/wagering/certs", OIDCAudience: "wagering-api",
		ConsumerName: "consumer", ConsumerWorkers: 1, ConsumerPoll: time.Second,
		OutboxOwner: "publisher", OutboxBatch: 1, OutboxInterval: time.Second,
		RetryBatch: 1, RetryInterval: time.Second, ShutdownTimeout: time.Second,
	}
}

func TestConfigValidate(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("configuração válida rejeitada: %v", err)
	}
}

func TestConfigValidateRejectsInvalidRuntimeValues(t *testing.T) {
	cfg := validConfig()
	cfg.HTTPAddr = "invalid"
	cfg.DatabaseURL = "not-a-database-url"
	cfg.ConsumerWorkers = 0
	cfg.ShutdownTimeout = 0

	err := cfg.Validate()
	if err == nil {
		t.Fatal("esperava erro de validação")
	}
	for _, field := range []string{"HTTP_ADDR", "DATABASE_URL", "consumidor SQS", "prazos"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("erro %q não identifica %q", err, field)
		}
	}
}

func TestLoadPreservesInvalidEnvironmentValuesForStartupValidation(t *testing.T) {
	t.Setenv("CONSUMER_WORKERS", "not-an-integer")
	if err := Load().Validate(); err == nil || !strings.Contains(err.Error(), "CONSUMER_WORKERS") {
		t.Fatalf("Load().Validate() = %v, esperado erro de variável", err)
	}
}
