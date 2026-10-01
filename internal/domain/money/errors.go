package money

import "errors"

var (
	ErrInvalidMoney     = errors.New("money: valor inválido")
	ErrInvalidCurrency  = errors.New("money: moeda inválida")
	ErrUninitialized    = errors.New("money: valor não inicializado")
	ErrCurrencyMismatch = errors.New("money: moedas incompatíveis")
	ErrOverflow         = errors.New("money: estouro numérico")
)
