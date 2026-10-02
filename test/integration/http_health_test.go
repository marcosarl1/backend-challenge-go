package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/auth"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/httpapi"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/postgres"
	infrasqs "github.com/marcosarl1/backend-challenge-go/internal/infra/sqs"
)

func sqsURL(t *testing.T) string {
	t.Helper()
	if url := os.Getenv("TEST_SQS_ENDPOINT"); url != "" {
		return url
	}
	t.Skip("TEST_SQS_ENDPOINT ausente; pulei o teste contra fila de verdade")
	return ""
}

func healthServer(t *testing.T, pgURL, sqsEndpoint string) *httptest.Server {
	t.Helper()
	base := keycloakURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgURL)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	runner := postgres.NewRunner(postgres.NewUnitOfWork(pool))
	verifier, err := auth.NewVerifier(ctx, base+"/realms/wagering",
		base+"/realms/wagering/protocol/openid-connect/certs", "wagering-api")
	if err != nil {
		t.Fatalf("verificador: %v", err)
	}
	sqsClient, err := infrasqs.NewClient(ctx, sqsEndpoint, "us-east-1")
	if err != nil {
		t.Fatalf("fila: %v", err)
	}
	srv := httptest.NewServer(httpapi.New(runner, verifier,
		application.SystemClock{}, application.UUIDv7Generator{},
		postgres.NewPingChecker(pool), sqsClient).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func TestHealth(t *testing.T) {
	srv := healthServer(t, ownerURL(t), sqsURL(t))

	// Vivo sem auth e com correlação de volta.
	resp, err := http.Get(srv.URL + "/health/live")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Correlation-Id") == "" {
		t.Fatalf("live = %d", resp.StatusCode)
	}

	// Pronto com tudo de pé.
	resp, err = http.Get(srv.URL + "/health/ready")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ready = %d", resp.StatusCode)
	}
}

func TestHealthFailingAndRecovering(t *testing.T) {
	keycloakURL(t)
	// Tudo fora: 503 dizendo quem falhou.
	down := healthServer(t, "postgres://wagering:wagering@localhost:54329/wagering?sslmode=disable", "http://localhost:45659")
	resp, err := http.Get(down.URL + "/health/ready")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ready com tudo fora = %d", resp.StatusCode)
	}
	// Recuperou: 200 de novo (mesmo processo, cheques bons).
	up := healthServer(t, ownerURL(t), sqsURL(t))
	resp, err = http.Get(up.URL + "/health/ready")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ready recuperado = %d", resp.StatusCode)
	}
}
