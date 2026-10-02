package application

import (
	"fmt"
	"slices"
)

// Papéis conhecidos pelo serviço.
const (
	RoleProvider = "provider"
	RoleInternal = "internal"
)

// Identity é quem chama, do jeito que os casos de uso enxergam: o provedor (vazio no serviço interno) e os papéis. O HTTP preenche a partir do token; o SQS e os workers usam o caminho interno, sem identidade.
type Identity struct {
	ProviderID string
	Roles      []string
}

// HasRole diz se a identidade tem o papel.
func (i Identity) HasRole(role string) bool {
	return slices.Contains(i.Roles, role)
}

// errForbidden indica acesso negado (o HTTP vira 403).

func requireInternal(ident Identity) error {
	if !ident.HasRole(RoleInternal) {
		return fmt.Errorf("%w: operação interna", ErrForbidden)
	}
	return nil
}

func requireProvider(ident Identity, providerID string) error {
	if !ident.HasRole(RoleProvider) || ident.ProviderID == "" {
		return fmt.Errorf("%w: operação de provedor", ErrForbidden)
	}
	if ident.ProviderID != providerID {
		return fmt.Errorf("%w: provedor %q", ErrForbidden, providerID)
	}
	return nil
}
