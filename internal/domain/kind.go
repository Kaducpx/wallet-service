package domain

import "fmt"

// Kind é o tipo da operação. OPENING é só interno (abertura de carteira);
// os outros vêm dos provedores.
type Kind string

const (
	KindOpening  Kind = "OPENING"  // crédito inicial na abertura da carteira (interno)
	KindBet      Kind = "BET"      // aposta: débito
	KindWin      Kind = "WIN"      // ganho: crédito
	KindLoss     Kind = "LOSS"     // perda: sem movimentação (o débito já ocorreu na BET)
	KindRefund   Kind = "REFUND"   // devolução integral de uma BET: crédito
	KindRollback Kind = "ROLLBACK" // desfaz uma BET, WIN ou REFUND: movimento contrário
)

// ParseExternalKind aceita só os tipos de provedor. OPENING tem erro próprio.
func ParseExternalKind(s string) (Kind, error) {
	switch k := Kind(s); k {
	case KindBet, KindWin, KindLoss, KindRefund, KindRollback:
		return k, nil
	case KindOpening:
		return "", ErrOpeningNotAllowed
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidKind, s)
	}
}

// RequiresReference: REFUND e ROLLBACK sempre apontam para outra operação.
func (k Kind) RequiresReference() bool { return k == KindRefund || k == KindRollback }

// IsReversal: REFUND e ROLLBACK desfazem outra operação.
func (k Kind) IsReversal() bool { return k.RequiresReference() }

// Origin separa operação interna (OPENING) de externa (provedor).
type Origin string

const (
	OriginInternal Origin = "INTERNAL" // criada pelo próprio serviço (abertura de carteira)
	OriginExternal Origin = "EXTERNAL" // enviada por um provedor via HTTP ou SQS
)
