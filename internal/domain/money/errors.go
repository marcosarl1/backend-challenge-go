package money

import (
	"github.com/marcosarl1/backend-challenge-go/internal/domain"
)

var (
	ErrInvalidMoney     = domain.ErrInvalidMoney
	ErrInvalidCurrency  = domain.ErrInvalidCurrency
	ErrUninitialized    = domain.ErrUninitialized
	ErrCurrencyMismatch = domain.ErrCurrencyMismatch
	ErrOverflow         = domain.ErrOverflow
)
