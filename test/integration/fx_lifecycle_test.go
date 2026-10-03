//go:build integration

package integration

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/marcosarl1/backend-challenge-go/internal/infra/sqs"
	"github.com/marcosarl1/backend-challenge-go/internal/platform"
	"github.com/marcosarl1/backend-challenge-go/internal/workers/outbox"
	"github.com/marcosarl1/backend-challenge-go/internal/workers/pending"
)

// freeLifecycleAddr reserva uma porta livre para o listener gerenciado pelo Fx.
func freeLifecycleAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// TestFxModuleStartsAndStopsRealComponents verifica o grafo completo e os recursos liberados no Stop.
func TestFxModuleStartsAndStopsRealComponents(t *testing.T) {
	httpAddr := freeLifecycleAddr(t)
	metricsAddr := freeLifecycleAddr(t)
	for metricsAddr == httpAddr {
		metricsAddr = freeLifecycleAddr(t)
	}
	t.Setenv("HTTP_ADDR", httpAddr)
	t.Setenv("METRICS_ADDR", metricsAddr)
	t.Setenv("DATABASE_URL", ownerURL(t))
	t.Setenv("SQS_ENDPOINT", sqsURL(t))
	t.Setenv("SQS_REGION", "us-east-1")
	t.Setenv("OIDC_ISSUER", keycloakURL(t)+"/realms/wagering")
	t.Setenv("OIDC_JWKS_URL", keycloakURL(t)+"/realms/wagering/protocol/openid-connect/certs")
	t.Setenv("OIDC_AUDIENCE", "wagering-api")
	t.Setenv("CONSUMER_POLL", "1s")
	t.Setenv("OUTBOX_OWNER", "fx-lifecycle")
	t.Setenv("SHUTDOWN_TIMEOUT", "5s")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	var pool *pgxpool.Pool
	var consumer *sqs.Consumer
	var publisher *outbox.Publisher
	var retry *pending.Worker
	app := fx.New(platform.Module, fx.NopLogger, fx.Populate(&pool, &consumer, &publisher, &retry))
	if err := app.Err(); err != nil {
		t.Fatalf("composição Fx: %v", err)
	}
	if pool == nil || consumer == nil || publisher == nil || retry == nil {
		t.Fatal("grafo Fx sem algum componente gerenciado")
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := app.Stop(ctx); err != nil {
				t.Errorf("encerrando Fx no cleanup: %v", err)
			}
		}
	})
	startCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.Start(startCtx); err != nil {
		t.Fatalf("iniciando Fx: %v", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	for _, endpoint := range []string{"http://" + httpAddr + "/health/ready", "http://" + metricsAddr + "/metrics"} {
		response, err := client.Get(endpoint)
		if err != nil {
			t.Fatalf("listener não iniciou em %s: %v", endpoint, err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("listener %s retornou %d", endpoint, response.StatusCode)
		}
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	if err := app.Stop(stopCtx); err != nil {
		t.Fatalf("encerrando Fx e workers: %v", err)
	}
	stopped = true
	pingCtx, pingCancel := context.WithTimeout(context.Background(), time.Second)
	defer pingCancel()
	if err := pool.Ping(pingCtx); err == nil {
		t.Fatal("pool PostgreSQL continuou aberto após Stop")
	}
	for _, addr := range []string{httpAddr, metricsAddr} {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			conn.Close()
			t.Fatalf("listener %s continuou aberto após Stop", addr)
		}
	}
}
