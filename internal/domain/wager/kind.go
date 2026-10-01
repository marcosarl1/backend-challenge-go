package wager

// Kind é o tipo da operação.
type Kind string

const (
	KindOpening  Kind = "OPENING"
	KindBet      Kind = "BET"
	KindWin      Kind = "WIN"
	KindLoss     Kind = "LOSS"
	KindRefund   Kind = "REFUND"
	KindRollback Kind = "ROLLBACK"
)

// Status é o estado da transação.
type Status string

const (
	StatusPending          Status = "PENDING"
	StatusPendingReference Status = "PENDING_REFERENCE"
	StatusProcessed        Status = "PROCESSED"
	StatusRejected         Status = "REJECTED"
	StatusFailed           Status = "FAILED"
)

// FailureCode é o código estável de uma rejeição ou falha.
type FailureCode string

const (
	CodeInsufficientFunds         FailureCode = "INSUFFICIENT_FUNDS"
	CodeReversalInsufficientFunds FailureCode = "REVERSAL_INSUFFICIENT_FUNDS"
	CodeReferenceNotFound         FailureCode = "REFERENCE_NOT_FOUND"
	CodeReferenceNotProcessed     FailureCode = "REFERENCE_NOT_PROCESSED"
	CodeReferenceMismatch         FailureCode = "REFERENCE_MISMATCH"
	CodeReferenceAmountMismatch   FailureCode = "REFERENCE_AMOUNT_MISMATCH"
	CodeInvalidReferenceKind      FailureCode = "INVALID_REFERENCE_KIND"
	CodeAlreadyReversed           FailureCode = "ALREADY_REVERSED"
	CodeWalletMismatch            FailureCode = "WALLET_MISMATCH"
	CodeCurrencyMismatch          FailureCode = "CURRENCY_MISMATCH"
	CodeInternalPermanentFailure  FailureCode = "INTERNAL_PERMANENT_FAILURE"
)

// Valid informa se c é um código conhecido do catálogo.
func (c FailureCode) Valid() bool {
	switch c {
	case CodeInsufficientFunds,
		CodeReversalInsufficientFunds,
		CodeReferenceNotFound,
		CodeReferenceNotProcessed,
		CodeReferenceMismatch,
		CodeReferenceAmountMismatch,
		CodeInvalidReferenceKind,
		CodeAlreadyReversed,
		CodeWalletMismatch,
		CodeCurrencyMismatch,
		CodeInternalPermanentFailure:
		return true
	default:
		return false
	}
}
