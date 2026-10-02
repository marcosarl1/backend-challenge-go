package auth

import (
	"errors"
	"slices"
	"time"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
)

var (
	// ErrMissingToken indica ausência de credencial.
	ErrMissingToken = errors.New("autenticação: token ausente")
	// ErrMalformed indica token fora do formato JWT.
	ErrMalformed = errors.New("autenticação: token malformado")
	// ErrBadAlgorithm indica algoritmo fora da lista permitida.
	ErrBadAlgorithm = errors.New("autenticação: algoritmo não permitido")
	// ErrBadSignature indica assinatura inválida.
	ErrBadSignature = errors.New("autenticação: assinatura inválida")
	// ErrExpired indica token vencido.
	ErrExpired = errors.New("autenticação: token vencido")
	// ErrWrongAudience indica audiência diferente da esperada.
	ErrWrongAudience = errors.New("autenticação: audiência inválida")
	// ErrWrongIssuer indica emissor diferente do esperado.
	ErrWrongIssuer = errors.New("autenticação: emissor inválido")
)

// Principal é a identidade autenticada: quem chama, de qual provedor (vazio no serviço interno) e com quais papéis.
type Principal struct {
	Subject    string
	ProviderID string
	Roles      []string
	ExpiresAt  time.Time
}

// HasRole diz se a identidade tem o papel.
func (p *Principal) HasRole(role string) bool {
	return slices.Contains(p.Roles, role)
}

// Identity converte para o formato que os casos de uso enxergam.
func (p *Principal) Identity() application.Identity {
	return application.Identity{ProviderID: p.ProviderID, Roles: append([]string(nil), p.Roles...)}
}
