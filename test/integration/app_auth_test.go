package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/marcosarl1/backend-challenge-go/internal/infra/auth"
)

func keycloakURL(t *testing.T) string {
	t.Helper()
	if url := os.Getenv("TEST_KEYCLOAK_URL"); url != "" {
		return url
	}
	t.Skip("TEST_KEYCLOAK_URL ausente; pulei o teste contra IdP de verdade")
	return ""
}

func clientToken(t *testing.T, base, clientID, secret string) string {
	t.Helper()
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", clientID)
	form.Set("client_secret", secret)
	resp, err := http.PostForm(base+"/realms/wagering/protocol/openid-connect/token", form)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token %s: %s", resp.Status, body)
	}
	var decoded struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("token: %v", err)
	}
	return decoded.AccessToken
}

func testVerifier(t *testing.T, audience string) *auth.Verifier {
	t.Helper()
	base := keycloakURL(t)
	issuer := base + "/realms/wagering"
	jwks := issuer + "/protocol/openid-connect/certs"
	v, err := auth.NewVerifier(context.Background(), issuer, jwks, audience)
	if err != nil {
		t.Fatalf("verificador: %v", err)
	}
	return v
}

func TestAuthValid(t *testing.T) {
	base := keycloakURL(t)
	v := testVerifier(t, "wagering-api")
	raw := clientToken(t, base, "provider-a", "provider-a-secret")

	p, err := v.Authenticate(context.Background(), raw)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if p.ProviderID != "provider-a" || !p.HasRole("provider") || p.Subject == "" {
		t.Fatalf("identidade = %+v", p)
	}

	internal := clientToken(t, base, "wallet-internal", "wallet-internal-secret")
	pi, err := v.Authenticate(context.Background(), internal)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if pi.ProviderID != "" || !pi.HasRole("internal") {
		t.Fatalf("interno = %+v", pi)
	}
}

func TestAuthFailures(t *testing.T) {
	base := keycloakURL(t)
	v := testVerifier(t, "wagering-api")
	ctx := context.Background()
	valid := clientToken(t, base, "provider-a", "provider-a-secret")

	if _, err := v.Authenticate(ctx, ""); !errors.Is(err, auth.ErrMissingToken) {
		t.Fatalf("ausente = %v", err)
	}
	if _, err := v.Authenticate(ctx, "não-é-token"); !errors.Is(err, auth.ErrMalformed) {
		t.Fatalf("malformado = %v", err)
	}
	// Assinatura violada: troca um caractere do payload.
	parts := strings.Split(valid, ".")
	tampered := parts[0] + "." + "A" + parts[1][1:] + "." + parts[2]
	if _, err := v.Authenticate(ctx, tampered); !errors.Is(err, auth.ErrBadSignature) {
		t.Fatalf("adulterado = %v", err)
	}
	// alg=none nunca entra, mesmo com o resto válido.
	unsigned := "eyJhbGciOiJub25lIn0." + parts[1] + "."
	if _, err := v.Authenticate(ctx, unsigned); !errors.Is(err, auth.ErrBadAlgorithm) {
		t.Fatalf("none = %v", err)
	}
	// Audiência e emissor trocados caem cada um no seu erro.
	otherAud, _ := auth.NewVerifier(ctx, base+"/realms/wagering",
		base+"/realms/wagering/protocol/openid-connect/certs", "outra-api")
	if _, err := otherAud.Authenticate(ctx, valid); !errors.Is(err, auth.ErrWrongAudience) {
		t.Fatalf("audiência = %v", err)
	}
	otherIss, _ := auth.NewVerifier(ctx, base+"/realms/outro",
		base+"/realms/wagering/protocol/openid-connect/certs", "wagering-api")
	if _, err := otherIss.Authenticate(ctx, valid); !errors.Is(err, auth.ErrWrongIssuer) {
		t.Fatalf("emissor = %v", err)
	}
}

func TestAuthExpired(t *testing.T) {
	base := keycloakURL(t)
	ctx := context.Background()
	setLifespan(t, base, 2)
	defer setLifespan(t, base, 300)

	raw := clientToken(t, base, "provider-a", "provider-a-secret")
	time.Sleep(3 * time.Second)
	v := testVerifier(t, "wagering-api").WithLeeway(time.Second)
	if _, err := v.Authenticate(ctx, raw); !errors.Is(err, auth.ErrExpired) {
		t.Fatalf("vencido = %v", err)
	}
}

// setLifespan troca a validade do token de acesso do realm (GET, muda o
// campo, PUT de volta).
func setLifespan(t *testing.T, base string, seconds int) {
	t.Helper()
	form := url.Values{}
	form.Set("grant_type", "password")
	form.Set("client_id", "admin-cli")
	form.Set("username", "admin")
	form.Set("password", "admin")
	resp, err := http.PostForm(base+"/realms/master/protocol/openid-connect/token", form)
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var admin struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &admin); err != nil {
		t.Fatalf("admin: %v", err)
	}

	call := func(method, path string, payload any) []byte {
		var reader io.Reader
		if payload != nil {
			data, _ := json.Marshal(payload)
			reader = bytes.NewReader(data)
		}
		req, _ := http.NewRequest(method, base+path, reader)
		req.Header.Set("Authorization", "Bearer "+admin.AccessToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("realm: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 300 {
			t.Fatalf("realm %s: %s", resp.Status, body)
		}
		return body
	}
	var realm map[string]any
	if err := json.Unmarshal(call("GET", "/admin/realms/wagering", nil), &realm); err != nil {
		t.Fatalf("realm: %v", err)
	}
	realm["accessTokenLifespan"] = seconds
	call("PUT", "/admin/realms/wagering", realm)
}
