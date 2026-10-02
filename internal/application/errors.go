package application

import "errors"

var (
	// ErrWalletExists indica carteira já aberta para jogador e moeda.
	ErrWalletExists = errors.New("aplicação: carteira já existe para jogador e moeda")
	// ErrInvalidInput indica comando malformado (antes de qualquer escrita).
	ErrInvalidInput = errors.New("aplicação: entrada inválida")
	// ErrNotFound indica recurso inexistente (ou de outro dono).
	ErrNotFound = errors.New("aplicação: não encontrado")
)

// ConflictError indica violação de unicidade vinda do banco, com a restrição que barrou. Quem chama classifica com errors.As.
type ConflictError struct {
	Constraint string
	Err        error
}

func (e *ConflictError) Error() string {
	return "conflito na restrição " + e.Constraint + ": " + e.Err.Error()
}

// Unwrap expõe o erro original.
func (e *ConflictError) Unwrap() error { return e.Err }
