package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
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

func TestHTTPServerTimeouts(t *testing.T) {
	srv := NewHTTPServer(http.NotFoundHandler(), "127.0.0.1:0")
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Fatalf("prazos zerados: %+v", srv)
	}
}
