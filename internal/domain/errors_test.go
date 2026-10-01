package domain

import (
	"errors"
	"fmt"
	"testing"
)

func TestSentinelsAreDistinct(t *testing.T) {
	sentinels := []error{
		ErrInvalidMoney,
		ErrInvalidCurrency,
		ErrUninitialized,
		ErrCurrencyMismatch,
		ErrOverflow,
		ErrInsufficientFunds,
		ErrAlreadyOpened,
		ErrInvalidTransition,
		ErrTerminalState,
	}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i != j && errors.Is(a, b) {
				t.Fatalf("sentinelas diferentes se confundem: %v e %v", a, b)
			}
		}
	}
}

func TestTransitionErrorClassification(t *testing.T) {
	invalid := InvalidTransition("PENDING", "OPENING")
	if !errors.Is(invalid, ErrInvalidTransition) {
		t.Fatalf("esperado ErrInvalidTransition, veio %v", invalid)
	}
	if errors.Is(invalid, ErrTerminalState) {
		t.Fatalf("transição comum não pode ser terminal: %v", invalid)
	}

	terminal := TerminalTransition("PROCESSED", "PENDING")
	if !errors.Is(terminal, ErrTerminalState) {
		t.Fatalf("esperado ErrTerminalState, veio %v", terminal)
	}

	// Através de embrulho com %w a classificação continua funcionando.
	wrapped := fmt.Errorf("processando aposta: %w", terminal)
	if !errors.Is(wrapped, ErrTerminalState) {
		t.Fatalf("embrulho perdeu a classificação: %v", wrapped)
	}

	// errors.As recupera a origem e o destino.
	var terr *TransitionError
	if !errors.As(wrapped, &terr) {
		t.Fatalf("esperado *TransitionError via errors.As: %v", wrapped)
	}
	if terr.From != "PROCESSED" || terr.To != "PENDING" {
		t.Fatalf("origem/destino errados: %+v", terr)
	}
}

func TestBusinessRejectionIsErrorNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("regra de negócio não pode usar panic: %v", r)
		}
	}()
	// Tentar sair de um estado final devolve erro, sem explodir.
	err := TerminalTransition("REJECTED", "PROCESSED")
	if !errors.Is(err, ErrTerminalState) {
		t.Fatalf("erro inesperado: %v", err)
	}
}
