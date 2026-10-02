package observability

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestTraceparentConnectsAsyncSpans(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(context.Background())
	tracing := NewTracingWithProvider(provider)

	httpCtx, httpSpan := tracing.Start(context.Background(), "http.request", trace.SpanKindServer)
	uowCtx, uowSpan := tracing.Start(httpCtx, "postgres.transaction", trace.SpanKindInternal)
	parent := InjectParent(uowCtx)
	uowSpan.End()
	httpSpan.End()
	if parent == "" {
		t.Fatal("traceparent vazio")
	}

	publishCtx := ExtractParent(context.Background(), parent)
	publishCtx, publishSpan := tracing.Start(publishCtx, "outbox.publish", trace.SpanKindInternal)
	_, sendSpan := tracing.Start(publishCtx, "sqs.send", trace.SpanKindProducer)
	sendSpan.End()
	publishSpan.End()

	spans := recorder.Ended()
	if len(spans) != 4 {
		t.Fatalf("spans = %d, esperado 4", len(spans))
	}
	byName := make(map[string]sdktrace.ReadOnlySpan, len(spans))
	for _, span := range spans {
		byName[span.Name()] = span
		if span.SpanContext().TraceID() != httpSpan.SpanContext().TraceID() {
			t.Errorf("%s perdeu o trace de entrada", span.Name())
		}
	}
	for child, parent := range map[string]string{
		"postgres.transaction": "http.request", "outbox.publish": "postgres.transaction", "sqs.send": "outbox.publish",
	} {
		if byName[child].Parent().SpanID() != byName[parent].SpanContext().SpanID() {
			t.Errorf("%s não é filho de %s", child, parent)
		}
	}
}

func TestInvalidTraceparentIsIgnored(t *testing.T) {
	ctx := ExtractParent(context.Background(), "Bearer private-token")
	if got := InjectParent(ctx); got != "" {
		t.Fatalf("traceparent inválido foi aceito: %q", got)
	}
}
