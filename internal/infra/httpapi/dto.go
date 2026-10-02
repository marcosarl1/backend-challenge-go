package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wager"
)

// maxBody limita o corpo para não estourar memória com entrada gigante.
const maxBody = 1 << 20

// MoneyDTO é dinheiro no contrato: decimal como texto, nunca número.
type MoneyDTO struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func (m MoneyDTO) parse() (money.Money, error) {
	return money.Parse(m.Amount, money.Currency(m.Currency))
}

func moneyOf(m money.Money) MoneyDTO {
	return MoneyDTO{Amount: m.String(), Currency: string(m.Currency())}
}

// decode lê o JSON com limite de tamanho e sem campos desconhecidos.
func decode(r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("%w: corpo: %v", application.ErrInvalidInput, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: corpo com mais de um JSON", application.ErrInvalidInput)
	}
	return nil
}

// OpenWalletRequest abre carteira.
type OpenWalletRequest struct {
	PlayerID       string   `json:"playerId"`
	InitialBalance MoneyDTO `json:"initialBalance"`
}

// ProcessRequest envia operação. A chave vem no header, nunca no corpo.
type ProcessRequest struct {
	ProviderID            string   `json:"providerId"`
	ExternalTransactionID string   `json:"externalTransactionId"`
	PlayerID              string   `json:"playerId"`
	WalletID              string   `json:"walletId"`
	RoundID               string   `json:"roundId"`
	GameID                string   `json:"gameId"`
	Kind                  string   `json:"kind"`
	Money                 MoneyDTO `json:"money"`
	ReferenceExternalTxID string   `json:"referenceExternalTransactionId,omitempty"`
}

func parseUUID(field, value string) (uuid.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %s inválido", application.ErrInvalidInput, field)
	}
	return id, nil
}

func (b ProcessRequest) command(idempotencyKey, correlationID string) (application.ProcessCommand, error) {
	if idempotencyKey == "" {
		return application.ProcessCommand{}, fmt.Errorf("%w: Idempotency-Key obrigatório", application.ErrInvalidInput)
	}
	playerID, err := parseUUID("playerId", b.PlayerID)
	if err != nil {
		return application.ProcessCommand{}, err
	}
	walletID, err := parseUUID("walletId", b.WalletID)
	if err != nil {
		return application.ProcessCommand{}, err
	}
	amount, err := b.Money.parse()
	if err != nil {
		return application.ProcessCommand{}, fmt.Errorf("%w: %v", application.ErrInvalidInput, err)
	}
	for field, value := range map[string]string{
		"providerId": b.ProviderID, "externalTransactionId": b.ExternalTransactionID,
		"roundId": b.RoundID, "gameId": b.GameID,
	} {
		if value == "" {
			return application.ProcessCommand{}, fmt.Errorf("%w: %s vazio", application.ErrInvalidInput, field)
		}
	}
	kind := wager.Kind(b.Kind)
	switch kind {
	case wager.KindBet, wager.KindWin, wager.KindLoss, wager.KindRefund, wager.KindRollback:
	default:
		return application.ProcessCommand{}, fmt.Errorf("%w: tipo %q", application.ErrInvalidInput, b.Kind)
	}
	return application.ProcessCommand{
		ProviderID: b.ProviderID, ExternalID: b.ExternalTransactionID,
		IdempotencyKey: idempotencyKey, PlayerID: playerID, WalletID: walletID,
		RoundID: b.RoundID, GameID: b.GameID, Kind: kind,
		Amount: amount, ReferenceExternalID: b.ReferenceExternalTxID,
		CorrelationID: correlationID,
	}, nil
}
