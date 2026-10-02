package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// allowedAlgorithm é o único algoritmo aceito. A lista é propositalmente de
// um: fora RS256, nem a verificação chega a rodar.
const allowedAlgorithm = "RS256"

// clockLeeway é a tolerância entre relógios na validade do token.
const clockLeeway = 30 * time.Second

// Verifier valida tokens contra o provedor de identidade: assinatura via
// JWKS (com cache e atualização), emissor exato, audiência e validade. O
// serviço nunca emite token — só confere.
type Verifier struct {
	issuer   string
	audience string
	leeway   time.Duration
	verifier *oidc.IDTokenVerifier
}

// NewVerifier monta o validador. issuer é o emissor esperado, jwksURL é onde
// buscar as chaves (pode ser o endereço interno do provedor) e audience é a
// audiência exigida.
func NewVerifier(ctx context.Context, issuer, jwksURL, audience string) (*Verifier, error) {
	if issuer == "" || jwksURL == "" || audience == "" {
		return nil, fmt.Errorf("%w: emissor, chaves ou audiência vazios", ErrMalformed)
	}
	keys := oidc.NewRemoteKeySet(ctx, jwksURL)
	// A expiração sai da lib e é conferida abaixo com tolerância de relógio.
	verifier := oidc.NewVerifier(issuer, keys, &oidc.Config{ClientID: audience, SkipExpiryCheck: true})
	return &Verifier{issuer: issuer, audience: audience, leeway: clockLeeway, verifier: verifier}, nil
}

// WithLeeway troca a tolerância entre relógios (padrão 30s). Devolve o
// próprio validador para encadear.
func (v *Verifier) WithLeeway(d time.Duration) *Verifier {
	v.leeway = d
	return v
}

// Authenticate confere o token e devolve a identidade. Cada falha tem um
// erro próprio para o chamador distinguir (e o HTTP mapear).
func (v *Verifier) Authenticate(ctx context.Context, raw string) (*Principal, error) {
	if raw == "" {
		return nil, ErrMissingToken
	}
	if err := checkAlgorithm(raw); err != nil {
		return nil, err
	}
	token, err := v.verifier.Verify(ctx, raw)
	if err != nil {
		return nil, translateError(err)
	}
	if err := checkExpiry(token.Expiry, time.Now(), v.leeway); err != nil {
		return nil, err
	}
	var times struct {
		NotBefore *time.Time `json:"nbf"`
	}
	if err := token.Claims(&times); err == nil && times.NotBefore != nil &&
		time.Now().Add(v.leeway).Before(*times.NotBefore) {
		return nil, fmt.Errorf("%w: vale a partir de %v", ErrMalformed, times.NotBefore)
	}
	var claims struct {
		Subject    string   `json:"sub"`
		ProviderID string   `json:"provider_id"`
		Roles      []string `json:"-"`
		Realm      struct {
			Roles []string `json:"roles"`
		} `json:"realm_access"`
		Expiry time.Time `json:"-"`
	}
	if err := token.Claims(&claims); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	claims.Roles = claims.Realm.Roles
	claims.Expiry = token.Expiry
	return &Principal{
		Subject: claims.Subject, ProviderID: claims.ProviderID,
		Roles: claims.Roles, ExpiresAt: claims.Expiry,
	}, nil
}

// checkAlgorithm barra antes da criptografia tudo que não for RS256,
// incluindo o clássico alg=none.
func checkAlgorithm(raw string) error {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return fmt.Errorf("%w: esperado 3 partes", ErrMalformed)
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fmt.Errorf("%w: cabeçalho: %v", ErrMalformed, err)
	}
	var decoded struct {
		Algorithm string `json:"alg"`
	}
	if err := json.Unmarshal(header, &decoded); err != nil {
		return fmt.Errorf("%w: cabeçalho: %v", ErrMalformed, err)
	}
	if decoded.Algorithm != allowedAlgorithm {
		return fmt.Errorf("%w: %q", ErrBadAlgorithm, decoded.Algorithm)
	}
	return nil
}

// checkExpiry confere a validade com tolerância: só passou do prazo se
// agora menos a tolerância ainda está depois do vencimento.
func checkExpiry(expiry, now time.Time, leeway time.Duration) error {
	if now.Add(-leeway).After(expiry) {
		return fmt.Errorf("%w: até %v", ErrExpired, expiry)
	}
	return nil
}

func translateError(err error) error {
	text := err.Error()
	switch {
	case contains(text, "expired"):
		return fmt.Errorf("%w: %v", ErrExpired, err)
	case contains(text, "audience", "aud"):
		return fmt.Errorf("%w: %v", ErrWrongAudience, err)
	case contains(text, "issuer", "iss"):
		return fmt.Errorf("%w: %v", ErrWrongIssuer, err)
	case contains(text, "signature", "verify", "key", "decod", "token"):
		return fmt.Errorf("%w: %v", ErrBadSignature, err)
	default:
		return fmt.Errorf("%w: %v", ErrBadSignature, err)
	}
}

func contains(text string, parts ...string) bool {
	lower := strings.ToLower(text)
	for _, p := range parts {
		if strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}
