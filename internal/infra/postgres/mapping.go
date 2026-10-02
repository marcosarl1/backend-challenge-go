package postgres

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/marcosarl1/backend-challenge-go/internal/domain"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
)

// toPGUUID converte para o tipo do banco sem mágica de codec.
func toPGUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: id != uuid.Nil}
}

// fromPGUUID volta para UUID de domínio, recusando nulo.
func fromPGUUID(field string, id pgtype.UUID) (uuid.UUID, error) {
	if !id.Valid {
		return uuid.Nil, fmt.Errorf("%w: %s nulo", domain.ErrUninitialized, field)
	}
	return id.Bytes, nil
}

// moneyFromRow monta Money validado da linha (centavos + código).
func moneyFromRow(field string, minor int64, code string) (money.Money, error) {
	m, err := money.FromMinor(minor, money.Currency(code))
	if err != nil {
		return money.Money{}, fmt.Errorf("%w: %s: %v", domain.ErrInvalidMoney, field, err)
	}
	return m, nil
}
