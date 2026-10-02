package application

import "errors"

var (
	// ErrWalletExists indica carteira já aberta para jogador e moeda.
	ErrWalletExists = errors.New("aplicação: carteira já existe para jogador e moeda")
	// ErrInvalidInput indica comando malformado (antes de qualquer escrita).
	ErrInvalidInput = errors.New("aplicação: entrada inválida")
	// ErrNotFound indica recurso inexistente (ou de outro dono).
	ErrNotFound = errors.New("aplicação: não encontrado")
	// ErrForbidden indica acesso negado.
	ErrForbidden = errors.New("aplicação: acesso negado")
	// ErrIdempotencyMismatch indica chave reutilizada com conteúdo diferente.
	ErrIdempotencyMismatch = errors.New("aplicação: chave com outro conteúdo")
	// ErrExternalIDReused indica id externo com outra chave.
	ErrExternalIDReused = errors.New("aplicação: id externo com outra chave")
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

// AsType classifica com errors.As devolvendo o valor tipado, sem o
// temporário `var alvo *Tipo`.
func AsType[T error](err error) (T, bool) {
	var target T
	if errors.As(err, &target) {
		return target, true
	}
	var zero T
	return zero, false
}
