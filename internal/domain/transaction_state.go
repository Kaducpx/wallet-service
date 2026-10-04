package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"wallet-service/internal/domain/money"
)

// Transições de estado. Cada Mark* muda o status e grava junto os dados
// daquele estado (saldo, código de falha, agenda de retry), então não dá para
// ficar, por exemplo, REJECTED sem código.

// transition é o único lugar que altera o status. Mudança fora da tabela
// allowedTransitions (ex.: PROCESSED -> REJECTED) vira erro.
func (t *WagerTransaction) transition(to Status, now time.Time) error {
	if !canTransition(t.status, to) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidStateTransition, t.status, to)
	}
	t.status = to
	t.updatedAt = now.UTC()
	return nil
}

// MarkProcessed conclui a operação. O saldo fica gravado para que um replay,
// mesmo dias depois, devolva o saldo daquele momento e não o atual.
func (t *WagerTransaction) MarkProcessed(balance money.Money, referenceTxID *uuid.UUID, now time.Time) error {
	if !balance.IsInitialized() {
		return money.ErrUninitialized
	}
	if err := t.transition(StatusProcessed, now); err != nil {
		return err
	}
	b := balance
	t.resultBalance = &b
	t.referenceTransactionID = referenceTxID
	t.clearRetry()
	return nil
}

// MarkRejected recusa a operação por regra de negócio. O código é obrigatório.
func (t *WagerTransaction) MarkRejected(code FailureCode, observedBalance *money.Money, referenceTxID *uuid.UUID, now time.Time) error {
	if code == "" {
		return fmt.Errorf("%w: failure code is required", ErrInvalidTransaction)
	}
	if err := t.transition(StatusRejected, now); err != nil {
		return err
	}
	t.failureCode = code
	t.resultBalance = observedBalance
	t.referenceTransactionID = referenceTxID
	t.clearRetry()
	return nil
}

// MarkFailed registra uma falha permanente, para auditoria. Falha passageira
// (banco fora do ar, por exemplo) não usa isto: a operação só não é gravada
// e é tentada de novo.
func (t *WagerTransaction) MarkFailed(code FailureCode, now time.Time) error {
	if code == "" {
		return fmt.Errorf("%w: failure code is required", ErrInvalidTransaction)
	}
	if err := t.transition(StatusFailed, now); err != nil {
		return err
	}
	t.failureCode = code
	t.clearRetry()
	return nil
}

// MarkPendingReference coloca em espera. nextRetryAt é a próxima tentativa;
// expiresAt é o prazo final.
func (t *WagerTransaction) MarkPendingReference(nextRetryAt, expiresAt, now time.Time) error {
	if t.external == nil || t.external.ReferenceExternalTransactionID == "" {
		return fmt.Errorf("%w: only operations with a reference can wait for it", ErrInvalidStateTransition)
	}
	if !expiresAt.After(now) {
		return fmt.Errorf("%w: expiresAt must be in the future", ErrInvalidTransaction)
	}
	if err := t.transition(StatusPendingReference, now); err != nil {
		return err
	}
	n, e := nextRetryAt.UTC(), expiresAt.UTC()
	t.attempts = 0
	t.nextRetryAt = &n
	t.expiresAt = &e
	return nil
}

// ScheduleRetry conta mais uma tentativa sem sucesso. Não muda o status:
// continua em PENDING_REFERENCE.
func (t *WagerTransaction) ScheduleRetry(nextRetryAt, now time.Time) error {
	if t.status != StatusPendingReference {
		return fmt.Errorf("%w: retry only allowed in PENDING_REFERENCE", ErrInvalidStateTransition)
	}
	n := nextRetryAt.UTC()
	t.attempts++
	t.nextRetryAt = &n
	t.updatedAt = now.UTC()
	return nil
}

func (t *WagerTransaction) clearRetry() { t.nextRetryAt = nil }
