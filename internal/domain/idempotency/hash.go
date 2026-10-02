package idempotency

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/marcosarl1/backend-challenge-go/internal/domain"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
)

// Operation resume os campos de negócio que identificam uma operação. É o que entra no hash, nunca a chave de idempotência nem metadados de transporte (messageId, occurredAt, headers, correlação). Tanto o HTTP quanto o SQS montam este mesmo resumo, então o mesmo comando pelos dois canais gera o mesmo hash.
type Operation struct {
	ProviderID   string
	ExternalID   string
	PlayerID     string
	WalletID     string
	RoundID      string
	GameID       string
	Kind         string
	Amount       string // forma canônica ("25.00");
	Currency     string
	ReferenceExt string // vazio = sem referência (some do cálculo)
}

// Hash resume a operação em SHA-256 sobre JSON canônico: chaves ordenadas, sem espaços e sem escape de HTML. Como a entrada externa só aceita a forma canônica do dinheiro, não há normalização antes do hash, duas escritas diferentes do mesmo valor nunca chegam até aqui.
func Hash(op Operation) ([32]byte, error) {
	var empty [32]byte
	for field, value := range map[string]string{
		"provedor": op.ProviderID, "externo": op.ExternalID,
		"jogador": op.PlayerID, "carteira": op.WalletID,
		"rodada": op.RoundID, "jogo": op.GameID, "tipo": op.Kind,
	} {
		if value == "" {
			return empty, fmt.Errorf("%w: %s vazio no hash", domain.ErrInvalidMoney, field)
		}
	}
	if _, err := money.Parse(op.Amount, money.Currency(op.Currency)); err != nil {
		return empty, err
	}
	doc := map[string]any{
		"providerId":            op.ProviderID,
		"externalTransactionId": op.ExternalID,
		"playerId":              op.PlayerID,
		"walletId":              op.WalletID,
		"roundId":               op.RoundID,
		"gameId":                op.GameID,
		"kind":                  op.Kind,
		"money": map[string]any{
			"amount":   op.Amount,
			"currency": op.Currency,
		},
	}
	// Referência ausente e referência nula convergem: o campo some do cálculo nos dois casos.
	if op.ReferenceExt != "" {
		doc["referenceExternalTransactionId"] = op.ReferenceExt
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return empty, fmt.Errorf("%w: %v", domain.ErrInvalidMoney, err)
	}
	return sha256.Sum256(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))), nil
}

// HashHex é o Hash em hexadecimal
func HashHex(op Operation) (string, error) {
	sum, err := Hash(op)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sum[:]), nil
}
