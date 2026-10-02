package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/marcosarl1/backend-challenge-go/internal/platform/observability"
)

type traceOutboxDB struct {
	query string
	args  []any
}

func (db *traceOutboxDB) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	db.query, db.args = query, args
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (*traceOutboxDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("Query não esperado")
}

func (*traceOutboxDB) QueryRow(context.Context, string, ...any) pgx.Row {
	panic("QueryRow não esperado")
}

func TestOutboxInsertPersistsTraceparent(t *testing.T) {
	provider := sdktrace.NewTracerProvider()
	defer provider.Shutdown(t.Context())
	tracing := observability.NewTracingWithProvider(provider)
	ctx, span := tracing.Start(context.Background(), "http.request", trace.SpanKindServer)
	defer span.End()

	db := &traceOutboxDB{}
	if err := (OutboxStore{}).Insert(ctx, db, OutboxEvent{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(db.args) != 12 || db.args[11] != observability.InjectParent(ctx) {
		t.Fatalf("outbox não persistiu traceparent: %v", db.args)
	}
}
