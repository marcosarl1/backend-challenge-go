//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wager"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/postgres"
	outboxworker "github.com/marcosarl1/backend-challenge-go/internal/workers/outbox"
)

// recordingSender grava os envios para conferir deduplicação.
type recordingSender struct {
	mu    sync.Mutex
	sends []sentCall
}

type sentCall struct {
	body  string
	group string
	dedup string
}

func (s *recordingSender) Send(_ context.Context, _, body, group, dedup string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends = append(s.sends, sentCall{body: body, group: group, dedup: dedup})
	return nil
}

func (s *recordingSender) dedups() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, call := range s.sends {
		out = append(out, call.dedup)
	}
	return out
}

func publisherFor(t *testing.T, runner postgres.Runner, sender *recordingSender, owner string) *outboxworker.Publisher {
	t.Helper()
	return outboxworker.NewPublisher(sender, "ignore-url", runner, application.SystemClock{}, owner)
}

// cleanPendingOutbox zera pendências de outros testes (só linhas, sem trigger).
func cleanPendingOutbox(t *testing.T) {
	t.Helper()
	conn := connect(t, ownerURL(t))
	if _, err := conn.Exec(context.Background(), `DELETE FROM outbox_events WHERE published_at IS NULL`); err != nil {
		t.Fatalf("limpando outbox: %v", err)
	}
}

func outboxState(t *testing.T, owner string) (published, pending int, attempts map[string]int) {
	t.Helper()
	conn := connect(t, ownerURL(t))
	_ = owner
	rows, err := conn.Query(context.Background(),
		`SELECT id::text, published_at IS NOT NULL, attempts FROM outbox_events`)
	if err != nil {
		t.Fatalf("lendo outbox: %v", err)
	}
	defer rows.Close()
	attempts = map[string]int{}
	for rows.Next() {
		var id string
		var done bool
		var n int
		if err := rows.Scan(&id, &done, &n); err != nil {
			t.Fatalf("lendo: %v", err)
		}
		if done {
			published++
		} else {
			pending++
		}
		attempts[id] = n
	}
	return published, pending, attempts
}

func TestPublishersDispute(t *testing.T) {
	cleanPendingOutbox(t)
	runner := openRunner(t)
	walletID, playerID := fundWallet(t, runner, "1000.00")
	betCmd := processCmd(t, walletID, playerID, wager.KindBet, "10.00")
	runProcess(t, runner, betCmd)

	sender := &recordingSender{}
	pubA := publisherFor(t, runner, sender, "pub-A")
	pubB := publisherFor(t, runner, sender, "pub-B")

	var wg sync.WaitGroup
	for _, pub := range []*outboxworker.Publisher{pubA, pubB} {
		wg.Add(1)
		go func(pub *outboxworker.Publisher) {
			defer wg.Done()
			if _, err := pub.RunOnce(context.Background()); err != nil {
				t.Errorf("publicando: %v", err)
			}
		}(pub)
	}
	wg.Wait()

	// Os 4 eventos saíram, cada um reservado uma vez só (sem disputa dupla).
	published, _, attempts := outboxState(t, "")
	if published < 4 {
		t.Fatalf("publicados = %d", published)
	}
	for id, n := range attempts {
		if n != 1 {
			t.Fatalf("evento %s reservado %dx", id, n)
		}
	}
	if len(sender.dedups()) != 4 {
		t.Fatalf("envios = %d", len(sender.dedups()))
	}
	seen := map[string]bool{}
	for _, dedup := range sender.dedups() {
		if seen[dedup] {
			t.Fatalf("eventId duplicado no envio: %s", dedup)
		}
		seen[dedup] = true
		if _, err := uuid.Parse(dedup); err != nil {
			t.Fatalf("dedup não é eventId: %s", dedup)
		}
	}
}

func TestPublishCrashRepublishesSameID(t *testing.T) {
	cleanPendingOutbox(t)
	runner := openRunner(t)
	walletID, playerID := fundWallet(t, runner, "1000.00")
	_ = walletID
	_ = playerID

	sender := &recordingSender{}
	pub := publisherFor(t, runner, sender, "pub-crash")
	crashed := map[string]bool{}
	pub.AfterPublish = func(id uuid.UUID) error {
		if !crashed[id.String()] {
			crashed[id.String()] = true
			return errors.New("queda entre publicar e confirmar")
		}
		return nil
	}
	if _, err := pub.RunOnce(context.Background()); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if len(sender.dedups()) == 0 {
		t.Fatal("nada enviado")
	}
	// Nada confirmado na queda: expira os arrendamentos e retoma.
	conn := connect(t, ownerURL(t))
	if _, err := conn.Exec(context.Background(),
		`UPDATE outbox_events SET lease_until = now() - interval '1 second' WHERE published_at IS NULL`); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	pub.AfterPublish = nil
	if _, err := pub.RunOnce(context.Background()); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	// Cada evento saiu 2x com o mesmo eventId e confirmou 1x.
	byDedup := map[string]int{}
	for _, dedup := range sender.dedups() {
		byDedup[dedup]++
	}
	for dedup, n := range byDedup {
		if n != 2 {
			t.Fatalf("eventId %s enviado %dx", dedup, n)
		}
	}
	published, pending, _ := outboxState(t, "")
	if pending != 0 || published < len(byDedup) {
		t.Fatalf("publicados=%d pendentes=%d", published, pending)
	}
}

func TestPublishDeliversToQueue(t *testing.T) {
	runner := openRunner(t)
	ctx := context.Background()
	walletID, playerID := fundWallet(t, runner, "1000.00")
	_ = walletID
	_ = playerID

	endpoint := sqsURL(t)
	client, queueURL := sqsClient(t)
	_ = queueURL
	eventsURL, err := client.ResolveQueue(ctx, "wager-events.fifo")
	if err != nil {
		t.Fatalf("fila: %v", err)
	}
	_ = endpoint
	pub := outboxworker.NewPublisher(outboxworker.ClientSender{Client: client}, eventsURL,
		runner, application.SystemClock{}, "pub-e2e")
	n, err := pub.RunOnce(ctx)
	if err != nil || n == 0 {
		t.Fatalf("publicados = %d, %v", n, err)
	}
	found := 0
	for range 3 {
		received, err := client.Receive(ctx, eventsURL, 10, 2)
		if err != nil {
			t.Fatalf("recebendo: %v", err)
		}
		for _, m := range received {
			if strings.Contains(m.Body, `"eventType"`) {
				found++
			}
			if err := client.Delete(ctx, eventsURL, m.ReceiptHandle); err != nil {
				t.Fatalf("limpando: %v", err)
			}
		}
		if len(received) == 0 {
			break
		}
	}
	if found == 0 {
		t.Fatal("nenhum evento chegou na fila")
	}
}
