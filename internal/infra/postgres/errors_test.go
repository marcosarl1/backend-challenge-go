package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func pgErr(code, constraint string) *pgconn.PgError {
	return &pgconn.PgError{Code: code, ConstraintName: constraint, Message: "sintético"}
}

func TestClassify(t *testing.T) {
	for _, tt := range []struct {
		name  string
		err   error
		class Class
	}{
		{"serialização repete", pgErr("40001", ""), ClassTransient},
		{"deadlock repete", pgErr("40P01", ""), ClassTransient},
		{"conexão repete", pgErr("08006", ""), ClassTransient},
		{"banco caiu repete", pgErr("57P01", ""), ClassTransient},
		{"pool cheio repete", pgErr("53300", ""), ClassTransient},
		{"conexão indisponível repete", pgErr("08001", ""), ClassTransient},
		{"disco cheio repete", pgErr("53100", ""), ClassTransient},
		{"única é restrição", pgErr("23505", "wt_provider_idemkey_uk"), ClassConstraint},
		{"estrangeira é restrição", pgErr("23503", "wallet_fkey"), ClassConstraint},
		{"não-nulo é restrição", pgErr("23502", "wallet_id_not_null"), ClassConstraint},
		{"check é restrição", pgErr("23514", "ledger_arith"), ClassConstraint},
		{"exclusão é restrição", pgErr("23P01", "period_exclusion"), ClassConstraint},
		{"prazo estourado repete", fmt.Errorf("consulta: %w", context.DeadlineExceeded), ClassTransient},
		{"cancelamento não repete", context.Canceled, ClassPermanent},
		{"permissão é permanente", pgErr("42501", ""), ClassPermanent},
		{"sql ruim é permanente", pgErr("42601", ""), ClassPermanent},
		{"nulo é permanente", nil, ClassPermanent},
	} {
		if got := Classify(tt.err); got != tt.class {
			t.Fatalf("%s: %q, esperado %q", tt.name, got, tt.class)
		}
	}
	// Através de embrulho, a classificação sobrevive.
	wrapped := &testWrap{err: pgErr("40001", "")}
	if got := Classify(wrapped); got != ClassTransient {
		t.Fatalf("embrulho: %q", got)
	}
}

type testWrap struct{ err error }

func (w *testWrap) Error() string { return w.err.Error() }
func (w *testWrap) Unwrap() error { return w.err }

func TestAsConstraint(t *testing.T) {
	ce, ok := AsConstraint(pgErr("23505", "wt_provider_idemkey_uk"))
	if !ok || ce.Constraint != "wt_provider_idemkey_uk" || ce.Code != "23505" {
		t.Fatalf("extração = %+v, %v", ce, ok)
	}
	if _, ok := AsConstraint(pgErr("40001", "")); ok {
		t.Fatal("serialização não é restrição")
	}
	if _, ok := AsConstraint(errors.New("rede")); ok {
		t.Fatal("erro comum não é restrição")
	}
	wrapped := fmt.Errorf("inserindo: %w", pgErr("23503", "wallet_fkey"))
	foreign, ok := AsConstraint(wrapped)
	if !ok || foreign.Code != "23503" || foreign.Constraint != "wallet_fkey" || !errors.Is(foreign, wrapped) {
		t.Fatalf("restrição embrulhada = %+v, ok = %v", foreign, ok)
	}
	if foreign.Error() == "" {
		t.Fatal("restrição sem descrição")
	}
	var target *ConstraintError
	if !errors.As(ce, &target) || target.Constraint != "wt_provider_idemkey_uk" {
		t.Fatalf("errors.As falhou: %+v", ce)
	}
}
