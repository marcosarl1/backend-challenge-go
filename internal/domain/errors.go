package domain

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidMoney     = errors.New("domínio: valor inválido")
	ErrInvalidCurrency  = errors.New("domínio: moeda inválida")
	ErrUninitialized    = errors.New("domínio: valor não inicializado")
	ErrCurrencyMismatch = errors.New("domínio: moedas incompatíveis")
	ErrOverflow         = errors.New("domínio: estouro numérico")

	ErrInsufficientFunds = errors.New("domínio: saldo insuficiente")
	ErrAlreadyOpened     = errors.New("domínio: abertura já aplicada")
	ErrInvalidTransition = errors.New("domínio: transição de estado inválida")
	ErrTerminalState     = errors.New("domínio: estado final não muda")
)

/*
TransitionError descreve uma transição de estado recusada, com a origem e o destino pretendido.
Quando a origem já é um estado final,
o erro se classifica também como ErrTerminalState; nos demais casos, como ErrInvalidTransition
*/
type TransitionError struct {
	From     string
	To       string
	Terminal bool
}

// InvalidTransition constrói o erro para uma transição proibida entre dois estados não finais.
func InvalidTransition(from, to string) *TransitionError {
	return &TransitionError{From: from, To: to}
}

// TerminalTransition constrói o erro para qualquer tentativa de sair de um estado final.
func TerminalTransition(from, to string) *TransitionError {
	return &TransitionError{From: from, To: to, Terminal: true}
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("transição inválida: %s -> %s", e.From, e.To)
}

// Unwrap permite classificar com errors.Is: estado final vira
// ErrTerminalState, o resto vira ErrInvalidTransition.
func (e *TransitionError) Unwrap() error {
	if e.Terminal {
		return ErrTerminalState
	}
	return ErrInvalidTransition
}
