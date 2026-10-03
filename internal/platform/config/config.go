package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
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
	MetricsAddr     string
	OTLPEndpoint    string
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
	loadErr         error
}

// Load lê o ambiente (padrões = compose local).
func Load() Config {
	return Config{
		HTTPAddr:        env("HTTP_ADDR", ":8081"),
		MetricsAddr:     env("METRICS_ADDR", "127.0.0.1:9090"),
		OTLPEndpoint:    env("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		DatabaseURL:     env("DATABASE_URL", "postgres://app:app@localhost:5432/wagering?sslmode=disable"),
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
		loadErr: errors.Join(
			invalidIntEnv("CONSUMER_WORKERS"), invalidDurationEnv("CONSUMER_POLL"),
			invalidIntEnv("OUTBOX_BATCH"), invalidDurationEnv("OUTBOX_INTERVAL"),
			invalidIntEnv("RETRY_BATCH"), invalidDurationEnv("RETRY_INTERVAL"),
			invalidDurationEnv("SHUTDOWN_TIMEOUT"),
		),
	}
}

// Validate confere os valores que controlam inicialização e encerramento.
func (c Config) Validate() error {
	var invalid []error
	if c.loadErr != nil {
		invalid = append(invalid, c.loadErr)
	}
	if _, _, err := net.SplitHostPort(c.HTTPAddr); err != nil {
		invalid = append(invalid, fmt.Errorf("HTTP_ADDR inválido: %w", err))
	}
	if c.MetricsAddr != "" {
		if _, _, err := net.SplitHostPort(c.MetricsAddr); err != nil {
			invalid = append(invalid, fmt.Errorf("METRICS_ADDR inválido: %w", err))
		} else if c.MetricsAddr == c.HTTPAddr {
			invalid = append(invalid, errors.New("METRICS_ADDR deve usar porta separada de HTTP_ADDR"))
		}
	}
	if c.OTLPEndpoint != "" && !validURL(c.OTLPEndpoint) {
		invalid = append(invalid, errors.New("OTEL_EXPORTER_OTLP_ENDPOINT deve ser URL HTTP ou HTTPS válida"))
	}
	if parsed, err := url.Parse(c.DatabaseURL); err != nil || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" || parsed.Host == "" {
		invalid = append(invalid, errors.New("DATABASE_URL deve ser uma URL PostgreSQL válida"))
	}
	if !validURL(c.SQSEndpoint) {
		invalid = append(invalid, errors.New("SQS_ENDPOINT deve ser uma URL HTTP ou HTTPS válida"))
	}
	if c.SQSRegion == "" || !validURL(c.OIDCIssuer) || !validURL(c.OIDCJWKSURL) || c.OIDCAudience == "" {
		invalid = append(invalid, errors.New("região SQS e configuração OIDC são obrigatórias"))
	}
	if c.ConsumerName == "" || c.ConsumerWorkers < 1 || c.ConsumerPoll <= 0 {
		invalid = append(invalid, errors.New("configuração do consumidor SQS inválida"))
	}
	if c.OutboxOwner == "" || c.OutboxBatch < 1 || c.OutboxInterval <= 0 {
		invalid = append(invalid, errors.New("configuração do publicador da outbox inválida"))
	}
	if c.RetryBatch < 1 || c.RetryInterval <= 0 || c.ShutdownTimeout <= 0 {
		invalid = append(invalid, errors.New("lotes e prazos de workers devem ser positivos"))
	}
	return errors.Join(invalid...)
}

// invalidIntEnv identifica uma variável inteira definida com formato inválido.
func invalidIntEnv(key string) error {
	value, exists := os.LookupEnv(key)
	if !exists || value == "" {
		return nil
	}
	if _, err := strconv.Atoi(value); err != nil {
		return fmt.Errorf("%s inválido: %w", key, err)
	}
	return nil
}

// invalidDurationEnv identifica uma variável de duração definida com formato inválido.
func invalidDurationEnv(key string) error {
	value, exists := os.LookupEnv(key)
	if !exists || value == "" {
		return nil
	}
	if _, err := time.ParseDuration(value); err != nil {
		return fmt.Errorf("%s inválido: %w", key, err)
	}
	return nil
}

// validURL aceita somente URLs HTTP(S) com host.
func validURL(raw string) bool {
	parsed, err := url.ParseRequestURI(raw)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
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
