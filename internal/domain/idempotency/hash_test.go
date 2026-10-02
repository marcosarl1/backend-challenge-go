package idempotency

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func baseOperation() Operation {
	return Operation{
		ProviderID: "provider-a", ExternalID: "transaction-123",
		PlayerID: "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
		WalletID: "0192f291-27dd-7d3f-8071-5f8685deef37",
		RoundID:  "round-987", GameID: "fortune-chimp", Kind: "BET",
		Amount: "25.00", Currency: "BRL",
	}
}

func TestHashCanonicalShape(t *testing.T) {
	// O algoritmo documentado: este JSON exato (chaves ordenadas, sem espaços, sem escape HTML) passa no SHA-256.
	want := sha256.Sum256([]byte(`{"externalTransactionId":"transaction-123","gameId":"fortune-chimp","kind":"BET","money":{"amount":"25.00","currency":"BRL"},"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","providerId":"provider-a","roundId":"round-987","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37"}`))
	got, err := Hash(baseOperation())
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if got != want {
		t.Fatalf("hash fugiu do canônico:\n%x\n%x", got, want)
	}
	// Determinístico: a ordem de montagem não importa.
	again, err := Hash(baseOperation())
	if err != nil || again != got {
		t.Fatalf("hash instável: %v", err)
	}
}

func TestHashFieldDifference(t *testing.T) {
	base, err := Hash(baseOperation())
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	mutations := map[string]func(*Operation){
		"provedor":   func(o *Operation) { o.ProviderID = "provider-b" },
		"externo":    func(o *Operation) { o.ExternalID = "transaction-124" },
		"jogador":    func(o *Operation) { o.PlayerID = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a2" },
		"carteira":   func(o *Operation) { o.WalletID = "0192f291-27dd-7d3f-8071-5f8685deef38" },
		"rodada":     func(o *Operation) { o.RoundID = "round-988" },
		"jogo":       func(o *Operation) { o.GameID = "other-game" },
		"tipo":       func(o *Operation) { o.Kind = "WIN" },
		"valor":      func(o *Operation) { o.Amount = "25.01" },
		"moeda":      func(o *Operation) { o.Amount, o.Currency = "25.00", "USD" },
		"referência": func(o *Operation) { o.ReferenceExt = "bet-9" },
	}
	for name, mutate := range mutations {
		op := baseOperation()
		mutate(&op)
		got, err := Hash(op)
		if err != nil {
			t.Fatalf("%s erro inesperado: %v", name, err)
		}
		if got == base {
			t.Fatalf("%s não mudou o hash", name)
		}
	}
}

func TestHashReferenceAbsentVsNull(t *testing.T) {
	// Pela fila, a referência pode vir explícita como null; pelo HTTP, o campo pode nem existir. Os dois casos convergem para "sem referência".
	var nullRef *string
	fromNull := baseOperation()
	if nullRef != nil {
		fromNull.ReferenceExt = *nullRef
	}
	absent, err := Hash(baseOperation())
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	fromNullHash, err := Hash(fromNull)
	if err != nil || fromNullHash != absent {
		t.Fatalf("null divergiu de ausente: %v", err)
	}
	withRef := baseOperation()
	withRef.ReferenceExt = "bet-9"
	present, err := Hash(withRef)
	if err != nil || present == absent {
		t.Fatalf("referência presente não mudou o hash: %v", err)
	}
}

func TestHashNoHTMLEscaping(t *testing.T) {
	// Caracteres que o encoding/json escaparia por padrão (&, <, >) entram crus no cálculo.
	op := baseOperation()
	op.GameID = "a&b<c>d"
	got, err := Hash(op)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	raw := `{"externalTransactionId":"transaction-123","gameId":"a&b<c>d","kind":"BET","money":{"amount":"25.00","currency":"BRL"},"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","providerId":"provider-a","roundId":"round-987","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37"}`
	if want := sha256.Sum256([]byte(raw)); got != want {
		t.Fatalf("escape HTML vazou para o hash")
	}
}

func TestHashRejectsBadInput(t *testing.T) {
	op := baseOperation()
	op.Amount = "25.0" // fora da forma canônica
	if _, err := Hash(op); err == nil {
		t.Fatal("valor não canônico aceito")
	}
	op = baseOperation()
	op.ProviderID = ""
	if _, err := Hash(op); err == nil {
		t.Fatal("campo vazio aceito")
	}
	op = baseOperation()
	op.Currency = "JPY"
	if _, err := Hash(op); err == nil {
		t.Fatal("moeda inválida aceita")
	}
}

func TestHashHex(t *testing.T) {
	op := baseOperation()
	sum, err := Hash(op)
	if err != nil {
		t.Fatalf("Hash erro inesperado: %v", err)
	}
	got, err := HashHex(op)
	if err != nil || got != hex.EncodeToString(sum[:]) {
		t.Fatalf("HashHex = %q, erro = %v", got, err)
	}
	op.Amount = "25.0"
	if got, err := HashHex(op); err == nil || got != "" {
		t.Fatalf("HashHex inválido = %q, erro = %v", got, err)
	}
}
