package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// Class diz o destino de um erro: tentar de novo, desistir com conflito de regra, ou registrar como falha.
type Class string

const (
	// ClassTransient pede repetição (serialização, deadlock, banco fora do ar, prazo estourado): nada foi decidido ainda.
	ClassTransient Class = "transient"
	// ClassConstraint é violação de unicidade ou CHECK: outro caminho
	// (idempotência, conflito) resolve, repetir não adianta.
	ClassConstraint Class = "constraint"
	// ClassPermanent é o resto: erro de programa, permissão, SQL inválido.
	ClassPermanent Class = "permanent"
)

// ConstraintError carrega a restrição violada para classificar com errors.As.
type ConstraintError struct {
	Code       string
	Constraint string
	Err        error
}

func (e *ConstraintError) Error() string {
	return "restrição " + e.Constraint + " (" + e.Code + "): " + e.Err.Error()
}

func (e *ConstraintError) Unwrap() error { return e.Err }

// Classify separa o erro por destino. nil não classifica (volta permanente, para ninguém repetir à toa).
func Classify(err error) Class {
	if err == nil {
		return ClassPermanent
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ClassTransient
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return ClassPermanent
	}
	switch pgErr.Code {
	case "40001", "40P01":
		// Serialização e deadlock: a repetição pega outra ordem.
		return ClassTransient
	case "08000", "08003", "08006", "08001", "08004", "08007", "08P01",
		"57P01", "57P02", "57P03", "53300", "53100", "53200", "53400":
		// Conexão e banco indisponível.
		return ClassTransient
	case "23505", "23503", "23502", "23514", "23P01":
		// Unicidade, chave estrangeira, não-nulo, CHECK e exclusão.
		return ClassConstraint
	default:
		return ClassPermanent
	}
}

// AsConstraint extrai código e nome da restrição de um erro do banco.
func AsConstraint(err error) (*ConstraintError, bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil, false
	}
	switch pgErr.Code {
	case "23505", "23503", "23502", "23514", "23P01":
		return &ConstraintError{Code: pgErr.Code, Constraint: pgErr.ConstraintName, Err: err}, true
	default:
		return nil, false
	}
}
