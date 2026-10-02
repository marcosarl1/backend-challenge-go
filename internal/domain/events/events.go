package events

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/domain"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
)

// Tipos de evento. O tipo e a versão nascem no construtor, nunca em quem publica: o contrato não depende de ninguém acertar a string.
const (
	TypeTransactionProcessed        = "WagerTransactionProcessed"
	TypeTransactionRejected         = "WagerTransactionRejected"
	TypeBalanceChanged              = "WalletBalanceChanged"
	TypeTransactionPendingReference = "WagerTransactionPendingReference"
)

// Version é a versão atual de todos os eventos. Quando um payload mudar de forma incompatível, o tipo ganha um construtor novo com versão maior.
const Version = 1

// Direction repete o sentido do lançamento dentro do evento, sem amarrar este pacote ao agregado da carteira.
type Direction string

const (
	DirectionDebit  Direction = "DEBIT"
	DirectionCredit Direction = "CREDIT"
)

// Envelope é a capa de todo evento: identidade estável (republicação mantém o mesmo eventId), tipo, agregado de origem, correlação, causa opcional, instante em UTC e o payload tipado. O payload viaja como valor, não como referência: depois de construído, ninguém o altera.
type Envelope[T any] struct {
	EventID       uuid.UUID `json:"eventId"`
	EventType     string    `json:"eventType"`
	AggregateID   uuid.UUID `json:"aggregateId"`
	CorrelationID string    `json:"correlationId"`
	CausationID   string    `json:"causationId,omitempty"`
	OccurredAt    time.Time `json:"occurredAt"`
	Version       int       `json:"version"`
	Data          T         `json:"data"`
}

// PayloadJSON serializa o envelope pronto para gravar na outbox como um retrato imutável.
func (e Envelope[T]) PayloadJSON() ([]byte, error) {
	return json.Marshal(e)
}

func checkBase(eventID, aggregateID uuid.UUID, correlationID string, occurredAt time.Time) (time.Time, error) {
	if eventID == uuid.Nil || aggregateID == uuid.Nil {
		return time.Time{}, fmt.Errorf("%w: identificador vazio no evento", domain.ErrUninitialized)
	}
	if correlationID == "" {
		return time.Time{}, fmt.Errorf("%w: correlação vazia no evento", domain.ErrInvalidMoney)
	}
	if occurredAt.IsZero() {
		return time.Time{}, fmt.Errorf("%w: instante vazio no evento", domain.ErrUninitialized)
	}
	return occurredAt.UTC(), nil
}

// ProcessedData descreve uma operação concluída. Os campos externos ficam de fora quando vazios: a abertura interna não carrega metadado externo.
type ProcessedData struct {
	TransactionID       uuid.UUID   `json:"transactionId"`
	ProviderID          string      `json:"providerId,omitempty"`
	ExternalTxID        string      `json:"externalTransactionId,omitempty"`
	WalletID            uuid.UUID   `json:"walletId"`
	PlayerID            uuid.UUID   `json:"playerId"`
	RoundID             string      `json:"roundId,omitempty"`
	GameID              string      `json:"gameId,omitempty"`
	Kind                string      `json:"kind"`
	Amount              money.Money `json:"money"`
	ResultBalance       money.Money `json:"resultBalance"`
	ResultWalletVersion int64       `json:"resultWalletVersion"`
}

// NewTransactionProcessed monta o evento de operação concluída, incluindo LOSS (que não mexe no saldo, mas conclui). O agregado é a transação.
func NewTransactionProcessed(eventID, txID uuid.UUID, correlationID, causationID string, occurredAt time.Time, data ProcessedData) (Envelope[ProcessedData], error) {
	var empty Envelope[ProcessedData]
	when, err := checkBase(eventID, txID, correlationID, occurredAt)
	if err != nil {
		return empty, err
	}
	if data.TransactionID != txID {
		return empty, fmt.Errorf("%w: transação do payload diverge do agregado", domain.ErrInvalidMoney)
	}
	if data.WalletID == uuid.Nil || data.PlayerID == uuid.Nil {
		return empty, fmt.Errorf("%w: identificador vazio no evento", domain.ErrUninitialized)
	}
	if data.Kind == "" {
		return empty, fmt.Errorf("%w: tipo vazio no evento", domain.ErrInvalidMoney)
	}
	if !data.Amount.Valid() || !data.ResultBalance.Valid() {
		return empty, domain.ErrUninitialized
	}
	if data.ResultWalletVersion < 1 {
		return empty, fmt.Errorf("%w: versão %d", domain.ErrInvalidMoney, data.ResultWalletVersion)
	}
	return Envelope[ProcessedData]{
		EventID: eventID, EventType: TypeTransactionProcessed, AggregateID: txID,
		CorrelationID: correlationID, CausationID: causationID,
		OccurredAt: when, Version: Version, Data: data,
	}, nil
}

// RejectedData soma ao ProcessedData o código estável e o detalhe.
type RejectedData struct {
	ProcessedData
	FailureCode   string `json:"failureCode"`
	FailureDetail string `json:"failureDetail,omitempty"`
}

// NewTransactionRejected monta o evento de rejeição definitiva. O agregado é a transação.
func NewTransactionRejected(eventID, txID uuid.UUID, correlationID, causationID string, occurredAt time.Time, data RejectedData) (Envelope[RejectedData], error) {
	var empty Envelope[RejectedData]
	if _, err := NewTransactionProcessed(eventID, txID, correlationID, causationID, occurredAt, data.ProcessedData); err != nil {
		return empty, err
	}
	if data.FailureCode == "" {
		return empty, fmt.Errorf("%w: código vazio no evento", domain.ErrInvalidMoney)
	}
	return Envelope[RejectedData]{
		EventID: eventID, EventType: TypeTransactionRejected, AggregateID: txID,
		CorrelationID: correlationID, CausationID: causationID,
		OccurredAt: occurredAt.UTC(), Version: Version, Data: data,
	}, nil
}

// BalanceChangedData descreve uma mudança efetiva de saldo. Só existe quando houve movimento: LOSS e rejeições não geram este evento.
type BalanceChangedData struct {
	WalletID      uuid.UUID   `json:"walletId"`
	TransactionID uuid.UUID   `json:"transactionId"`
	Direction     Direction   `json:"direction"`
	Amount        money.Money `json:"money"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	WalletVersion int64       `json:"walletVersion"`
}

// NewBalanceChanged monta o evento de saldo. O agregado é a carteira, para que o consumo ordene por carteira.
func NewBalanceChanged(eventID, walletID uuid.UUID, correlationID, causationID string, occurredAt time.Time, data BalanceChangedData) (Envelope[BalanceChangedData], error) {
	var empty Envelope[BalanceChangedData]
	when, err := checkBase(eventID, walletID, correlationID, occurredAt)
	if err != nil {
		return empty, err
	}
	if data.WalletID != walletID {
		return empty, fmt.Errorf("%w: carteira do payload diverge do agregado", domain.ErrInvalidMoney)
	}
	if data.TransactionID == uuid.Nil {
		return empty, fmt.Errorf("%w: transação vazia no evento", domain.ErrUninitialized)
	}
	if data.Direction != DirectionDebit && data.Direction != DirectionCredit {
		return empty, fmt.Errorf("%w: direção %q", domain.ErrInvalidMoney, string(data.Direction))
	}
	if !data.Amount.Valid() || !data.BalanceBefore.Valid() || !data.BalanceAfter.Valid() {
		return empty, domain.ErrUninitialized
	}
	if data.WalletVersion < 1 {
		return empty, fmt.Errorf("%w: versão %d", domain.ErrInvalidMoney, data.WalletVersion)
	}
	return Envelope[BalanceChangedData]{
		EventID: eventID, EventType: TypeBalanceChanged, AggregateID: walletID,
		CorrelationID: correlationID, CausationID: causationID,
		OccurredAt: when, Version: Version, Data: data,
	}, nil
}

// PendingReferenceData descreve uma operação em espera pela referência, com a próxima tentativa, o prazo e quantas tentativas já houve.
type PendingReferenceData struct {
	TransactionID  uuid.UUID `json:"transactionId"`
	ProviderID     string    `json:"providerId"`
	ExternalTxID   string    `json:"externalTransactionId"`
	WalletID       uuid.UUID `json:"walletId"`
	ReferenceExtID string    `json:"referenceExternalTransactionId"`
	Attempts       int       `json:"attempts"`
	NextAttemptAt  time.Time `json:"nextAttemptAt"`
	ExpiresAt      time.Time `json:"expiresAt"`
}

// NewTransactionPendingReference monta o evento de espera. O agregado é a transação.
func NewTransactionPendingReference(eventID, txID uuid.UUID, correlationID string, occurredAt time.Time, data PendingReferenceData) (Envelope[PendingReferenceData], error) {
	var empty Envelope[PendingReferenceData]
	when, err := checkBase(eventID, txID, correlationID, occurredAt)
	if err != nil {
		return empty, err
	}
	if data.TransactionID != txID {
		return empty, fmt.Errorf("%w: transação do payload diverge do agregado", domain.ErrInvalidMoney)
	}
	if data.ProviderID == "" || data.ExternalTxID == "" || data.ReferenceExtID == "" {
		return empty, fmt.Errorf("%w: referência incompleta no evento", domain.ErrInvalidMoney)
	}
	if data.WalletID == uuid.Nil {
		return empty, fmt.Errorf("%w: carteira vazia no evento", domain.ErrUninitialized)
	}
	if data.NextAttemptAt.IsZero() || data.ExpiresAt.IsZero() {
		return empty, fmt.Errorf("%w: instante vazio no evento", domain.ErrUninitialized)
	}
	return Envelope[PendingReferenceData]{
		EventID: eventID, EventType: TypeTransactionPendingReference, AggregateID: txID,
		CorrelationID: correlationID,
		OccurredAt:    when, Version: Version, Data: data,
	}, nil
}
