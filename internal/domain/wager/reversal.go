package wager

import (
	"fmt"

	"github.com/marcosarl1/backend-challenge-go/internal/domain"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
)

// ReversalAction é o veredito sobre uma operação que cita outra.
type ReversalAction string

const (
	// ReversalApplyCredit credita o valor (REFUND, ROLLBACK de BET, WIN com
	// referência).
	ReversalApplyCredit ReversalAction = "APPLY_CREDIT"
	// ReversalApplyDebit debita o valor (ROLLBACK de WIN ou de REFUND).
	ReversalApplyDebit ReversalAction = "APPLY_DEBIT"
	// ReversalWait guarda a operação para quando a referência chegar.
	ReversalWait ReversalAction = "WAIT"
	// ReversalReject encerra com o FailureCode do resultado.
	ReversalReject ReversalAction = "REJECT"
)

// ReversalOutcome é a decisão: o que fazer e, se for rejeitar, com qual
// código estável.
type ReversalOutcome struct {
	Action      ReversalAction
	FailureCode FailureCode
}

// DecideReversal decide, sem encostar em nada, o destino de uma operação que
// cita outra (REFUND, ROLLBACK ou WIN com referência):
//
//   - referência ausente ou ainda pendente → esperar;
//   - referência terminada sem sucesso → rejeitar como não processada;
//   - referência processada → conferir provedor, jogador, carteira, moeda,
//     rodada e valor, o tipo permitido para cada caso e se o alvo já foi
//     revertido; o movimento do ROLLBACK é o contrário do original, e
//     debitar além do saldo rejeita com código próprio.
//
// refReversed diz se o alvo já tem uma reversão bem-sucedida; balance é o
// saldo atual, usado só para caber o débito do estorno.
func DecideReversal(op, ref *WagerTransaction, refReversed bool, balance money.Money) (ReversalOutcome, error) {
	wait := ReversalOutcome{Action: ReversalWait}
	reject := func(code FailureCode) ReversalOutcome {
		return ReversalOutcome{Action: ReversalReject, FailureCode: code}
	}
	switch op.Kind() {
	case KindRefund, KindRollback, KindWin:
	default:
		return wait, fmt.Errorf("%w: decisão só para reversão ou prêmio com referência, veio %s", domain.ErrInvalidKind, op.Kind())
	}
	if op.ReferenceExtID() == "" {
		return wait, fmt.Errorf("%w: decisão exige referência", domain.ErrInvalidMoney)
	}
	if ref == nil {
		return wait, nil
	}
	switch ref.Status() {
	case StatusPending, StatusPendingReference:
		return wait, nil
	case StatusRejected, StatusFailed:
		return reject(CodeReferenceNotProcessed), nil
	case StatusProcessed:
	default:
		return wait, fmt.Errorf("%w: estado %q", domain.ErrInvalidMoney, string(ref.Status()))
	}

	if op.ProviderID() != ref.ProviderID() ||
		op.PlayerID() != ref.PlayerID() ||
		op.WalletID() != ref.WalletID() ||
		op.RoundID() != ref.RoundID() ||
		op.Amount().Currency() != ref.Amount().Currency() {
		return reject(CodeReferenceMismatch), nil
	}
	if cmp, _ := op.Amount().Cmp(ref.Amount()); cmp != 0 {
		return reject(CodeReferenceAmountMismatch), nil
	}
	if !referenceKindAllowed(op.Kind(), ref.Kind()) {
		return reject(CodeInvalidReferenceKind), nil
	}
	if refReversed {
		return reject(CodeAlreadyReversed), nil
	}
	if op.Kind() == KindRollback && (ref.Kind() == KindWin || ref.Kind() == KindRefund) {
		if !balance.Valid() {
			return wait, domain.ErrUninitialized
		}
		if cmp, err := balance.Cmp(op.Amount()); err != nil {
			return wait, err
		} else if cmp < 0 {
			return reject(CodeReversalInsufficientFunds), nil
		}
		return ReversalOutcome{Action: ReversalApplyDebit}, nil
	}
	return ReversalOutcome{Action: ReversalApplyCredit}, nil
}

// referenceKindAllowed diz quais alvos cada tipo aceita: reembolso só
// desfaz aposta; estorno desfaz aposta, prêmio ou reembolso (nunca outro
// estorno, derrota informativa ou abertura); prêmio com referência só cita
// aposta da mesma rodada.
func referenceKindAllowed(op, ref Kind) bool {
	switch op {
	case KindRefund:
		return ref == KindBet
	case KindRollback:
		return ref == KindBet || ref == KindWin || ref == KindRefund
	case KindWin:
		return ref == KindBet
	default:
		return false
	}
}
