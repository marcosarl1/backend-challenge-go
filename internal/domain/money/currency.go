package money

import "fmt"

type Currency string

/*
twoMinorUnitCurrencies é o subconjunto operacional de códigos ISO 4217 com expoente 2. Os cenários principais rodam em BRL; o tipo continua carregando a moeda para que misturar duas moedas diferentes seja sempre rejeitado.
*/

var twoMinorUnitCurrencies = map[Currency]struct{}{
	"USD": {}, "EUR": {}, "BRL": {}, "GBP": {}, "CAD": {}, "AUD": {},
	"CHF": {}, "CNY": {}, "SEK": {}, "NOK": {}, "DKK": {}, "PLN": {},
	"CZK": {}, "MXN": {}, "ARS": {}, "CLP": {}, "COP": {}, "PEN": {},
	"ILS": {}, "ZAR": {}, "INR": {}, "NZD": {}, "SGD": {}, "HKD": {},
}

// Valid informa se c pertence ao subconjunto suportado do ISO 4217.
func (c Currency) Valid() bool {
	_, ok := twoMinorUnitCurrencies[c]
	return ok
}

func checkCurrency(c Currency) error {
	if !c.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidCurrency, string(c))
	}
	return nil
}
