package wager

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/domain"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
)

func mustAmount(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("Parse(%q) erro inesperado: %v", amount, err)
	}
	return m
}

func baseParams(t *testing.T, kind Kind, amount string) ExternalParams {
	t.Helper()
	id, _ := uuid.NewV7()
	wallet, _ := uuid.NewV7()
	player, _ := uuid.NewV7()
	ref := ""
	if kind == KindRefund || kind == KindRollback {
		ref = "bet-orig"
	}
	return ExternalParams{
		ID: id, ProviderID: "provider-a", ExternalID: "ext-1",
		IdempotencyKey: "provider-a:ext-1", PayloadHash: []byte("hash32bytes....................."),
		WalletID: wallet, PlayerID: player, RoundID: "round-1", GameID: "jogo",
		Kind: kind, Amount: mustAmount(t, amount), ReferenceExtID: ref,
	}
}

func newPending(t *testing.T, kind Kind, amount string) *WagerTransaction {
	t.Helper()
	tx, err := NewExternal(baseParams(t, kind, amount), time.Now().UTC())
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	return tx
}

// emEstado leva uma aposta até cada estado de partida da matriz.
func emEstado(t *testing.T, s Status) *WagerTransaction {
	t.Helper()
	now := time.Now().UTC()
	tx := newPending(t, KindBet, "10.00")
	switch s {
	case StatusPending:
	case StatusPendingReference:
		if err := tx.WaitForReference(now.Add(time.Second), now.Add(time.Minute), now); err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
	case StatusProcessed:
		if err := tx.MarkProcessed(mustAmount(t, "90.00"), 2, now); err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
	case StatusRejected:
		if err := tx.Reject(CodeInsufficientFunds, "sem saldo", now); err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
	case StatusFailed:
		if err := tx.Fail(now); err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
	}
	return tx
}

func TestTransitionMatrix(t *testing.T) {
	now := time.Now().UTC()
	balance := mustAmount(t, "90.00")

	/*
	   * Para cada estado, cada método: funciona ou volta erro classificável.
	   terminal=true exige ErrTerminalState; senão, sucesso ou transição
	   inválida conforme a máquina.
	*/
	tests := []struct {
		from     Status
		method   string
		run      func(*WagerTransaction) error
		terminal bool
		wantErr  error // nil = espera sucesso
	}{
		{StatusPending, "processar", func(tx *WagerTransaction) error {
			return tx.MarkProcessed(balance, 2, now)
		}, false, nil},
		{StatusPending, "rejeitar", func(tx *WagerTransaction) error {
			return tx.Reject(CodeInsufficientFunds, "", now)
		}, false, nil},
		{StatusPending, "esperar", func(tx *WagerTransaction) error {
			return tx.WaitForReference(now.Add(time.Second), now.Add(time.Minute), now)
		}, false, nil},
		{StatusPending, "remarcar", func(tx *WagerTransaction) error {
			return tx.Reschedule(now.Add(time.Second), now)
		}, false, domain.ErrInvalidTransition},
		{StatusPending, "falhar", func(tx *WagerTransaction) error {
			return tx.Fail(now)
		}, false, nil},
		{StatusPending, "resolver", func(tx *WagerTransaction) error {
			id, _ := uuid.NewV7()
			return tx.ResolveReference(id)
		}, false, nil},

		{StatusPendingReference, "processar", func(tx *WagerTransaction) error {
			return tx.MarkProcessed(balance, 2, now)
		}, false, nil},
		{StatusPendingReference, "rejeitar", func(tx *WagerTransaction) error {
			return tx.Reject(CodeReferenceNotFound, "", now)
		}, false, nil},
		{StatusPendingReference, "esperar de novo", func(tx *WagerTransaction) error {
			return tx.WaitForReference(now.Add(time.Second), now.Add(time.Minute), now)
		}, false, domain.ErrInvalidTransition},
		{StatusPendingReference, "remarcar", func(tx *WagerTransaction) error {
			return tx.Reschedule(now.Add(time.Second), now)
		}, false, nil},
		{StatusPendingReference, "falhar", func(tx *WagerTransaction) error {
			return tx.Fail(now)
		}, false, nil},
		{StatusPendingReference, "resolver", func(tx *WagerTransaction) error {
			id, _ := uuid.NewV7()
			return tx.ResolveReference(id)
		}, false, nil},
	}
	for _, s := range []Status{StatusProcessed, StatusRejected, StatusFailed} {
		for _, m := range []struct {
			name string
			run  func(*WagerTransaction) error
		}{
			{"processar", func(tx *WagerTransaction) error { return tx.MarkProcessed(balance, 2, now) }},
			{"rejeitar", func(tx *WagerTransaction) error { return tx.Reject(CodeInsufficientFunds, "", now) }},
			{"esperar", func(tx *WagerTransaction) error {
				return tx.WaitForReference(now.Add(time.Second), now.Add(time.Minute), now)
			}},
			{"remarcar", func(tx *WagerTransaction) error { return tx.Reschedule(now.Add(time.Second), now) }},
			{"falhar", func(tx *WagerTransaction) error { return tx.Fail(now) }},
			{"resolver", func(tx *WagerTransaction) error {
				id, _ := uuid.NewV7()
				return tx.ResolveReference(id)
			}},
		} {
			tests = append(tests, struct {
				from     Status
				method   string
				run      func(*WagerTransaction) error
				terminal bool
				wantErr  error
			}{s, m.name, m.run, true, domain.ErrTerminalState})
		}
	}

	for _, tt := range tests {
		t.Run(string(tt.from)+"/"+tt.method, func(t *testing.T) {
			tx := emEstado(t, tt.from)
			err := tt.run(tx)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("erro inesperado: %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("erro = %v, esperado %v", err, tt.wantErr)
			}
			if tt.terminal {
				var terr *domain.TransitionError
				if !errors.As(err, &terr) {
					t.Fatalf("estado final sem TransitionError: %v", err)
				}
			}
		})
	}
}

func TestKindValueRules(t *testing.T) {
	now := time.Now().UTC()
	for _, tt := range []struct {
		kind    Kind
		amount  string
		wantErr error
	}{
		{KindBet, "10.00", nil},
		{KindWin, "10.00", nil},
		{KindLoss, "0.00", nil},
		{KindRefund, "10.00", nil},
		{KindRollback, "10.00", nil},
		{KindBet, "0.00", domain.ErrInvalidMoney},
		{KindWin, "0.00", domain.ErrInvalidMoney},
		{KindRefund, "0.00", domain.ErrInvalidMoney},
		{KindRollback, "0.00", domain.ErrInvalidMoney},
		{KindLoss, "0.01", domain.ErrInvalidMoney},
		{KindOpening, "10.00", domain.ErrInvalidKind},
		{Kind("DADO"), "10.00", domain.ErrInvalidKind},
	} {
		p := baseParams(t, tt.kind, "10.00")
		if tt.amount != "10.00" || tt.kind == KindLoss {
			p.Amount = mustAmount(t, tt.amount)
		}
		_, err := NewExternal(p, now)
		if tt.wantErr == nil && err != nil {
			t.Fatalf("%s %s erro inesperado: %v", tt.kind, tt.amount, err)
		}
		if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
			t.Fatalf("%s %s erro = %v, esperado %v", tt.kind, tt.amount, err, tt.wantErr)
		}
	}
}

func TestReferencePresenceRules(t *testing.T) {
	now := time.Now().UTC()
	// REFUND e ROLLBACK sem referência são rejeitados na criação.
	for _, kind := range []Kind{KindRefund, KindRollback} {
		p := baseParams(t, kind, "10.00")
		p.ReferenceExtID = ""
		if _, err := NewExternal(p, now); err == nil {
			t.Fatalf("%s sem referência aceito", kind)
		}
	}
	// BET e LOSS com referência são rejeitados na criação.
	for _, kind := range []Kind{KindBet, KindLoss} {
		p := baseParams(t, kind, "10.00")
		if kind == KindLoss {
			p.Amount = mustAmount(t, "0.00")
		}
		p.ReferenceExtID = "alguma"
		if _, err := NewExternal(p, now); err == nil {
			t.Fatalf("%s com referência aceito", kind)
		}
	}
	// WIN com referência é aceito (aguarda como as reversões).
	p := baseParams(t, KindWin, "10.00")
	p.ReferenceExtID = "bet-orig"
	if _, err := NewExternal(p, now); err != nil {
		t.Fatalf("WIN com referência rejeitado: %v", err)
	}
}

func TestOpeningConstructor(t *testing.T) {
	now := time.Now().UTC()
	id, _ := uuid.NewV7()
	wallet, _ := uuid.NewV7()
	player, _ := uuid.NewV7()
	tx, err := NewOpening(OpeningParams{
		ID: id, WalletID: wallet, PlayerID: player, Amount: mustAmount(t, "100.00"),
	}, now)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if tx.Status() != StatusProcessed || tx.Kind() != KindOpening {
		t.Fatalf("abertura precisa nascer processada: %s %s", tx.Kind(), tx.Status())
	}
	if tx.ProviderID() != "" || tx.ReferenceExtID() != "" || len(tx.PayloadHash()) != 0 {
		t.Fatal("abertura carrega metadado externo")
	}
	bal, ver, err := tx.Result()
	if err != nil || bal.String() != "100.00" || ver != 1 {
		t.Fatalf("resultado = %v %d %v", bal, ver, err)
	}
	// Abertura zerada não existe.
	if _, err := NewOpening(OpeningParams{
		ID: id, WalletID: wallet, PlayerID: player, Amount: mustAmount(t, "0.00"),
	}, now); err == nil {
		t.Fatal("abertura zero aceita")
	}
}

func TestFailureCodes(t *testing.T) {
	tx := newPending(t, KindBet, "10.00")
	now := time.Now().UTC()
	if err := tx.Reject("QUALQUER_COISA", "", now); err == nil {
		t.Fatal("código fora do catálogo aceito")
	}
	if err := tx.Reject(CodeInsufficientFunds, "detalhe", now); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	code, detail, err := tx.Failure()
	if err != nil || code != CodeInsufficientFunds || detail != "detalhe" {
		t.Fatalf("falha = %q %q %v", code, detail, err)
	}
	for _, c := range []FailureCode{
		CodeReversalInsufficientFunds, CodeReferenceNotFound, CodeReferenceNotProcessed,
		CodeReferenceMismatch, CodeReferenceAmountMismatch, CodeInvalidReferenceKind,
		CodeAlreadyReversed, CodeWalletMismatch, CodeCurrencyMismatch,
		CodeInternalPermanentFailure,
	} {
		if !c.Valid() {
			t.Fatalf("código do catálogo inválido: %q", c)
		}
	}
	if FailureCode("NADA").Valid() {
		t.Fatal("código desconhecido válido")
	}
}

func TestResultAndFailureGuards(t *testing.T) {
	tx := newPending(t, KindBet, "10.00")
	if _, _, err := tx.Result(); err == nil {
		t.Fatal("resultado de pendente aceito")
	}
	if _, _, err := tx.Failure(); err == nil {
		t.Fatal("falha de pendente aceita")
	}
	now := time.Now().UTC()
	if err := tx.MarkProcessed(mustAmount(t, "90.00"), 2, now); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if _, _, err := tx.Failure(); err == nil {
		t.Fatal("falha de processada aceita")
	}
}
