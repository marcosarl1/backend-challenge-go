//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wager"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wallet"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/postgres"
)

func newIDs(t *testing.T) (id, player string) {
	t.Helper()
	idU, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	playerU, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	return idU.String(), playerU.String()
}

func mustParseMoney(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	return m
}

func TestWalletStore(t *testing.T) {
	uow := testPool(t)
	ctx := context.Background()
	var store postgres.WalletStore
	now := time.Now().UTC()

	id, _ := uuid.NewV7()
	player, _ := uuid.NewV7()
	w, err := wallet.NewWallet(id, player, "BRL", now)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return store.Insert(ctx, tx, w)
	}); err != nil {
		t.Fatalf("inserindo: %v", err)
	}

	// Duplicata jogador+moeda conflita.
	dup, _ := wallet.NewWallet(id, player, "BRL", now)
	if err := uow.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return store.Insert(ctx, tx, dup)
	}); err == nil {
		t.Fatal("duplicata passou")
	}

	// Travada e aberta: débito grava com a versão esperada...
	var loaded *wallet.Wallet
	if err := uow.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		loaded, err = store.GetForUpdate(ctx, tx, id)
		return err
	}); err != nil || loaded.Version() != 1 {
		t.Fatalf("leitura = %+v, %v", loaded, err)
	}
	txID, _ := uuid.NewV7()
	if _, err := loaded.Debit(txID, mustParseMoney(t, "0.00"), now); err == nil {
		t.Fatal("débito zero no domínio passou")
	}
	betID, _ := uuid.NewV7()
	if _, err := loaded.Credit(betID, mustParseMoney(t, "50.00"), now); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return store.UpdateBalance(ctx, tx, loaded, 1, now)
	}); err != nil {
		t.Fatalf("atualizando: %v", err)
	}

	// ...e versão velha não grava (sem atualização perdida).
	if err := uow.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return store.UpdateBalance(ctx, tx, loaded, 1, now)
	}); err == nil {
		t.Fatal("versão velha passou")
	}
}

func TestWagerStore(t *testing.T) {
	uow := testPool(t)
	ctx := context.Background()
	var wallets postgres.WalletStore
	var txs postgres.WagerStore
	now := time.Now().UTC()

	id, _ := uuid.NewV7()
	player, _ := uuid.NewV7()
	w, _ := wallet.NewWallet(id, player, "BRL", now)
	txID, _ := uuid.NewV7()
	betID, _ := uuid.NewV7()
	tx, err := wager.NewExternal(wager.ExternalParams{
		ID: txID, ProviderID: "provider-a", ExternalID: "bet-" + txID.String(),
		IdempotencyKey: "k-" + txID.String(), PayloadHash: []byte("hash............................"),
		WalletID: id, PlayerID: player, RoundID: "r1", GameID: "g1",
		Kind: wager.KindBet, Amount: mustParseMoney(t, "10.00"),
	}, now)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	var inserted bool
	if err := uow.Do(ctx, func(ctx context.Context, db pgx.Tx) error {
		if err := wallets.Insert(ctx, db, w); err != nil {
			return err
		}
		var err error
		inserted, err = txs.Insert(ctx, db, tx, "corr-1")
		return err
	}); err != nil || !inserted {
		t.Fatalf("inserindo: %v %v", inserted, err)
	}

	// Mesma chave não grava de novo (idempotência na porta).
	if err := uow.Do(ctx, func(ctx context.Context, db pgx.Tx) error {
		dup, err := txs.Insert(ctx, db, tx, "corr-1")
		if err != nil {
			return err
		}
		if dup {
			t.Fatal("reinseriu a mesma chave")
		}
		found, err := txs.FindByProviderKey(ctx, db, "provider-a", "k-"+txID.String())
		if err != nil {
			return err
		}
		if found.Status() != wager.StatusPending {
			t.Fatalf("estado = %s", found.Status())
		}
		return nil
	}); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	// Transição gravada volta reidratada, com resultado.
	bal := mustParseMoney(t, "90.00")
	if err := tx.MarkProcessed(bal, 1, now); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, db pgx.Tx) error {
		return txs.Save(ctx, db, tx)
	}); err != nil {
		t.Fatalf("gravando: %v", err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, db pgx.Tx) error {
		found, err := txs.FindByID(ctx, db, txID)
		if err != nil {
			return err
		}
		got, ver, err := found.Result()
		if err != nil || ver != 1 {
			return err
		}
		if cmp, _ := got.Cmp(bal); cmp != 0 {
			t.Fatalf("resultado = %s", got)
		}
		byExt, err := txs.FindByProviderExternal(ctx, db, "provider-a", "bet-"+txID.String())
		if err != nil || byExt.ID() != txID {
			t.Fatalf("por externo = %v, %v", byExt, err)
		}
		_ = betID
		return nil
	}); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
}

func TestLedgerAndInboxOutboxStores(t *testing.T) {
	uow := testPool(t)
	ctx := context.Background()
	var wallets postgres.WalletStore
	var txs postgres.WagerStore
	var ledger postgres.LedgerStore
	var inbox postgres.InboxStore
	var outbox postgres.OutboxStore
	now := time.Now().UTC()

	id, _ := uuid.NewV7()
	player, _ := uuid.NewV7()
	w, _ := wallet.NewWallet(id, player, "BRL", now)
	openID, _ := uuid.NewV7()
	opening, _ := wager.NewOpening(wager.OpeningParams{
		ID: openID, WalletID: id, PlayerID: player, Amount: mustParseMoney(t, "100.00"),
	}, now)
	entryID, _ := uuid.NewV7()
	entry, err := wallet.NewLedgerEntry(entryID, id, openID, wallet.DirectionCredit,
		mustParseMoney(t, "100.00"), mustParseMoney(t, "0.00"), mustParseMoney(t, "100.00"), now)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	if err := uow.Do(ctx, func(ctx context.Context, db pgx.Tx) error {
		if err := wallets.Insert(ctx, db, w); err != nil {
			return err
		}
		if _, err := txs.Insert(ctx, db, opening, "corr-o"); err != nil {
			return err
		}
		return ledger.Insert(ctx, db, entry)
	}); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	if err := uow.Do(ctx, func(ctx context.Context, db pgx.Tx) error {
		total, count, err := ledger.SumByWallet(ctx, db, id, "BRL")
		if err != nil {
			return err
		}
		if total.String() != "100.00" || count != 1 {
			t.Fatalf("soma = %s (%d)", total, count)
		}
		page, _, err := ledger.Page(ctx, db, id, 0, 50)
		if err != nil || len(page) != 1 || page[0].ID() != entryID {
			t.Fatalf("página = %v, %v", page, err)
		}
		return nil
	}); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	// Inbox: primeira grava, repetida com mesmo hash dispensa.
	msgID := "msg-1-" + uuid.NewString()
	if err := uow.Do(ctx, func(ctx context.Context, db pgx.Tx) error {
		first, err := inbox.Insert(ctx, db, "test", msgID, []byte("h1"), now)
		if err != nil || !first {
			return errors.New("primeira inbox falhou")
		}
		second, err := inbox.Insert(ctx, db, "test", msgID, []byte("h1"), now)
		if err != nil || second {
			return errors.New("repetida deveria dispensar")
		}
		hash, err := inbox.HashOf(ctx, db, "test", msgID)
		if err != nil || string(hash) != "h1" {
			return errors.New("hash divergiu")
		}
		return nil
	}); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	// Outbox: enfileira, reserva, confirma; depois não há mais pendente.
	// Começa zerando a tabela: a reserva pega tudo vencido, e restos de outras execuções entrariam no lote.
	if err := uow.Do(ctx, func(ctx context.Context, db pgx.Tx) error {
		_, err := db.Exec(ctx, `DELETE FROM outbox_events`)
		return err
	}); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	evID, _ := uuid.NewV7()
	if err := uow.Do(ctx, func(ctx context.Context, db pgx.Tx) error {
		return outbox.Insert(ctx, db, postgres.OutboxEvent{
			ID: evID, AggregateType: "wallet", AggregateID: id,
			EventType: "WalletBalanceChanged", EventVersion: 1, OrderingKey: id.String(),
			CorrelationID: "corr-o", Payload: []byte(`{}`), OccurredAt: now,
		}, now)
	}); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, db pgx.Tx) error {
		claimed, err := outbox.Claim(ctx, db, "pub-1", now, now.Add(time.Minute), 10)
		if err != nil || len(claimed) != 1 || claimed[0].ID != evID {
			t.Fatalf("reserva = %v, %v", claimed, err)
		}
		return outbox.MarkPublished(ctx, db, evID, "pub-1", now)
	}); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, db pgx.Tx) error {
		claimed, err := outbox.Claim(ctx, db, "pub-1", now, now.Add(time.Minute), 10)
		if err != nil || len(claimed) != 0 {
			t.Fatalf("pendente após publicar = %v, %v", claimed, err)
		}
		return nil
	}); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
}
