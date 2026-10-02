package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marcosarl1/backend-challenge-go/internal/infra/httpapi"
	"github.com/marcosarl1/backend-challenge-go/internal/platform/observability"
)

func TestMetricsListenerUsesSeparatePort(t *testing.T) {
	runtime, app := newLifecycleTestApp(t, validRuntimeConfig(), nil, componentRunner{
		name: "worker", run: func(ctx context.Context) error { <-ctx.Done(); return nil },
	})
	runtime.admin = httpapi.NewHTTPServer(newAdminHandler(observability.NewMetrics()), "127.0.0.1:0")
	app.RequireStart()
	defer app.RequireStop()

	if runtime.listener.Addr().String() == runtime.adminListener.Addr().String() {
		t.Fatal("API e métricas compartilham a porta")
	}
	response, err := http.Get("http://" + runtime.adminListener.Addr().String() + "/metrics")
	if err != nil {
		t.Fatalf("consultando métricas: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status de /metrics = %d", response.StatusCode)
	}
}

func TestMetricsOnlyOnAdminHandler(t *testing.T) {
	metrics := observability.NewMetrics()
	admin := newAdminHandler(metrics)
	recorder := httptest.NewRecorder()
	admin.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "wager_results_total") {
		t.Fatalf("listener administrativo: status %d, body %s", recorder.Code, recorder.Body.String())
	}

	api := httpapi.New(nil, nil, nil, nil).Handler()
	recorder = httptest.NewRecorder()
	api.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code == http.StatusOK || strings.Contains(recorder.Body.String(), "wager_results_total") {
		t.Fatalf("API pública expôs métricas: status %d", recorder.Code)
	}
}
