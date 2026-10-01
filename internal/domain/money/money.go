package money

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Scale é o número fixo de casas decimais. Uma unidade da moeda principal sempre vale 100 unidades mínimas.
const Scale = 2

// minorPerMajor converte unidades principais em unidades mínimas.
const minorPerMajor = 100

// Limites em unidades mínimas (centavos)
const (
	MaxMinor = math.MaxInt64
	MinMinor = math.MinInt64
)

/*
amountPattern aceita só a forma canônica externa: sem sinal,
sem zeros à esquerda (exceto o próprio "0"), exatamente duas casas decimais.
Qualquer outra forma ("25", "25.0", "025.00", "+25.00", "25.000", "1e2",
"NaN", "-0.00", espaços) é rejeitada em vez de normalizada.
*/
var amountPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.[0-9]{2}$`)

/*
Money é um value object imutável com o valor em unidades mínimas e a moeda.
O valor zero do struct é inválido: todo Money em uso precisa vir de Parse,
ParseSigned, Zero ou FromMinor, que validam as entradas.
*/

type Money struct {
	minor int64
	cur   Currency
}

/*
Valid informa se m foi construído por um construtor (moeda conhecida).
Qualquer valor int64 é representável, incluindo negativos, que só surgem
da aritmética interna (Parse nunca os produz).
*/

func (m Money) Valid() bool {
	return m.cur.Valid()
}

// Currency devolve a moeda. Chamar só em Money válido.
func (m Money) Currency() Currency {
	return m.cur
}

// Minor devolve o valor em unidades mínimas. Chamar só em Money válido; os construtores são o único lugar onde a validade é garantida.
func (m Money) Minor() int64 {
	return m.minor
}

// Zero devolve o valor zero na moeda dada.
func Zero(cur Currency) (Money, error) {
	if err := checkCurrency(cur); err != nil {
		return Money{}, err
	}
	return Money{minor: 0, cur: cur}, nil
}

/*
FromMinor constrói Money a partir de um valor já expresso em unidades
mínimas. Negativos são aceitos aqui: eles só surgem de diferenças internas,
nunca da entrada externa.
*/
func FromMinor(minor int64, cur Currency) (Money, error) {
	if err := checkCurrency(cur); err != nil {
		return Money{}, err
	}
	return Money{minor: minor, cur: cur}, nil
}

/*
Parse constrói Money a partir da forma decimal canônica externa ("25.00").
Rejeita texto vazio, NaN/Infinity, notação científica, escala ausente ou
excedente, sinais, espaços e qualquer coisa que estoure o int64.
*/
func Parse(amount string, cur Currency) (Money, error) {
	if err := checkCurrency(cur); err != nil {
		return Money{}, err
	}
	minor, err := parseCanonical(amount)
	if err != nil {
		return Money{}, err
	}
	return Money{minor: minor, cur: cur}, nil
}

/*
ParseSigned é o Parse com um "-" inicial opcional, para contas internas
(por exemplo, saldo guardado menos saldo reconstruído). "-0.00" é
rejeitado: não existe zero negativo na forma canônica.
*/
func ParseSigned(amount string, cur Currency) (Money, error) {
	if err := checkCurrency(cur); err != nil {
		return Money{}, err
	}
	neg := strings.HasPrefix(amount, "-")
	body := strings.TrimPrefix(amount, "-")
	minor, err := parseCanonical(body)
	if err != nil {
		return Money{}, err
	}
	if !neg {
		return Money{minor: minor, cur: cur}, nil
	}
	if minor == 0 {
		return Money{}, fmt.Errorf("%w: %q", ErrInvalidMoney, amount)
	}
	return Money{minor: -minor, cur: cur}, nil
}

func parseCanonical(amount string) (int64, error) {
	if !amountPattern.MatchString(amount) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidMoney, amount)
	}
	dot := strings.IndexByte(amount, '.')
	dollars, err := strconv.ParseInt(amount[:dot], 10, 64)
	if err != nil {
		// O padrão já limita o formato; falhar aqui significa que a parte inteira não cabe no int64.
		return 0, fmt.Errorf("%w: %q", ErrOverflow, amount)
	}
	cents := int64(amount[dot+1]-'0')*10 + int64(amount[dot+2]-'0')
	if dollars > (math.MaxInt64-cents)/minorPerMajor {
		return 0, fmt.Errorf("%w: %q", ErrOverflow, amount)
	}
	return dollars*minorPerMajor + cents, nil
}

func (m Money) checkOperable(o Money) error {
	if !m.Valid() || !o.Valid() {
		return ErrUninitialized
	}
	if m.cur != o.cur {
		return fmt.Errorf("%w: %q vs %q", ErrCurrencyMismatch, m.cur, o.cur)
	}
	return nil
}

// Add devolve m+o. Exige a mesma moeda; estouro de sinal é reportado.
func (m Money) Add(o Money) (Money, error) {
	if err := m.checkOperable(o); err != nil {
		return Money{}, err
	}
	sum := m.minor + o.minor
	if (m.minor > 0 && o.minor > 0 && sum < 0) ||
		(m.minor < 0 && o.minor < 0 && sum >= 0) {
		return Money{}, fmt.Errorf("%w na adição", ErrOverflow)
	}
	return Money{minor: sum, cur: m.cur}, nil
}

// Sub devolve m-o. Exige a mesma moeda; estouro de sinal é reportado.
func (m Money) Sub(o Money) (Money, error) {
	if err := m.checkOperable(o); err != nil {
		return Money{}, err
	}
	diff := m.minor - o.minor
	if (m.minor >= 0 && o.minor < 0 && diff < 0) ||
		(m.minor < 0 && o.minor > 0 && diff >= 0) {
		return Money{}, fmt.Errorf("%w na subtração", ErrOverflow)
	}
	return Money{minor: diff, cur: m.cur}, nil
}

// Neg devolve -m. Negar o MinInt64 estouraria e é reportado.
func (m Money) Neg() (Money, error) {
	if !m.Valid() {
		return Money{}, ErrUninitialized
	}
	if m.minor == math.MinInt64 {
		return Money{}, fmt.Errorf("%w na negação", ErrOverflow)
	}
	return Money{minor: -m.minor, cur: m.cur}, nil
}

// Cmp compara m e o: -1, 0 ou +1. Exige a mesma moeda.
func (m Money) Cmp(o Money) (int, error) {
	if err := m.checkOperable(o); err != nil {
		return 0, err
	}
	switch {
	case m.minor < o.minor:
		return -1, nil
	case m.minor > o.minor:
		return 1, nil
	default:
		return 0, nil
	}
}

// String mostra a forma decimal canônica ("25.00", "-25.00").
// Money não inicializado aparece como "INVALID".
func (m Money) String() string {
	if !m.Valid() {
		return "INVALID"
	}
	neg := m.minor < 0
	q := m.minor / minorPerMajor
	r := m.minor % minorPerMajor
	if neg {
		q, r = -q, -r
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	b.WriteString(strconv.FormatInt(q, 10))
	b.WriteByte('.')
	if r < 10 {
		b.WriteByte('0')
	}
	b.WriteString(strconv.FormatInt(r, 10))
	return b.String()
}

// moneyJSON é o contrato externo: decimal como texto, nunca como número.
type moneyJSON struct {
	Amount   string   `json:"amount"`
	Currency Currency `json:"currency"`
}

// MarshalJSON emite {"amount":"25.00","currency":"BRL"}.
func (m Money) MarshalJSON() ([]byte, error) {
	if !m.Valid() {
		return nil, ErrUninitialized
	}
	return json.Marshal(moneyJSON{Amount: m.String(), Currency: m.cur})
}

// UnmarshalJSON aceita só {"amount":"25.00","currency":"BRL"} com o valor
// como texto JSON; um valor numérico não decodifica em string e por isso é
// rejeitado.
func (m *Money) UnmarshalJSON(data []byte) error {
	var dto moneyJSON
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&dto); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidMoney, err)
	}
	if dto.Amount == "" {
		return fmt.Errorf("%w: amount ausente", ErrInvalidMoney)
	}
	parsed, err := Parse(dto.Amount, dto.Currency)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}
