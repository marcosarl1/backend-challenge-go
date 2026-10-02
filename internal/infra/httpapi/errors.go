package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/auth"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/postgres"
)

// problem é o corpo de erro (application/problem+json) com código estável.
type problem struct {
	Type          string `json:"type"`
	Title         string `json:"title"`
	Status        int    `json:"status"`
	Code          string `json:"code"`
	Detail        string `json:"detail,omitempty"`
	CorrelationID string `json:"correlationId"`
}

// Authenticator confere o token. O verificador OIDC implementa.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (*auth.Principal, error)
}

type ctxKey string

const identityKey ctxKey = "identity"

// identityOf recupera a identidade guardada pelo middleware.
func identityOf(r *http.Request) application.Identity {
	if ident, ok := r.Context().Value(identityKey).(application.Identity); ok {
		return ident
	}
	return application.Identity{}
}

func correlationOf(r *http.Request) string {
	if corr, ok := r.Context().Value(ctxKey("correlation")).(string); ok && corr != "" {
		return corr
	}
	return uuid.NewString()
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeError mapeia o erro para o status e o código estáveis.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, title := statusFor(err)
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "5")
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{
		Type: "/problems/" + codeToSlug(code), Title: title, Status: status,
		Code: code, Detail: err.Error(), CorrelationID: correlationOf(r),
	})
}

func statusFor(err error) (int, string, string) {
	switch {
	case errors.Is(err, application.ErrInvalidInput):
		return http.StatusBadRequest, "INVALID_REQUEST", "Invalid request"
	case errors.Is(err, auth.ErrMissingToken),
		errors.Is(err, auth.ErrMalformed),
		errors.Is(err, auth.ErrBadAlgorithm),
		errors.Is(err, auth.ErrBadSignature),
		errors.Is(err, auth.ErrExpired),
		errors.Is(err, auth.ErrWrongAudience),
		errors.Is(err, auth.ErrWrongIssuer):
		return http.StatusUnauthorized, "UNAUTHENTICATED", "Unauthenticated"
	case errors.Is(err, application.ErrForbidden):
		return http.StatusForbidden, "FORBIDDEN", "Forbidden"
	case errors.Is(err, application.ErrNotFound):
		return http.StatusNotFound, "NOT_FOUND", "Not found"
	case errors.Is(err, application.ErrIdempotencyMismatch):
		return http.StatusConflict, "IDEMPOTENCY_KEY_PAYLOAD_MISMATCH", "Idempotency conflict"
	case errors.Is(err, application.ErrExternalIDReused):
		return http.StatusConflict, "EXTERNAL_TRANSACTION_ID_CONFLICT", "External id conflict"
	case errors.Is(err, application.ErrWalletExists):
		return http.StatusConflict, "WALLET_ALREADY_EXISTS", "Wallet exists"
	case postgres.Classify(err) == postgres.ClassTransient:
		return http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Service unavailable"
	default:
		return http.StatusInternalServerError, "INTERNAL_ERROR", "Internal error"
	}
}

func codeToSlug(code string) string {
	var slug strings.Builder
	slug.Grow(len(code))
	for _, c := range code {
		switch {
		case c >= 'A' && c <= 'Z':
			slug.WriteByte(byte(c - 'A' + 'a'))
		case c >= 'a' && c <= 'z' || c >= '0' && c <= '9':
			slug.WriteRune(c)
		case c == '_':
			slug.WriteByte('-')
		}
	}
	return slug.String()
}
