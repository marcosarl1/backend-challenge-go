package wager

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func snapshotOf(t *testing.T, tx *WagerTransaction) Snapshot {
	t.Helper()
	return tx.Snapshot()
}

func TestSnapshotRoundTrip(t *testing.T) {
	now := time.Now().UTC()
	tx := newPending(t, KindRefund, "10.00")
	if err := tx.WaitForReference(now.Add(time.Second), now.Add(time.Minute), now); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	id, _ := uuid.NewV7()
	if err := tx.ResolveReference(id); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	back, err := Rehydrate(snapshotOf(t, tx))
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if back.Snapshot().Status != StatusPendingReference || back.Snapshot().Attempts != 1 {
		t.Fatalf("reidratação divergiu: %+v", back.Snapshot())
	}
	resolved, ok := back.ResolvedRefID()
	if !ok || resolved != id {
		t.Fatalf("referência = %v, %v", resolved, ok)
	}
}

func TestRehydrateRejects(t *testing.T) {
	good := snapshotOf(t, newPending(t, KindBet, "10.00"))
	cases := map[string]func(*Snapshot){
		"id vazio":             func(s *Snapshot) { s.ID = uuid.Nil },
		"tipo estranho":        func(s *Snapshot) { s.Kind = "DADO" },
		"estado estranho":      func(s *Snapshot) { s.Status = "VOANDO" },
		"externa sem jogo":     func(s *Snapshot) { s.GameID = "" },
		"abertura com jogo":    func(s *Snapshot) { s.Kind, s.GameID = KindOpening, "jogo" },
		"tentativas negativas": func(s *Snapshot) { s.Attempts = -1 },
		"criação vazia":        func(s *Snapshot) { s.CreatedAt = time.Time{} },
	}
	for name, mut := range cases {
		snap := good
		mut(&snap)
		if _, err := Rehydrate(snap); err == nil {
			t.Fatalf("%s aceito", name)
		}
	}
	// Resultado só em processada; código só em falha.
	processed := snapshotOf(t, emEstado(t, StatusProcessed))
	processed.ResultVersion = 0
	if _, err := Rehydrate(processed); err == nil {
		t.Fatal("processada sem versão aceita")
	}
	pending := good
	pending.FailureCode = CodeInsufficientFunds
	if _, err := Rehydrate(pending); err == nil {
		t.Fatal("pendente com código aceita")
	}
}
