package observability

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics reúne séries de baixa cardinalidade; cada instância mantém seu próprio registro.
type Metrics struct {
	registry    *prometheus.Registry
	results     *prometheus.CounterVec
	duplicates  *prometheus.CounterVec
	retries     *prometheus.CounterVec
	dlq         prometheus.Counter
	conflicts   prometheus.Counter
	outboxDelay prometheus.Histogram
	latency     *prometheus.HistogramVec
	divergences prometheus.Counter
}

// NewMetrics registra as séries da aplicação sem usar o registro global do Prometheus.
func NewMetrics() *Metrics {
	m := &Metrics{registry: prometheus.NewRegistry()}
	m.results = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "wager_results_total", Help: "Operações concluídas por origem e status."}, []string{"source", "status"})
	m.duplicates = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "wager_duplicates_total", Help: "Operações repetidas sem novo efeito financeiro."}, []string{"source"})
	m.retries = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "wager_retries_total", Help: "Tentativas remarcadas por origem."}, []string{"source"})
	m.dlq = prometheus.NewCounter(prometheus.CounterOpts{Name: "wager_dlq_total", Help: "Mensagens enviadas à DLQ."})
	m.conflicts = prometheus.NewCounter(prometheus.CounterOpts{Name: "wager_concurrency_conflicts_total", Help: "Conflitos de serialização ou deadlock no PostgreSQL."})
	m.outboxDelay = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "wager_outbox_delay_seconds", Help: "Tempo entre criação e publicação do evento.", Buckets: prometheus.DefBuckets})
	m.latency = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "wager_latency_seconds", Help: "Latência das operações instrumentadas.", Buckets: prometheus.DefBuckets}, []string{"operation"})
	m.divergences = prometheus.NewCounter(prometheus.CounterOpts{Name: "wager_reconciliation_divergences_total", Help: "Reconciliações com saldo divergente."})
	m.registry.MustRegister(m.results, m.duplicates, m.retries, m.dlq, m.conflicts, m.outboxDelay, m.latency, m.divergences)
	for _, source := range []string{"http", "sqs"} {
		for _, status := range []string{"PROCESSED", "REJECTED", "FAILED", "PENDING_REFERENCE"} {
			m.results.WithLabelValues(source, status).Add(0)
		}
		m.duplicates.WithLabelValues(source).Add(0)
	}
	for _, source := range []string{"sqs", "outbox", "postgres"} {
		m.retries.WithLabelValues(source).Add(0)
	}
	for _, operation := range []string{"http_process", "sqs_process", "outbox_publish", "reconcile"} {
		m.latency.WithLabelValues(operation)
	}
	return m
}

// Handler expõe somente as séries deste processo no formato Prometheus.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// Result conta um desfecho persistido de operação.
func (m *Metrics) Result(source, status string) { m.results.WithLabelValues(source, status).Inc() }

// Duplicate conta uma entrada idempotente ou uma mensagem já vista.
func (m *Metrics) Duplicate(source string) { m.duplicates.WithLabelValues(source).Inc() }

// Retry conta uma tentativa remarcada.
func (m *Metrics) Retry(source string) { m.retries.WithLabelValues(source).Inc() }

// DLQ conta uma mensagem movida com sucesso para a fila de erros.
func (m *Metrics) DLQ() { m.dlq.Inc() }

// Conflict conta um conflito de concorrência detectado pelo banco.
func (m *Metrics) Conflict() { m.conflicts.Inc() }

// OutboxDelay mede a idade do evento quando a publicação foi confirmada.
func (m *Metrics) OutboxDelay(delay time.Duration) {
	if delay < 0 {
		delay = 0
	}
	m.outboxDelay.Observe(delay.Seconds())
}

// Latency mede a duração de uma operação sem incluir identificadores nos rótulos.
func (m *Metrics) Latency(operation string, duration time.Duration) {
	m.latency.WithLabelValues(operation).Observe(duration.Seconds())
}

// Divergence conta uma reconciliação que encontrou saldos diferentes.
func (m *Metrics) Divergence() { m.divergences.Inc() }
