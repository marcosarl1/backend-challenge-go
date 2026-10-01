package money

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func mustParse(t *testing.T, amount string, cur Currency) Money {
	t.Helper()
	m, err := Parse(amount, cur)
	if err != nil {
		t.Fatalf("Parse(%q) erro inesperado: %v", amount, err)
	}
	return m
}

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		amount  string
		cur     Currency
		want    int64
		wantErr error
	}{
		{name: "zero", amount: "0.00", cur: "BRL", want: 0},
		{name: "simples", amount: "25.00", cur: "BRL", want: 2500},
		{name: "centavos", amount: "0.01", cur: "BRL", want: 1},
		{name: "grande", amount: "1000000.99", cur: "USD", want: 100000099},
		{name: "int64 máximo", amount: "92233720368547758.07", cur: "BRL", want: math.MaxInt64},

		{name: "vazio", amount: "", cur: "BRL", wantErr: ErrInvalidMoney},
		{name: "branco", amount: " ", cur: "BRL", wantErr: ErrInvalidMoney},
		{name: "espaços ao redor", amount: " 25.00 ", cur: "BRL", wantErr: ErrInvalidMoney},
		{name: "nan", amount: "NaN", cur: "BRL", wantErr: ErrInvalidMoney},
		{name: "infinito", amount: "Infinity", cur: "BRL", wantErr: ErrInvalidMoney},
		{name: "científica", amount: "1e3", cur: "BRL", wantErr: ErrInvalidMoney},
		{name: "científica decimal", amount: "2.5E3", cur: "BRL", wantErr: ErrInvalidMoney},
		{name: "sem decimais", amount: "25", cur: "BRL", wantErr: ErrInvalidMoney},
		{name: "uma decimal", amount: "25.0", cur: "BRL", wantErr: ErrInvalidMoney},
		{name: "três decimais", amount: "25.000", cur: "BRL", wantErr: ErrInvalidMoney},
		{name: "negativo", amount: "-1.00", cur: "BRL", wantErr: ErrInvalidMoney},
		{name: "zero negativo", amount: "-0.00", cur: "BRL", wantErr: ErrInvalidMoney},
		{name: "sinal de mais", amount: "+1.00", cur: "BRL", wantErr: ErrInvalidMoney},
		{name: "zeros à esquerda", amount: "025.00", cur: "BRL", wantErr: ErrInvalidMoney},
		{name: "zero duplo", amount: "00.00", cur: "BRL", wantErr: ErrInvalidMoney},
		{name: "vírgula", amount: "25,00", cur: "BRL", wantErr: ErrInvalidMoney},
		{name: "letras", amount: "abc", cur: "BRL", wantErr: ErrInvalidMoney},
		{name: "hexadecimal", amount: "0x10.00", cur: "BRL", wantErr: ErrInvalidMoney},

		{name: "um centavo acima do máximo", amount: "92233720368547758.08", cur: "BRL", wantErr: ErrOverflow},
		{name: "parte inteira enorme", amount: "99999999999999999999.00", cur: "BRL", wantErr: ErrOverflow},
		{name: "dígitos enormes", amount: "123456789012345678901234567890.00", cur: "BRL", wantErr: ErrOverflow},

		{name: "moeda vazia", amount: "1.00", cur: "", wantErr: ErrInvalidCurrency},
		{name: "moeda minúscula", amount: "1.00", cur: "brl", wantErr: ErrInvalidCurrency},
		{name: "moeda desconhecida", amount: "1.00", cur: "XXX", wantErr: ErrInvalidCurrency},
		{name: "moeda sem centavos", amount: "1.00", cur: "JPY", wantErr: ErrInvalidCurrency},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.amount, tt.cur)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Parse(%q) erro = %v, esperado %v", tt.amount, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) erro inesperado: %v", tt.amount, err)
			}
			if got.Minor() != tt.want || got.Currency() != tt.cur {
				t.Fatalf("Parse(%q) = (%d, %q), esperado (%d, %q)",
					tt.amount, got.Minor(), got.Currency(), tt.want, tt.cur)
			}
			// Volta canônica: formatar um valor lido devolve a entrada.
			if got.String() != tt.amount {
				t.Fatalf("String() = %q, esperado %q", got.String(), tt.amount)
			}
		})
	}
}

func TestParseSigned(t *testing.T) {
	m, err := ParseSigned("-25.00", "BRL")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if m.Minor() != -2500 {
		t.Fatalf("Minor() = %d, esperado -2500", m.Minor())
	}
	if m.String() != "-25.00" {
		t.Fatalf("String() = %q, esperado -25.00", m.String())
	}

	for _, amount := range []string{"-0.00", "--1.00", "-NaN", "-25.0", ""} {
		if _, err := ParseSigned(amount, "BRL"); err == nil {
			t.Fatalf("ParseSigned(%q) esperado erro, veio nil", amount)
		}
	}
}

func TestZeroAndFromMinor(t *testing.T) {
	z, err := Zero("BRL")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if z.Minor() != 0 || z.String() != "0.00" {
		t.Fatalf("Zero = %v", z)
	}
	if _, err := Zero("JPY"); !errors.Is(err, ErrInvalidCurrency) {
		t.Fatalf("Zero(JPY) erro = %v", err)
	}

	neg, err := FromMinor(-5, "EUR")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if neg.String() != "-0.05" {
		t.Fatalf("String() = %q", neg.String())
	}
	max, err := FromMinor(math.MaxInt64, "BRL")
	if err != nil || max.String() != "92233720368547758.07" {
		t.Fatalf("FromMinor(max) = %v, %v", max, err)
	}
	min, err := FromMinor(math.MinInt64, "BRL")
	if err != nil || min.String() != "-92233720368547758.08" {
		t.Fatalf("FromMinor(min) = %v, %v", min, err)
	}
}

func TestAddSub(t *testing.T) {
	a := mustParse(t, "25.00", "BRL")
	b := mustParse(t, "10.50", "BRL")

	sum, err := a.Add(b)
	if err != nil || sum.String() != "35.50" {
		t.Fatalf("Add = %v, %v", sum, err)
	}
	diff, err := a.Sub(b)
	if err != nil || diff.String() != "14.50" {
		t.Fatalf("Sub = %v, %v", diff, err)
	}

	// Moeda diferente em toda operação.
	usd := mustParse(t, "1.00", "USD")
	for _, op := range []struct {
		name string
		run  func() error
	}{
		{"add", func() error { _, err := a.Add(usd); return err }},
		{"sub", func() error { _, err := a.Sub(usd); return err }},
		{"cmp", func() error { _, err := a.Cmp(usd); return err }},
	} {
		if err := op.run(); !errors.Is(err, ErrCurrencyMismatch) {
			t.Fatalf("%s com moeda diferente erro = %v", op.name, err)
		}
	}

	// Estouro de sinal nos limites.
	top, _ := FromMinor(math.MaxInt64, "BRL")
	one, _ := FromMinor(1, "BRL")
	if _, err := top.Add(one); !errors.Is(err, ErrOverflow) {
		t.Fatalf("MaxInt64+1 erro = %v, esperado estouro", err)
	}
	bottom, _ := FromMinor(math.MinInt64, "BRL")
	if _, err := bottom.Sub(one); !errors.Is(err, ErrOverflow) {
		t.Fatalf("MinInt64-1 erro = %v, esperado estouro", err)
	}
	if _, err := bottom.Add(Money{}); !errors.Is(err, ErrUninitialized) {
		t.Fatalf("soma com valor vazio erro = %v", err)
	}
	neg, _ := FromMinor(-1, "BRL")
	if _, err := bottom.Add(neg); !errors.Is(err, ErrOverflow) {
		t.Fatalf("MinInt64+(-1) erro = %v, esperado estouro", err)
	}
	// Subtrair o MinInt64 do zero estoura (0-MinInt64 == MaxInt64+1).
	if _, err := top.Sub(bottom); !errors.Is(err, ErrOverflow) {
		t.Fatalf("MaxInt64-MinInt64 erro = %v, esperado estouro", err)
	}
	// Resultado exatamente no limite funciona.
	if got, err := top.Sub(top); err != nil || got.Minor() != 0 {
		t.Fatalf("MaxInt64-MaxInt64 = %v, %v", got, err)
	}
}

func TestNeg(t *testing.T) {
	a := mustParse(t, "25.00", "BRL")
	n, err := a.Neg()
	if err != nil || n.String() != "-25.00" {
		t.Fatalf("Neg = %v, %v", n, err)
	}
	back, err := n.Neg()
	if err != nil || back.String() != "25.00" {
		t.Fatalf("Neg(Neg) = %v, %v", back, err)
	}
	bottom, _ := FromMinor(math.MinInt64, "BRL")
	if _, err := bottom.Neg(); !errors.Is(err, ErrOverflow) {
		t.Fatalf("Neg(MinInt64) erro = %v, esperado estouro", err)
	}
	if _, err := (Money{}).Neg(); !errors.Is(err, ErrUninitialized) {
		t.Fatalf("Neg(valor vazio) erro = %v", err)
	}
}

func TestCmp(t *testing.T) {
	a := mustParse(t, "25.00", "BRL")
	b := mustParse(t, "25.00", "BRL")
	c := mustParse(t, "30.00", "BRL")
	for _, tt := range []struct {
		x, y Money
		want int
	}{
		{a, b, 0}, {a, c, -1}, {c, a, 1},
	} {
		got, err := tt.x.Cmp(tt.y)
		if err != nil || got != tt.want {
			t.Fatalf("Cmp = %d, %v", got, err)
		}
	}
}

func TestUninitialized(t *testing.T) {
	var zero Money
	if zero.Valid() {
		t.Fatal("valor zero precisa ser inválido")
	}
	if zero.String() != "INVALID" {
		t.Fatalf("String() = %q", zero.String())
	}
	a := mustParse(t, "1.00", "BRL")
	for name, err := range map[string]error{
		"add": func() error { _, err := zero.Add(a); return err }(),
		"sub": func() error { _, err := zero.Sub(a); return err }(),
		"cmp": func() error { _, err := zero.Cmp(a); return err }(),
		"neg": func() error { _, err := zero.Neg(); return err }(),
	} {
		if !errors.Is(err, ErrUninitialized) {
			t.Fatalf("%s com valor vazio erro = %v", name, err)
		}
	}
	if _, err := zero.MarshalJSON(); !errors.Is(err, ErrUninitialized) {
		t.Fatalf("MarshalJSON com valor vazio erro = %v", err)
	}
}

func TestJSON(t *testing.T) {
	a := mustParse(t, "25.00", "BRL")
	data, err := a.MarshalJSON()
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if string(data) != `{"amount":"25.00","currency":"BRL"}` {
		t.Fatalf("MarshalJSON = %s", data)
	}

	var back Money
	if err := back.UnmarshalJSON(data); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if cmp, _ := back.Cmp(a); cmp != 0 {
		t.Fatalf("volta diferente: %v", back)
	}

	// Valor numérico precisa falhar: número não decodifica em string.
	var num Money
	if err := num.UnmarshalJSON([]byte(`{"amount":25.00,"currency":"BRL"}`)); err == nil {
		t.Fatal("valor numérico aceito, esperado rejeição")
	}
	for _, raw := range []string{
		`{"amount":"25.0","currency":"BRL"}`,
		`{"amount":"25.00","currency":"JPY"}`,
		`{"amount":"25.00"}`,
		`{"currency":"BRL"}`,
		`{"amount":"25.00","currency":"BRL","extra":1}`,
		`not json`,
	} {
		var m Money
		if err := m.UnmarshalJSON([]byte(raw)); err == nil {
			t.Fatalf("UnmarshalJSON(%s) aceito, esperado rejeição", raw)
		}
	}
}

// FuzzParse joga textos arbitrários no Parse. Entrada aceita precisa ser canônica: formatar devolve exatamente a entrada e reler concorda.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"0.00", "25.00", "0.01", "92233720368547758.07",
		"", "NaN", "1e3", "25", "25.0", "25.000", "-1.00", "+1.00",
		"025.00", "25,00", " 25.00", "99999999999999999999.00",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		m, err := Parse(s, "BRL")
		if err != nil {
			return
		}
		formatted := m.String()
		if formatted != s {
			t.Fatalf("aceito sem ser canônico: Parse(%q).String() = %q", s, formatted)
		}
		again, err := Parse(formatted, "BRL")
		if err != nil {
			t.Fatalf("releitura falhou: %v", err)
		}
		cmp, err := m.Cmp(again)
		if err != nil || cmp != 0 {
			t.Fatalf("releitura divergiu para %q", s)
		}
		if strings.ContainsAny(s, "eEnNaf") {
			t.Fatalf("entrada com cara de float aceita: %q", s)
		}
	})
}
