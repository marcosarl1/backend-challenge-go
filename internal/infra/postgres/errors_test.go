package postgres

import (
	"errors"
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
		{"única é restrição", pgErr("23505", "wt_provider_idemkey_uk"), ClassConstraint},
		{"check é restrição", pgErr("23514", "ledger_arith"), ClassConstraint},
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
	var target *ConstraintError
	if !errors.As(ce, &target) || target.Constraint != "wt_provider_idemkey_uk" {
		t.Fatalf("errors.As falhou: %+v", ce)
	}
}
