package auth

const (
	RoleProvider = "wager-provider"
	RoleOperator = "wallet-operator"
)

// Principal é quem fez a chamada.
type Principal struct {
	Subject    string
	ClientID   string
	ProviderID string
	roles      map[string]bool
}

func (p Principal) HasRole(r string) bool { return p.roles[r] }

// Provedor precisa ter o papel e um provider_id no token.
func (p Principal) IsProvider() bool { return p.HasRole(RoleProvider) && p.ProviderID != "" }

func (p Principal) IsOperator() bool { return p.HasRole(RoleOperator) }

// Provedor só acessa o que é dele; operador acessa tudo.
func (p Principal) CanAccessProvider(providerID string) bool {
	return p.IsOperator() || (p.IsProvider() && p.ProviderID == providerID)
}
