package httpapi

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/auth"
)

func TestStatusMapping(t *testing.T) {
	transient := &pgconn.PgError{Code: "40001", Message: "x"}
	for _, tt := range []struct {
		err    error
		status int
		code   string
	}{
		{application.ErrInvalidInput, 400, "INVALID_REQUEST"},
		{auth.ErrMissingToken, 401, "UNAUTHENTICATED"},
		{auth.ErrExpired, 401, "UNAUTHENTICATED"},
		{auth.ErrBadSignature, 401, "UNAUTHENTICATED"},
		{application.ErrForbidden, 403, "FORBIDDEN"},
		{application.ErrNotFound, 404, "NOT_FOUND"},
		{application.ErrIdempotencyMismatch, 409, "IDEMPOTENCY_KEY_PAYLOAD_MISMATCH"},
		{application.ErrExternalIDReused, 409, "EXTERNAL_TRANSACTION_ID_CONFLICT"},
		{application.ErrWalletExists, 409, "WALLET_ALREADY_EXISTS"},
		{transient, 503, "SERVICE_UNAVAILABLE"},
		{errors.New("boom"), 500, "INTERNAL_ERROR"},
	} {
		status, code, _ := statusFor(tt.err)
		if status != tt.status || code != tt.code {
			t.Fatalf("%v = %d/%s", tt.err, status, code)
		}
	}
	if got := codeToSlug("SERVICE_UNAVAILABLE"); got != "service-unavailable" {
		t.Fatalf("slug = %q", got)
	}
}
