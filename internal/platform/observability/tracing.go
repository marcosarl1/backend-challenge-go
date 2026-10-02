package observability

import (
	"context"
	"fmt"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"

	"github.com/marcosarl1/backend-challenge-go/internal/platform/config"
)

// Tracing inicia spans e propaga traceparent sem incluir payload financeiro.
type Tracing struct {
	provider trace.TracerProvider
	carrier  propagation.TraceContext
}

// NewTracing ativa a exportação OTLP quando há endpoint e fecha o exporter no shutdown.
func NewTracing(lifecycle fx.Lifecycle, cfg config.Config) (*Tracing, error) {
	if cfg.OTLPEndpoint == "" {
		return &Tracing{provider: trace.NewNoopTracerProvider()}, nil
	}
	exporter, err := otlptracehttp.New(context.Background(), otlptracehttp.WithEndpointURL(cfg.OTLPEndpoint))
	if err != nil {
		return nil, fmt.Errorf("configurando exporter OTLP: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewWithAttributes("", attribute.String("service.name", "wagering-api"))),
	)
	lifecycle.Append(fx.Hook{OnStop: provider.Shutdown})
	return &Tracing{provider: provider}, nil
}

// NewTracingWithProvider permite testar spans sem coletor externo.
func NewTracingWithProvider(provider trace.TracerProvider) *Tracing {
	return &Tracing{provider: provider}
}

// Start abre um span filho do contexto recebido.
func (t *Tracing) Start(ctx context.Context, name string, kind trace.SpanKind) (context.Context, trace.Span) {
	return t.provider.Tracer("wagering").Start(ctx, name, trace.WithSpanKind(kind))
}

// ExtractHTTP lê o contexto dos cabeçalhos da requisição.
func (t *Tracing) ExtractHTTP(ctx context.Context, headers http.Header) context.Context {
	return t.carrier.Extract(ctx, propagation.HeaderCarrier(headers))
}

// InjectParent serializa o contexto para persistir junto do evento da outbox.
func InjectParent(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	(propagation.TraceContext{}).Inject(ctx, carrier)
	return carrier.Get("traceparent")
}

// ExtractParent restaura o contexto persistido antes de publicar no SQS.
func ExtractParent(ctx context.Context, parent string) context.Context {
	if parent == "" {
		return ctx
	}
	return (propagation.TraceContext{}).Extract(ctx, propagation.MapCarrier{"traceparent": parent})
}
