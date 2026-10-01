package wager

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/domain"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
)

/*
* ExternalParams carrega tudo que identifica uma operação vinda de fora
(HTTP ou fila). A chave de idempotência é a informada pelo chamador; o
hash resume o conteúdo para detectar chave reutilizada com outro conteúdo.
*/
type ExternalParams struct {
	ID             uuid.UUID
	ProviderID     string
	ExternalID     string
	IdempotencyKey string
	PayloadHash    []byte
	WalletID       uuid.UUID
	PlayerID       uuid.UUID
	RoundID        string
	GameID         string
	Kind           Kind
	Amount         money.Money
	ReferenceExtID string // vazio = sem referência
}

// OpeningParams carrega o crédito inicial interno: sem provedor, sem chave, sem hash, sem rodada, sem jogo e sem referência.
type OpeningParams struct {
	ID       uuid.UUID
	WalletID uuid.UUID
	PlayerID uuid.UUID
	Amount   money.Money
}

/*
 * WagerTransaction é uma operação sobre a carteira: aposta, prêmio, derrota
 informativa, reembolso, estorno ou a abertura interna. Nasce pendente (ou,
 no caso da abertura, já processada) e termina num estado final que não
 muda mais. Repetir a consulta de uma operação final devolve o gravado, sem
 reexecutar.
*/

type WagerTransaction struct {
	id             uuid.UUID
	providerID     string
	externalID     string
	idempotencyKey string
	payloadHash    []byte
	walletID       uuid.UUID
	playerID       uuid.UUID
	roundID        string
	gameID         string
	kind           Kind
	amount         money.Money
	referenceExtID string
	resolvedRefID  uuid.UUID
	status         Status
	failureCode    FailureCode
	failureDetail  string
	resultBalance  money.Money
	resultVersion  int64
	attempts       int
	nextAttemptAt  time.Time
	expiresAt      time.Time
	createdAt      time.Time
	updatedAt      time.Time
	resolvedAt     time.Time
}

/*
*NewExternal registra uma operação externa como pendente. OPENING não entra; LOSS exige valor zero; os demais tipos exigem valor positivo;
REFUND e ROLLBACK exigem referência, BET e LOSS não aceitam, WIN aceita.
*/
func NewExternal(p ExternalParams, now time.Time) (*WagerTransaction, error) {
	if p.ID == uuid.Nil || p.WalletID == uuid.Nil || p.PlayerID == uuid.Nil {
		return nil, fmt.Errorf("%w: identificador vazio na transação", domain.ErrUninitialized)
	}
	for field, value := range map[string]string{
		"provedor": p.ProviderID, "externo": p.ExternalID,
		"chave": p.IdempotencyKey, "rodada": p.RoundID, "jogo": p.GameID,
	} {
		if value == "" {
			return nil, fmt.Errorf("%w: %s vazio na transação", domain.ErrInvalidMoney, field)
		}
	}
	if len(p.PayloadHash) == 0 {
		return nil, fmt.Errorf("%w: hash vazio na transação", domain.ErrInvalidMoney)
	}
	if now.IsZero() {
		return nil, fmt.Errorf("%w: instante vazio na transação", domain.ErrUninitialized)
	}
	if !p.Amount.Valid() {
		return nil, domain.ErrUninitialized
	}
	switch p.Kind {
	case KindBet, KindWin, KindRefund, KindRollback:
		zero, _ := money.Zero(p.Amount.Currency())
		if cmp, _ := p.Amount.Cmp(zero); cmp <= 0 {
			return nil, fmt.Errorf("%w: %s exige valor positivo", domain.ErrInvalidMoney, p.Kind)
		}
	case KindLoss:
		zero, _ := money.Zero(p.Amount.Currency())
		if cmp, _ := p.Amount.Cmp(zero); cmp != 0 {
			return nil, fmt.Errorf("%w: LOSS exige valor zero", domain.ErrInvalidMoney)
		}
	case KindOpening:
		return nil, fmt.Errorf("%w: abertura não entra por fora", domain.ErrInvalidKind)
	default:
		return nil, fmt.Errorf("%w: %q", domain.ErrInvalidKind, string(p.Kind))
	}
	switch p.Kind {
	case KindRefund, KindRollback:
		if p.ReferenceExtID == "" {
			return nil, fmt.Errorf("%w: %s exige referência", domain.ErrInvalidMoney, p.Kind)
		}
	case KindBet, KindLoss:
		if p.ReferenceExtID != "" {
			return nil, fmt.Errorf("%w: %s não aceita referência", domain.ErrInvalidMoney, p.Kind)
		}
	}
	return &WagerTransaction{
		id: p.ID, providerID: p.ProviderID, externalID: p.ExternalID,
		idempotencyKey: p.IdempotencyKey, payloadHash: append([]byte(nil), p.PayloadHash...),
		walletID: p.WalletID, playerID: p.PlayerID, roundID: p.RoundID, gameID: p.GameID,
		kind: p.Kind, amount: p.Amount, referenceExtID: p.ReferenceExtID,
		status: StatusPending, createdAt: now, updatedAt: now,
	}, nil
}

// NewOpening registra o crédito inicial interno, já processado: não há provedor, chave, hash, rodada, jogo nem referência nessa origem.
func NewOpening(p OpeningParams, now time.Time) (*WagerTransaction, error) {
	if p.ID == uuid.Nil || p.WalletID == uuid.Nil || p.PlayerID == uuid.Nil {
		return nil, fmt.Errorf("%w: identificador vazio na abertura", domain.ErrUninitialized)
	}
	if !p.Amount.Valid() {
		return nil, domain.ErrUninitialized
	}
	zero, _ := money.Zero(p.Amount.Currency())
	if cmp, _ := p.Amount.Cmp(zero); cmp <= 0 {
		return nil, fmt.Errorf("%w: abertura exige valor positivo", domain.ErrInvalidMoney)
	}
	if now.IsZero() {
		return nil, fmt.Errorf("%w: instante vazio na abertura", domain.ErrUninitialized)
	}
	return &WagerTransaction{
		id: p.ID, walletID: p.WalletID, playerID: p.PlayerID,
		kind: KindOpening, amount: p.Amount,
		status: StatusProcessed, resultBalance: p.Amount, resultVersion: 1,
		createdAt: now, updatedAt: now, resolvedAt: now,
	}, nil
}

// terminal recusa qualquer transição a partir de estado final.
func (t *WagerTransaction) terminal(to string) *domain.TransitionError {
	return domain.TerminalTransition(string(t.status), to)
}

// MarkProcessed conclui com sucesso, guardando o saldo e a versão da carteira observados — é esse retrato que a repetição devolve depois.
func (t *WagerTransaction) MarkProcessed(balance money.Money, walletVersion int64, now time.Time) error {
	if t.isTerminal() {
		return t.terminal(string(StatusProcessed))
	}
	if t.status != StatusPending && t.status != StatusPendingReference {
		return domain.InvalidTransition(string(t.status), string(StatusProcessed))
	}
	if !balance.Valid() {
		return domain.ErrUninitialized
	}
	if cur := balance.Currency(); cur != t.amount.Currency() {
		return fmt.Errorf("%w: %q vs %q", domain.ErrCurrencyMismatch, cur, t.amount.Currency())
	}
	if walletVersion < 1 {
		return fmt.Errorf("%w: versão %d", domain.ErrInvalidMoney, walletVersion)
	}
	if now.IsZero() {
		return fmt.Errorf("%w: instante vazio na conclusão", domain.ErrUninitialized)
	}
	t.status = StatusProcessed
	t.resultBalance = balance
	t.resultVersion = walletVersion
	t.updatedAt = now
	t.resolvedAt = now
	return nil
}

// Reject recusa por regra de negócio, com código estável do catálogo.
func (t *WagerTransaction) Reject(code FailureCode, detail string, now time.Time) error {
	if t.isTerminal() {
		return t.terminal(string(StatusRejected))
	}
	if t.status != StatusPending && t.status != StatusPendingReference {
		return domain.InvalidTransition(string(t.status), string(StatusRejected))
	}
	if !code.Valid() {
		return fmt.Errorf("%w: código %q", domain.ErrInvalidMoney, string(code))
	}
	if now.IsZero() {
		return fmt.Errorf("%w: instante vazio na rejeição", domain.ErrUninitialized)
	}
	t.status = StatusRejected
	t.failureCode = code
	t.failureDetail = detail
	t.updatedAt = now
	t.resolvedAt = now
	return nil
}

/*
* WaitForReference põe a operação em espera pela referência ainda ausente,
com a próxima tentativa e o prazo final. Só vale saindo de pendente; quem
já espera usa Reschedule.
*/
func (t *WagerTransaction) WaitForReference(nextAttempt, expiresAt, now time.Time) error {
	if t.isTerminal() {
		return t.terminal(string(StatusPendingReference))
	}
	if t.status != StatusPending {
		return domain.InvalidTransition(string(t.status), string(StatusPendingReference))
	}
	if nextAttempt.IsZero() || expiresAt.IsZero() || now.IsZero() {
		return fmt.Errorf("%w: instante vazio na espera", domain.ErrUninitialized)
	}
	if !expiresAt.After(nextAttempt) {
		return fmt.Errorf("%w: prazo precisa vir depois da tentativa", domain.ErrInvalidMoney)
	}
	t.status = StatusPendingReference
	t.attempts++
	t.nextAttemptAt = nextAttempt
	t.expiresAt = expiresAt
	t.updatedAt = now
	return nil
}

// Reschedule remarca uma espera existente, contando mais uma tentativa.
func (t *WagerTransaction) Reschedule(nextAttempt, now time.Time) error {
	if t.isTerminal() {
		return t.terminal(string(StatusPendingReference))
	}
	if t.status != StatusPendingReference {
		return domain.InvalidTransition(string(t.status), string(StatusPendingReference))
	}
	if nextAttempt.IsZero() || now.IsZero() {
		return fmt.Errorf("%w: instante vazio no reagendamento", domain.ErrUninitialized)
	}
	t.attempts++
	t.nextAttemptAt = nextAttempt
	t.updatedAt = now
	return nil
}

/*
* Fail registra falha permanente de infraestrutura. É o único destino que
aceita só o código de falha interna: qualquer outro motivo ou é rejeição
de negócio ou é transitório (que nem muda o estado).
*/
func (t *WagerTransaction) Fail(now time.Time) error {
	if t.isTerminal() {
		return t.terminal(string(StatusFailed))
	}
	if t.status != StatusPending && t.status != StatusPendingReference {
		return domain.InvalidTransition(string(t.status), string(StatusFailed))
	}
	if now.IsZero() {
		return fmt.Errorf("%w: instante vazio na falha", domain.ErrUninitialized)
	}
	t.status = StatusFailed
	t.failureCode = CodeInternalPermanentFailure
	t.updatedAt = now
	t.resolvedAt = now
	return nil
}

// ResolveReference guarda a referência interna resolvida. Vale antes de terminar; depois do fim, nem a referência se toca mais.
func (t *WagerTransaction) ResolveReference(id uuid.UUID) error {
	if t.isTerminal() {
		return t.terminal("RESOLVE_REFERENCE")
	}
	if id == uuid.Nil {
		return fmt.Errorf("%w: referência vazia", domain.ErrUninitialized)
	}
	t.resolvedRefID = id
	return nil
}

func (t *WagerTransaction) isTerminal() bool {
	return t.status == StatusProcessed || t.status == StatusRejected || t.status == StatusFailed
}

// Leitores.
func (t *WagerTransaction) ID() uuid.UUID          { return t.id }
func (t *WagerTransaction) ProviderID() string     { return t.providerID }
func (t *WagerTransaction) ExternalID() string     { return t.externalID }
func (t *WagerTransaction) IdempotencyKey() string { return t.idempotencyKey }
func (t *WagerTransaction) WalletID() uuid.UUID    { return t.walletID }
func (t *WagerTransaction) PlayerID() uuid.UUID    { return t.playerID }
func (t *WagerTransaction) RoundID() string        { return t.roundID }
func (t *WagerTransaction) GameID() string         { return t.gameID }
func (t *WagerTransaction) Kind() Kind             { return t.kind }
func (t *WagerTransaction) Amount() money.Money    { return t.amount }
func (t *WagerTransaction) ReferenceExtID() string { return t.referenceExtID }
func (t *WagerTransaction) Status() Status         { return t.status }
func (t *WagerTransaction) Attempts() int          { return t.attempts }
func (t *WagerTransaction) CreatedAt() time.Time   { return t.createdAt }
func (t *WagerTransaction) UpdatedAt() time.Time   { return t.updatedAt }
func (t *WagerTransaction) ResolvedAt() time.Time  { return t.resolvedAt }

// PayloadHash devolve cópia do hash (o original não sai daqui).
func (t *WagerTransaction) PayloadHash() []byte {
	return append([]byte(nil), t.payloadHash...)
}

// ResolvedRefID devolve a referência interna, se já resolvida.
func (t *WagerTransaction) ResolvedRefID() (uuid.UUID, bool) {
	return t.resolvedRefID, t.resolvedRefID != uuid.Nil
}

// NextAttempt devolve a próxima tentativa e o prazo, quando em espera.
func (t *WagerTransaction) NextAttempt() (next, expires time.Time, ok bool) {
	if t.status != StatusPendingReference {
		return time.Time{}, time.Time{}, false
	}
	return t.nextAttemptAt, t.expiresAt, true
}

// Result devolve o retrato guardado na conclusão. Só existe em PROCESSED.
func (t *WagerTransaction) Result() (money.Money, int64, error) {
	if t.status != StatusProcessed {
		return money.Money{}, 0, domain.InvalidTransition(string(t.status), "RESULT")
	}
	return t.resultBalance, t.resultVersion, nil
}

// Failure devolve código e detalhe. Só existe em REJECTED ou FAILED.
func (t *WagerTransaction) Failure() (FailureCode, string, error) {
	if t.status != StatusRejected && t.status != StatusFailed {
		return "", "", domain.InvalidTransition(string(t.status), "FAILURE")
	}
	return t.failureCode, t.failureDetail, nil
}
