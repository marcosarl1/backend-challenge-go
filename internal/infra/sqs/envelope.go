package sqs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/domain"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/idempotency"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wager"
)

// envelopeIn é o formato que chega na fila de entrada. Rígido de propósito: campo desconhecido ou fora do formato é veneno, não chute.
type envelopeIn struct {
	MessageID  string `json:"messageId"`
	Type       string `json:"type"`
	OccurredAt string `json:"occurredAt"`
	Data       struct {
		ProviderID     string `json:"providerId"`
		ExternalTxID   string `json:"externalTransactionId"`
		IdempotencyKey string `json:"idempotencyKey"`
		PlayerID       string `json:"playerId"`
		WalletID       string `json:"walletId"`
		RoundID        string `json:"roundId"`
		GameID         string `json:"gameId"`
		Kind           string `json:"kind"`
		Money          struct {
			Amount   string `json:"amount"`
			Currency string `json:"currency"`
		} `json:"money"`
		ReferenceExtID string `json:"referenceExternalTransactionId"`
	} `json:"data"`
}

// parsedCommand é o comando pronto com o hash do conteúdo.
type parsedCommand struct {
	MessageID string
	Command   application.ProcessCommand
	Hash      []byte
}

const expectedEnvelopeType = "WagerTransactionRequested"

// parseEnvelope valida e converte. Erro aqui é permanente: a mensagem nunca vai prestar, então vai para a DLQ com o motivo.
func parseEnvelope(raw []byte) (parsedCommand, []byte, error) {
	var empty parsedCommand
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var in envelopeIn
	if err := dec.Decode(&in); err != nil {
		return empty, nil, fmt.Errorf("%w: %v", application.ErrInvalidInput, err)
	}
	if in.Type != expectedEnvelopeType {
		return empty, nil, fmt.Errorf("%w: tipo %q", application.ErrInvalidInput, in.Type)
	}
	if in.MessageID == "" {
		return empty, nil, fmt.Errorf("%w: messageId vazio", application.ErrInvalidInput)
	}
	if _, err := time.Parse(time.RFC3339, in.OccurredAt); err != nil {
		return empty, nil, fmt.Errorf("%w: occurredAt: %v", application.ErrInvalidInput, err)
	}
	playerID, err := uuid.Parse(in.Data.PlayerID)
	if err != nil {
		return empty, nil, fmt.Errorf("%w: playerId: %v", application.ErrInvalidInput, err)
	}
	walletID, err := uuid.Parse(in.Data.WalletID)
	if err != nil {
		return empty, nil, fmt.Errorf("%w: walletId: %v", application.ErrInvalidInput, err)
	}
	amount, err := money.Parse(in.Data.Money.Amount, money.Currency(in.Data.Money.Currency))
	if err != nil {
		return empty, nil, fmt.Errorf("%w: money: %v", application.ErrInvalidInput, err)
	}
	for field, value := range map[string]string{
		"providerId": in.Data.ProviderID, "externalTransactionId": in.Data.ExternalTxID,
		"idempotencyKey": in.Data.IdempotencyKey, "roundId": in.Data.RoundID, "gameId": in.Data.GameID,
	} {
		if value == "" {
			return empty, nil, fmt.Errorf("%w: %s vazio", application.ErrInvalidInput, field)
		}
	}
	hash, err := idempotency.Hash(idempotency.Operation{
		ProviderID: in.Data.ProviderID, ExternalID: in.Data.ExternalTxID,
		PlayerID: playerID.String(), WalletID: walletID.String(),
		RoundID: in.Data.RoundID, GameID: in.Data.GameID, Kind: in.Data.Kind,
		Amount: in.Data.Money.Amount, Currency: in.Data.Money.Currency,
		ReferenceExt: in.Data.ReferenceExtID,
	})
	if err != nil {
		return empty, nil, fmt.Errorf("%w: %v", application.ErrInvalidInput, err)
	}
	return parsedCommand{
		MessageID: in.MessageID,
		Command: application.ProcessCommand{
			ProviderID: in.Data.ProviderID, ExternalID: in.Data.ExternalTxID,
			IdempotencyKey: in.Data.IdempotencyKey, PlayerID: playerID, WalletID: walletID,
			RoundID: in.Data.RoundID, GameID: in.Data.GameID, Kind: wager.Kind(in.Data.Kind),
			Amount: amount, ReferenceExternalID: in.Data.ReferenceExtID,
			CorrelationID: in.MessageID,
		},
		Hash: hash[:],
	}, hash[:], nil
}

// isPermanent diz o que nunca vai prestar (vai para a DLQ). O resto é transitório: solta e tenta de novo (o redrive segura o limite).
func isPermanent(err error) bool {
	for _, sentinel := range []error{
		application.ErrInvalidInput,
		application.ErrIdempotencyMismatch,
		application.ErrExternalIDReused,
		application.ErrNotFound,
		application.ErrWalletExists,
		domain.ErrInvalidMoney,
		domain.ErrInvalidCurrency,
		domain.ErrInvalidKind,
		domain.ErrUninitialized,
	} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}
