package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"wallet-service/internal/domain/money"
)

// ExternalInfo são os dados que só operações de provedores têm. O OPENING
// (interno) não tem nenhum deles; o banco garante isso com um CHECK.
type ExternalInfo struct {
	ProviderID                     string // vem do token
	ExternalTransactionID          string // id da operação no sistema do provedor
	IdempotencyKey                 string
	PayloadHash                    string // SHA-256 dos campos de negócio
	RoundID                        string
	GameID                         string
	ReferenceExternalTransactionID string // operação referenciada; vazio se não houver
}

// WagerTransaction é o pedido e o resultado ao mesmo tempo: o que o provedor
// quis fazer e o que aconteceu (status, motivo, saldo). É daqui que sai a
// resposta de um reenvio, sem reaplicar nada.
//
// Veja também kind.go, status.go, transaction_state.go e failure_code.go.
type WagerTransaction struct {
	id       uuid.UUID
	origin   Origin
	kind     Kind
	status   Status
	walletID uuid.UUID
	playerID uuid.UUID
	money    money.Money
	external *ExternalInfo // nil para OPENING

	referenceTransactionID *uuid.UUID // id interno da referência, depois de achada
	failureCode            FailureCode
	resultBalance          *money.Money // saldo devolvido ao provedor

	// Só usados em PENDING_REFERENCE.
	attempts    int
	nextRetryAt *time.Time
	expiresAt   *time.Time

	createdAt time.Time
	updatedAt time.Time
}

// ExternalParams descreve uma operação recebida de um provedor.
type ExternalParams struct {
	ID       uuid.UUID
	Kind     Kind
	WalletID uuid.UUID
	PlayerID uuid.UUID
	Money    money.Money
	External ExternalInfo
	Now      time.Time
}

// NewExternalTransaction valida a operação e a cria em PENDING.
//
// LOSS exige 0.00; os outros tipos exigem valor maior que zero. REFUND e
// ROLLBACK exigem referência; WIN pode ter.
//
// Repete validações do app.ParseWagerRequest de propósito: o domínio não
// confia em quem chama.
func NewExternalTransaction(p ExternalParams) (*WagerTransaction, error) {
	if p.ID == uuid.Nil {
		return nil, fmt.Errorf("%w: id is required", ErrInvalidTransaction)
	}
	if p.Kind == KindOpening {
		return nil, ErrOpeningNotAllowed
	}
	if _, err := ParseExternalKind(string(p.Kind)); err != nil {
		return nil, err
	}
	if p.WalletID == uuid.Nil {
		return nil, fieldErr("walletId", ErrInvalidTransaction)
	}
	if p.PlayerID == uuid.Nil {
		return nil, fieldErr("playerId", ErrInvalidTransaction)
	}

	if !p.Money.IsInitialized() {
		return nil, fieldErr("money", money.ErrUninitialized)
	}
	if p.Money.IsNegative() {
		return nil, fieldErr("money", money.ErrNegativeAmount)
	}
	if p.Kind == KindLoss && !p.Money.IsZero() {
		return nil, fieldErr("money", fmt.Errorf("%w: LOSS requires 0.00", ErrInvalidAmountForKind))
	}
	if p.Kind != KindLoss && !p.Money.IsPositive() {
		return nil, fieldErr("money", fmt.Errorf("%w: %s requires amount > 0", ErrInvalidAmountForKind, p.Kind))
	}

	e := p.External
	required := []struct{ field, value string }{
		{"providerId", e.ProviderID}, {"externalTransactionId", e.ExternalTransactionID},
		{"idempotencyKey", e.IdempotencyKey}, {"payloadHash", e.PayloadHash},
		{"roundId", e.RoundID}, {"gameId", e.GameID},
	}
	for _, r := range required {
		if strings.TrimSpace(r.value) == "" {
			return nil, fieldErr(r.field, ErrInvalidTransaction)
		}
	}

	if p.Kind.RequiresReference() && strings.TrimSpace(e.ReferenceExternalTransactionID) == "" {
		return nil, fieldErr("referenceExternalTransactionId", ErrMissingReference)
	}
	if (p.Kind == KindBet || p.Kind == KindLoss) && e.ReferenceExternalTransactionID != "" {
		return nil, fieldErr("referenceExternalTransactionId",
			fmt.Errorf("%w: %s does not accept a reference", ErrInvalidTransaction, p.Kind))
	}
	if e.ReferenceExternalTransactionID == e.ExternalTransactionID {
		return nil, fieldErr("referenceExternalTransactionId",
			fmt.Errorf("%w: transaction cannot reference itself", ErrInvalidTransaction))
	}
	if p.Now.IsZero() {
		return nil, fmt.Errorf("%w: timestamp is required", ErrInvalidTransaction)
	}

	// Copia e para quem chamou não conseguir alterar os metadados depois.
	ext := e
	now := p.Now.UTC()
	return &WagerTransaction{
		id: p.ID, origin: OriginExternal, kind: p.Kind, status: StatusPending,
		walletID: p.WalletID, playerID: p.PlayerID, money: p.Money, external: &ext,
		createdAt: now, updatedAt: now,
	}, nil
}

// OpeningParams descreve o crédito inicial de uma carteira.
type OpeningParams struct {
	ID       uuid.UUID
	WalletID uuid.UUID
	PlayerID uuid.UUID
	Money    money.Money
	Now      time.Time
}

// NewOpeningTransaction cria o crédito inicial (interno). Não tem provedor,
// ids externos, rodada, jogo nem referência.
func NewOpeningTransaction(p OpeningParams) (*WagerTransaction, error) {
	if p.ID == uuid.Nil || p.WalletID == uuid.Nil || p.PlayerID == uuid.Nil {
		return nil, fmt.Errorf("%w: opening ids are required", ErrInvalidTransaction)
	}
	if !p.Money.IsPositive() {
		return nil, fieldErr("initialBalance", ErrNonPositiveAmount)
	}
	if p.Now.IsZero() {
		return nil, fmt.Errorf("%w: timestamp is required", ErrInvalidTransaction)
	}
	now := p.Now.UTC()
	return &WagerTransaction{
		id: p.ID, origin: OriginInternal, kind: KindOpening, status: StatusPending,
		walletID: p.WalletID, playerID: p.PlayerID, money: p.Money,
		createdAt: now, updatedAt: now,
	}, nil
}

// TransactionSnapshot é a transação como está no banco.
type TransactionSnapshot struct {
	ID                     uuid.UUID
	Origin                 Origin
	Kind                   Kind
	Status                 Status
	WalletID               uuid.UUID
	PlayerID               uuid.UUID
	Money                  money.Money
	External               *ExternalInfo
	ReferenceTransactionID *uuid.UUID
	FailureCode            FailureCode
	ResultBalance          *money.Money
	Attempts               int
	NextRetryAt            *time.Time
	ExpiresAt              *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// RehydrateTransaction carrega uma transação do banco sem executar
// transições nem gerar eventos. Registro inconsistente vira erro.
func RehydrateTransaction(s TransactionSnapshot) (*WagerTransaction, error) {
	bad := func(msg string) (*WagerTransaction, error) {
		return nil, fmt.Errorf("%w: rehydrate: %s", ErrInvalidTransaction, msg)
	}

	switch {
	case s.ID == uuid.Nil || s.WalletID == uuid.Nil || s.PlayerID == uuid.Nil:
		return bad("ids are required")
	case !s.Money.IsInitialized():
		return bad("money is required")
	case s.CreatedAt.IsZero() || s.UpdatedAt.IsZero():
		return bad("timestamps are required")
	}
	switch s.Status {
	case StatusPending, StatusPendingReference, StatusProcessed, StatusRejected, StatusFailed:
	default:
		return bad("unknown status " + string(s.Status))
	}
	// OPENING nunca tem dados de provedor; operação externa sempre tem.
	if s.Origin == OriginInternal {
		if s.Kind != KindOpening || s.External != nil {
			return bad("internal transaction must be OPENING without external metadata")
		}
	} else if s.Origin == OriginExternal {
		if s.Kind == KindOpening || s.External == nil {
			return bad("external transaction requires external metadata")
		}
	} else {
		return bad("unknown origin")
	}
	var ext *ExternalInfo
	if s.External != nil {
		e := *s.External
		ext = &e
	}
	return &WagerTransaction{
		id: s.ID, origin: s.Origin, kind: s.Kind, status: s.Status,
		walletID: s.WalletID, playerID: s.PlayerID, money: s.Money, external: ext,
		referenceTransactionID: s.ReferenceTransactionID, failureCode: s.FailureCode,
		resultBalance: s.ResultBalance, attempts: s.Attempts,
		nextRetryAt: s.NextRetryAt, expiresAt: s.ExpiresAt,
		createdAt: s.CreatedAt.UTC(), updatedAt: s.UpdatedAt.UTC(),
	}, nil
}

// Getters. Os que devolvem ponteiro devolvem uma cópia: alterar o retorno
// não muda a transação.

func (t *WagerTransaction) ID() uuid.UUID            { return t.id }
func (t *WagerTransaction) Origin() Origin           { return t.origin }
func (t *WagerTransaction) Kind() Kind               { return t.kind }
func (t *WagerTransaction) Status() Status           { return t.status }
func (t *WagerTransaction) WalletID() uuid.UUID      { return t.walletID }
func (t *WagerTransaction) PlayerID() uuid.UUID      { return t.playerID }
func (t *WagerTransaction) Money() money.Money       { return t.money }
func (t *WagerTransaction) FailureCode() FailureCode { return t.failureCode }
func (t *WagerTransaction) Attempts() int            { return t.attempts }
func (t *WagerTransaction) CreatedAt() time.Time     { return t.createdAt }
func (t *WagerTransaction) UpdatedAt() time.Time     { return t.updatedAt }

// External devolve uma cópia dos metadados externos (nil para OPENING).
func (t *WagerTransaction) External() *ExternalInfo {
	if t.external == nil {
		return nil
	}
	e := *t.external
	return &e
}

func (t *WagerTransaction) ReferenceTransactionID() *uuid.UUID {
	return copyPtr(t.referenceTransactionID)
}
func (t *WagerTransaction) ResultBalance() *money.Money { return copyPtr(t.resultBalance) }
func (t *WagerTransaction) NextRetryAt() *time.Time     { return copyPtr(t.nextRetryAt) }
func (t *WagerTransaction) ExpiresAt() *time.Time       { return copyPtr(t.expiresAt) }

// ProviderID é um atalho seguro: vazio para operações internas.
func (t *WagerTransaction) ProviderID() string {
	if t.external == nil {
		return ""
	}
	return t.external.ProviderID
}

// copyPtr devolve um ponteiro para uma cópia do valor (ou nil).
func copyPtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
