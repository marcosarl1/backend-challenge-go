package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marcosarl1/backend-challenge-go/internal/platform/observability"
)

func TestRecoverMiddleware(t *testing.T) {
	srv := New(nil, nil, nil, nil)
	panicHandler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", nil)
	srv.withRecover(panicHandler).ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("código = %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("tipo = %q", rec.Header().Get("Content-Type"))
	}
}

func TestRecoverLogIncludesCorrelationWithoutPanicValue(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(observability.NewJSONHandler(&output))
	srv := NewWithLogger(nil, nil, nil, nil, logger)
	panicHandler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("Bearer private-token")
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("X-Correlation-Id", "corr-1")
	srv.withCorrelation(srv.withRecover(panicHandler)).ServeHTTP(rec, req)

	line := output.String()
	if !strings.Contains(line, `"correlationId":"corr-1"`) {
		t.Fatalf("log sem correlação: %s", line)
	}
	if strings.Contains(line, "private-token") || !strings.Contains(line, `"panic":"[REDACTED]"`) {
		t.Fatalf("log expôs o valor do pânico: %s", line)
	}
}

func TestHTTPServerTimeouts(t *testing.T) {
	srv := NewHTTPServer(http.NotFoundHandler(), "127.0.0.1:0")
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Fatalf("prazos zerados: %+v", srv)
	}
}
