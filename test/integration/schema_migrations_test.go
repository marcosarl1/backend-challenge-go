//go:build integration

package integration

import (
	"context"
	"fmt"
	"io/fs"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/marcosarl1/backend-challenge-go/migrations"
)

// TestSchemaMigrationsUpDown verifica as duas direções em um banco isolado, inclusive a coluna acrescentada depois do schema inicial.
func TestSchemaMigrationsUpDown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := postgrescontainer.Run(ctx, "postgres:17.11-alpine",
		postgrescontainer.WithDatabase("wagering"), postgrescontainer.WithUsername("wagering"),
		postgrescontainer.WithPassword("wagering"), postgrescontainer.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("PostgreSQL isolado: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(db); err != nil {
			t.Errorf("encerrando PostgreSQL: %v", err)
		}
	})
	url, err := db.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(ctx, url); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_name = 'outbox_events' AND column_name = 'traceparent')`).Scan(&exists); err != nil || !exists {
		t.Fatalf("migration final não aplicada: existe=%v, erro=%v", exists, err)
	}
	files, err := fs.Glob(migrations.FS, "*.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for i := len(files) - 1; i >= 0; i-- {
		query, err := fs.ReadFile(migrations.FS, files[i])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(query), pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatalf("revertendo %s: %v", files[i], err)
		}
	}
	if err := conn.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name = 'wallets')`).Scan(&exists); err != nil || exists {
		t.Fatalf("schema permaneceu após down: existe=%v, erro=%v", exists, err)
	}
	if err := applyMigrations(ctx, url); err != nil {
		t.Fatal(fmt.Errorf("reaplicando migrations: %w", err))
	}
}
