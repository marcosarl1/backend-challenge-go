// Roda com:
//
//	TEST_DATABASE_URL=postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable \
//	  go test -race ./test/integration/ -run TestLedgerAppendOnly -v
package integration

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func ownerURL(t *testing.T) string {
	t.Helper()
	if url := os.Getenv("TEST_DATABASE_URL"); url != "" {
		return url
	}
	t.Skip("TEST_DATABASE_URL ausente; pulei o teste contra banco de verdade")
	return ""
}

func appURL(t *testing.T, owner string) string {
	t.Helper()
	if strings.Contains(owner, "wagering:wagering@") {
		return strings.Replace(owner, "wagering:wagering@", "app:app@", 1)
	}
	t.Skip("URL sem credencial do compose; pulei o papel app")
	return ""
}

func connect(t *testing.T, url string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("conectando: %v", err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func TestLedgerAppendOnly(t *testing.T) {
	owner := connect(t, ownerURL(t))
	app := connect(t, appURL(t, ownerURL(t)))
	ctx := context.Background()

	walletID := uuid.NewString()
	playerID := uuid.NewString()
	openingID := uuid.NewString()
	entryID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	_, err := owner.Exec(ctx, `INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
		VALUES ($1, $2, 'BRL', 10000, 1, $3, $3)`, walletID, playerID, now)
	if err != nil {
		t.Fatalf("carteira: %v", err)
	}
	_, err = owner.Exec(ctx, `INSERT INTO wager_transactions
		(id, origin, kind, status, wallet_id, player_id, currency, amount_minor,
		 result_balance_minor, result_wallet_version, correlation_id, created_at, updated_at, resolved_at)
		VALUES ($1, 'INTERNAL', 'OPENING', 'PROCESSED', $2, $3, 'BRL', 10000, 10000, 1, 'seed', $4, $4, $4)`,
		openingID, walletID, playerID, now)
	if err != nil {
		t.Fatalf("abertura: %v", err)
	}
	_, err = owner.Exec(ctx, `INSERT INTO wallet_ledger_entries
		(id, wallet_id, transaction_id, direction, amount_minor, currency, balance_before_minor, balance_after_minor, created_at)
		VALUES ($1, $2, $3, 'CREDIT', 10000, 'BRL', 0, 10000, $4)`,
		entryID, walletID, openingID, now)
	if err != nil {
		t.Fatalf("lançamento: %v", err)
	}

	// Como dono: gatilhos barram UPDATE, DELETE e TRUNCATE no ledger...
	for name, sql := range map[string]string{
		"update dono":   `UPDATE wallet_ledger_entries SET amount_minor = 1 WHERE id = '` + entryID + `'`,
		"delete dono":   `DELETE FROM wallet_ledger_entries WHERE id = '` + entryID + `'`,
		"truncate dono": `TRUNCATE wallet_ledger_entries`,
	} {
		if _, err := owner.Exec(ctx, sql); err == nil ||
			!strings.Contains(err.Error(), "append-only") {
			t.Fatalf("%s: esperado bloqueio append-only, veio %v", name, err)
		}
	}

	// ...e barram mexer na transação final, mas liberam a pendente.
	if _, err := owner.Exec(ctx, `UPDATE wager_transactions SET failure_detail = 'x' WHERE id = '`+openingID+`'`); err == nil ||
		!strings.Contains(err.Error(), "immutable") {
		t.Fatalf("transação final mutável: %v", err)
	}

	// Como app: inserir passa, atualizar/apagar/truncar caem na permissão.
	winID := uuid.NewString()
	winExt := "win-app-" + uuid.NewString()
	_, err = owner.Exec(ctx, `INSERT INTO wager_transactions
		(id, origin, kind, status, wallet_id, player_id, currency, amount_minor,
		 provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
		 result_balance_minor, result_wallet_version, correlation_id, created_at, updated_at, resolved_at)
		VALUES ($1, 'EXTERNAL', 'WIN', 'PROCESSED', $2, $3, 'BRL', 100,
		 'provider-a', $5, $5, '\xaa', 'round-1', 'jogo',
		 10100, 2, 'seed', $4, $4, $4)`,
		winID, walletID, playerID, now, winExt)
	if err != nil {
		t.Fatalf("prêmio: %v", err)
	}
	appEntry := uuid.NewString()
	if _, err := app.Exec(ctx, `INSERT INTO wallet_ledger_entries
		(id, wallet_id, transaction_id, direction, amount_minor, currency, balance_before_minor, balance_after_minor, created_at)
		VALUES ($1, $2, $3, 'CREDIT', 100, 'BRL', 10000, 10100, $4)`,
		appEntry, walletID, winID, now); err != nil {
		t.Fatalf("app inserindo: %v", err)
	}
	for name, sql := range map[string]string{
		"update app": `UPDATE wallet_ledger_entries SET amount_minor = 1 WHERE id = '` + appEntry + `'`,
		"delete app": `DELETE FROM wallet_ledger_entries WHERE id = '` + appEntry + `'`,
	} {
		if _, err := app.Exec(ctx, sql); err == nil ||
			!strings.Contains(err.Error(), "denied") {
			t.Fatalf("%s: esperado permission denied, veio %v", name, err)
		}
	}
	if _, err := app.Exec(ctx, `TRUNCATE wallet_ledger_entries`); err == nil {
		t.Fatal("truncate app passou, esperado falha")
	}
}
