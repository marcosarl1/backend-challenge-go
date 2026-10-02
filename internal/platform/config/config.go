package config

import (
	"os"
	"strconv"
	"time"

	"go.uber.org/fx"
)

// Module monta a configuração a partir do ambiente.
var Module = fx.Module("config", fx.Provide(Load))

// Config carrega tudo do ambiente, com padrões para o compose local.
type Config struct {
	HTTPAddr        string
	DatabaseURL     string
	SQSEndpoint     string
	SQSRegion       string
	OIDCIssuer      string
	OIDCJWKSURL     string
	OIDCAudience    string
	ConsumerName    string
	ConsumerWorkers int
	ConsumerPoll    time.Duration
	OutboxOwner     string
	OutboxBatch     int
	OutboxInterval  time.Duration
	RetryBatch      int
	RetryInterval   time.Duration
	ShutdownTimeout time.Duration
}

// Load lê o ambiente (padrões = compose local).
func Load() Config {
	return Config{
		HTTPAddr:        env("HTTP_ADDR", ":8081"),
		DatabaseURL:     env("DATABASE_URL", "postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable"),
		SQSEndpoint:     env("SQS_ENDPOINT", "http://localhost:4566"),
		SQSRegion:       env("SQS_REGION", "us-east-1"),
		OIDCIssuer:      env("OIDC_ISSUER", "http://localhost:8080/realms/wagering"),
		OIDCJWKSURL:     env("OIDC_JWKS_URL", "http://localhost:8080/realms/wagering/protocol/openid-connect/certs"),
		OIDCAudience:    env("OIDC_AUDIENCE", "wagering-api"),
		ConsumerName:    env("CONSUMER_NAME", "wager-consumer"),
		ConsumerWorkers: envInt("CONSUMER_WORKERS", 4),
		ConsumerPoll:    envDuration("CONSUMER_POLL", 20*time.Second),
		OutboxOwner:     env("OUTBOX_OWNER", "outbox-1"),
		OutboxBatch:     envInt("OUTBOX_BATCH", 10),
		OutboxInterval:  envDuration("OUTBOX_INTERVAL", time.Second),
		RetryBatch:      envInt("RETRY_BATCH", 10),
		RetryInterval:   envDuration("RETRY_INTERVAL", 5*time.Second),
		ShutdownTimeout: envDuration("SHUTDOWN_TIMEOUT", 15*time.Second),
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if value := os.Getenv(key); value != "" {
		if n, err := strconv.Atoi(value); err == nil {
			return n
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if d, err := time.ParseDuration(value); err == nil {
			return d
		}
	}
	return fallback
}
