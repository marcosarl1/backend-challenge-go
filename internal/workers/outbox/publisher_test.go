package outbox

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/platform/observability"
)

type tracedOutboxUOW struct{ outbox application.OutboxRepository }

func (u tracedOutboxUOW) Do(ctx context.Context, fn func(context.Context, application.Repositories) error) error {
	return fn(ctx, application.Repositories{Outbox: u.outbox})
}

type tracedOutboxRepo struct{ event application.OutboxClaim }

func (r tracedOutboxRepo) Insert(context.Context, application.OutboxEvent, time.Time) error {
	return nil
}
func (r tracedOutboxRepo) Claim(context.Context, string, time.Time, time.Time, int) ([]application.OutboxClaim, error) {
	return []application.OutboxClaim{r.event}, nil
}
func (r tracedOutboxRepo) MarkPublished(context.Context, uuid.UUID, string, time.Time) error {
	return nil
}
func (r tracedOutboxRepo) DeferFailed(context.Context, uuid.UUID, string, time.Time, string) error {
	return nil
}

type tracedSender struct{ span trace.SpanContext }

func (s *tracedSender) Send(ctx context.Context, _, _, _, _ string) error {
	s.span = trace.SpanContextFromContext(ctx)
	return nil
}

func TestPublisherRestoresPersistedTraceparent(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(t.Context())
	tracing := observability.NewTracingWithProvider(provider)
	ctx, parentSpan := tracing.Start(context.Background(), "http.request", trace.SpanKindServer)
	parent := observability.InjectParent(ctx)
	parentSpan.End()

	sender := &tracedSender{}
	repo := tracedOutboxRepo{event: application.OutboxClaim{Traceparent: parent, OccurredAt: time.Now()}}
	publisher := NewPublisherWithTracing(sender, "events", tracedOutboxUOW{outbox: repo}, application.SystemClock{}, "owner", nil, nil, tracing)
	if published, err := publisher.RunOnce(context.Background()); err != nil || published != 1 {
		t.Fatalf("RunOnce() = %d, %v", published, err)
	}
	if sender.span.TraceID() != parentSpan.SpanContext().TraceID() {
		t.Fatal("publicação perdeu o trace original")
	}
	for _, span := range recorder.Ended() {
		if span.Name() == "outbox.publish" && span.Parent().SpanID() != parentSpan.SpanContext().SpanID() {
			t.Fatal("span da publicação não herdou o span persistido")
		}
	}
}

func TestBackoffBounds(t *testing.T) {
	base, max := time.Second, time.Minute
	for attempts := range 12 {
		for range 50 {
			got := backoffWithJitter(base, max, attempts)
			ceiling := base << attempts
			if ceiling <= 0 || ceiling > max {
				ceiling = max
			}
			if got < ceiling/2 || got > ceiling {
				t.Fatalf("tentativa %d: %v fora de [%v, %v]", attempts, got, ceiling/2, ceiling)
			}
		}
	}
}
