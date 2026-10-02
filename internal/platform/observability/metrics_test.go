package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsHandlerExposesAllSeries(t *testing.T) {
	metrics := NewMetrics()
	metrics.Result("http", "PROCESSED")
	metrics.Duplicate("http")
	metrics.Retry("sqs")
	metrics.DLQ()
	metrics.Conflict()
	metrics.OutboxDelay(2 * time.Second)
	metrics.Latency("http_process", time.Second)
	metrics.Divergence()

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, esperado 200", recorder.Code)
	}
	for _, series := range []string{
		`wager_results_total{source="http",status="PROCESSED"} 1`,
		`wager_duplicates_total{source="http"} 1`,
		`wager_retries_total{source="sqs"} 1`,
		"wager_dlq_total 1", "wager_concurrency_conflicts_total 1",
		"wager_outbox_delay_seconds_count 1", "wager_latency_seconds_count{operation=\"http_process\"} 1",
		"wager_reconciliation_divergences_total 1",
	} {
		if !strings.Contains(recorder.Body.String(), series) {
			t.Errorf("/metrics sem %s", series)
		}
	}
}

func TestMetricsRegistriesAreIndependent(t *testing.T) {
	first, second := NewMetrics(), NewMetrics()
	first.DLQ()
	recorder := httptest.NewRecorder()
	second.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(recorder.Body.String(), "wager_dlq_total 0") {
		t.Fatalf("registro compartilhou contador entre instâncias: %s", recorder.Body.String())
	}
}
