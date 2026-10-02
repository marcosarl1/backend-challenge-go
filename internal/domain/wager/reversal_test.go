package wager

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
)

func reversalTx(t *testing.T, kind Kind, amount, refExt string) *WagerTransaction {
	t.Helper()
	id, _ := uuid.NewV7()
	wallet := uuid.MustParse("0192f291-27dd-7d3f-8071-5f8685deef37")
	player := uuid.MustParse("0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1")
	amt, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	tx, err := NewExternal(ExternalParams{
		ID: id, ProviderID: "provider-a", ExternalID: "ext-" + string(kind),
		IdempotencyKey: "k-" + string(kind), PayloadHash: []byte("hash............................"),
		WalletID: wallet, PlayerID: player, RoundID: "round-1", GameID: "jogo",
		Kind: kind, Amount: amt, ReferenceExtID: refExt,
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	return tx
}

func processedRef(t *testing.T, kind Kind, amount string) *WagerTransaction {
	t.Helper()
	// Forja o alvo a partir de uma aposta válida e troca o tipo: o que
	// importa para a decisão é o tipo e o estado do alvo. (Só o teste faz isso.)
	ref := reversalTx(t, KindBet, amount, "")
	ref.kind = kind
	now := time.Now().UTC()
	bal, _ := money.Parse("1000.00", "BRL")
	if err := ref.MarkProcessed(bal, 3, now); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	return ref
}

func richBalance(t *testing.T) money.Money {
	t.Helper()
	bal, err := money.Parse("1000.00", "BRL")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	return bal
}

func TestReversalMatrix(t *testing.T) {
	rich := richBalance(t)
	tests := []struct {
		name string
		op   func(t *testing.T) *WagerTransaction
		ref  func(t *testing.T) *WagerTransaction
		want ReversalOutcome
	}{
		// REFUND: só aposta processada vira crédito.
		{"reembolso de aposta", func(t *testing.T) *WagerTransaction {
			return reversalTx(t, KindRefund, "10.00", "bet-1")
		}, func(t *testing.T) *WagerTransaction {
			return processedRef(t, KindBet, "10.00")
		}, ReversalOutcome{Action: ReversalApplyCredit}},
		{"reembolso sem referência", func(t *testing.T) *WagerTransaction {
			return reversalTx(t, KindRefund, "10.00", "bet-1")
		}, nil, ReversalOutcome{Action: ReversalWait}},
		{"reembolso de prêmio", func(t *testing.T) *WagerTransaction {
			return reversalTx(t, KindRefund, "10.00", "win-1")
		}, func(t *testing.T) *WagerTransaction {
			return processedRef(t, KindWin, "10.00")
		}, ReversalOutcome{Action: ReversalReject, FailureCode: CodeInvalidReferenceKind}},

		// ROLLBACK: aposta vira crédito, prêmio e reembolso viram débito.
		{"estorno de aposta", func(t *testing.T) *WagerTransaction {
			return reversalTx(t, KindRollback, "10.00", "bet-1")
		}, func(t *testing.T) *WagerTransaction {
			return processedRef(t, KindBet, "10.00")
		}, ReversalOutcome{Action: ReversalApplyCredit}},
		{"estorno de prêmio", func(t *testing.T) *WagerTransaction {
			return reversalTx(t, KindRollback, "10.00", "win-1")
		}, func(t *testing.T) *WagerTransaction {
			return processedRef(t, KindWin, "10.00")
		}, ReversalOutcome{Action: ReversalApplyDebit}},
		{"estorno de reembolso", func(t *testing.T) *WagerTransaction {
			return reversalTx(t, KindRollback, "10.00", "refund-1")
		}, func(t *testing.T) *WagerTransaction {
			return processedRef(t, KindRefund, "10.00")
		}, ReversalOutcome{Action: ReversalApplyDebit}},
		{"estorno de estorno", func(t *testing.T) *WagerTransaction {
			return reversalTx(t, KindRollback, "10.00", "rb-1")
		}, func(t *testing.T) *WagerTransaction {
			return processedRef(t, KindRollback, "10.00")
		}, ReversalOutcome{Action: ReversalReject, FailureCode: CodeInvalidReferenceKind}},
		{"estorno de derrota", func(t *testing.T) *WagerTransaction {
			return reversalTx(t, KindRollback, "10.00", "loss-1")
		}, func(t *testing.T) *WagerTransaction {
			return processedRef(t, KindLoss, "10.00")
		}, ReversalOutcome{Action: ReversalReject, FailureCode: CodeInvalidReferenceKind}},

		// WIN com referência: só aposta da mesma rodada, sempre crédito.
		{"prêmio com aposta", func(t *testing.T) *WagerTransaction {
			return reversalTx(t, KindWin, "10.00", "bet-1")
		}, func(t *testing.T) *WagerTransaction {
			return processedRef(t, KindBet, "10.00")
		}, ReversalOutcome{Action: ReversalApplyCredit}},
		{"prêmio citando prêmio", func(t *testing.T) *WagerTransaction {
			return reversalTx(t, KindWin, "10.00", "win-1")
		}, func(t *testing.T) *WagerTransaction {
			return processedRef(t, KindWin, "10.00")
		}, ReversalOutcome{Action: ReversalReject, FailureCode: CodeInvalidReferenceKind}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op := tt.op(t)
			var ref *WagerTransaction
			if tt.ref != nil {
				ref = tt.ref(t)
			}
			got, err := DecideReversal(op, ref, false, rich)
			if err != nil {
				t.Fatalf("erro inesperado: %v", err)
			}
			if got != tt.want {
				t.Fatalf("decisão = %+v, esperado %+v", got, tt.want)
			}
		})
	}
}

func TestReversalWaitingAndDeadReference(t *testing.T) {
	rich := richBalance(t)
	op := reversalTx(t, KindRefund, "10.00", "bet-1")

	if got, err := DecideReversal(op, nil, false, rich); err != nil || got.Action != ReversalWait {
		t.Fatalf("sem referência = %+v, %v", got, err)
	}
	for _, status := range []Status{StatusPending, StatusPendingReference} {
		ref := reversalTx(t, KindBet, "10.00", "")
		if status == StatusPendingReference {
			now := time.Now().UTC()
			if err := ref.WaitForReference(now.Add(time.Second), now.Add(time.Minute), now); err != nil {
				t.Fatalf("erro inesperado: %v", err)
			}
		}
		if got, err := DecideReversal(op, ref, false, rich); err != nil || got.Action != ReversalWait {
			t.Fatalf("%s = %+v, %v", status, got, err)
		}
	}
	for _, status := range []Status{StatusRejected, StatusFailed} {
		ref := reversalTx(t, KindBet, "10.00", "")
		now := time.Now().UTC()
		if status == StatusRejected {
			if err := ref.Reject(CodeInsufficientFunds, "", now); err != nil {
				t.Fatalf("erro inesperado: %v", err)
			}
		} else {
			if err := ref.Fail(now); err != nil {
				t.Fatalf("erro inesperado: %v", err)
			}
		}
		got, err := DecideReversal(op, ref, false, rich)
		if err != nil || got != (ReversalOutcome{Action: ReversalReject, FailureCode: CodeReferenceNotProcessed}) {
			t.Fatalf("%s = %+v, %v", status, got, err)
		}
	}
}

func TestReversalMismatch(t *testing.T) {
	rich := richBalance(t)
	op := reversalTx(t, KindRefund, "10.00", "bet-1")
	mutate := map[string]func(*WagerTransaction){
		"provedor": func(r *WagerTransaction) { r.providerID = "provider-b" },
		"jogador":  func(r *WagerTransaction) { r.playerID = uuid.MustParse("0192f28f-5dc0-7d58-bdb2-814ad6a0f4a2") },
		"carteira": func(r *WagerTransaction) { r.walletID = uuid.MustParse("0192f291-27dd-7d3f-8071-5f8685deef38") },
		"rodada":   func(r *WagerTransaction) { r.roundID = "round-2" },
		"moeda": func(r *WagerTransaction) {
			usd, _ := money.Parse("10.00", "USD")
			r.amount = usd
		},
	}
	for name, mut := range mutate {
		ref := processedRef(t, KindBet, "10.00")
		mut(ref)
		got, err := DecideReversal(op, ref, false, rich)
		if err != nil || got.FailureCode != CodeReferenceMismatch {
			t.Fatalf("%s = %+v, %v", name, got, err)
		}
	}
	ref := processedRef(t, KindBet, "11.00")
	got, err := DecideReversal(op, ref, false, rich)
	if err != nil || got.FailureCode != CodeReferenceAmountMismatch {
		t.Fatalf("valor = %+v, %v", got, err)
	}
}

func TestReversalAlreadyReversed(t *testing.T) {
	rich := richBalance(t)
	op := reversalTx(t, KindRollback, "10.00", "bet-1")
	ref := processedRef(t, KindBet, "10.00")
	got, err := DecideReversal(op, ref, true, rich)
	if err != nil || got.FailureCode != CodeAlreadyReversed {
		t.Fatalf("decisão = %+v, %v", got, err)
	}
}

func TestReversalWithoutFunds(t *testing.T) {
	// Estornar prêmio ou reembolso debita: sem saldo, rejeita com código
	// próprio, diferente do saldo insuficiente da aposta.
	poor, _ := money.Parse("5.00", "BRL")
	op := reversalTx(t, KindRollback, "10.00", "win-1")
	ref := processedRef(t, KindWin, "10.00")
	got, err := DecideReversal(op, ref, false, poor)
	if err != nil || got.FailureCode != CodeReversalInsufficientFunds {
		t.Fatalf("decisão = %+v, %v", got, err)
	}
	// Reembolso é crédito: saldo baixo não impede.
	opRefund := reversalTx(t, KindRefund, "10.00", "bet-1")
	refBet := processedRef(t, KindBet, "10.00")
	got, err = DecideReversal(opRefund, refBet, false, poor)
	if err != nil || got.Action != ReversalApplyCredit {
		t.Fatalf("decisão = %+v, %v", got, err)
	}
}

func TestDecideRejectsWrongKind(t *testing.T) {
	rich := richBalance(t)
	for _, kind := range []Kind{KindBet, KindLoss, KindOpening} {
		op := reversalTx(t, KindBet, "10.00", "")
		op.kind = kind
		if _, err := DecideReversal(op, nil, false, rich); err == nil {
			t.Fatalf("%s aceito na decisão", kind)
		}
	}
}
