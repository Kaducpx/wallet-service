package domain

import (
	"math/rand/v2"
	"time"
)

// Espera por referência (PENDING_REFERENCE).
//
// Caso típico: chega um REFUND da "bet-1", mas a "bet-1" ainda não chegou.
// Em vez de rejeitar na hora, a operação espera e um worker tenta de novo
// com intervalos cada vez maiores, até a aposta aparecer ou o prazo acabar.

// PendingPolicy define quanto tempo e quantas vezes esperar.
type PendingPolicy struct {
	TTL         time.Duration // prazo máximo desde que entrou em espera
	MaxAttempts int           // máximo de tentativas do worker
	BaseBackoff time.Duration // primeiro intervalo; dobra a cada tentativa
	MaxBackoff  time.Duration // teto do intervalo
}

// Backoff dá o intervalo da próxima tentativa: base, 2x, 4x... até o teto,
// mais até 20% aleatório para várias instâncias não acordarem juntas.
func (p PendingPolicy) Backoff(attempt int) time.Duration {
	d := p.BaseBackoff
	for i := 0; i < attempt && d < p.MaxBackoff; i++ {
		d *= 2
	}
	if d > p.MaxBackoff {
		d = p.MaxBackoff
	}
	if d <= 0 {
		return 0
	}
	return d + time.Duration(rand.Int64N(int64(d)/5+1))
}

// awaitReference tem três saídas:
//   - primeira vez: entra em PENDING_REFERENCE e emite o evento de espera;
//   - ainda no prazo: só agenda a próxima tentativa (sem evento);
//   - prazo esgotado: REJECTED com REFERENCE_NOT_FOUND.
func awaitReference(t *WagerTransaction, p PendingPolicy, now time.Time) (Settlement, error) {
	if t.Status() == StatusPending {
		if err := t.MarkPendingReference(now.Add(p.Backoff(0)), now.Add(p.TTL), now); err != nil {
			return Settlement{}, err
		}
		return Settlement{
			Outcome: OutcomeAwaitingReference,
			Events:  []DomainEvent{NewWagerTransactionPendingReference(t)},
		}, nil
	}

	attempt := t.Attempts() + 1
	expired := t.ExpiresAt() != nil && !now.Before(*t.ExpiresAt())
	if expired || attempt >= p.MaxAttempts {
		// Sem saldo observado: esta operação nunca tocou na carteira.
		if err := t.MarkRejected(FailureReferenceNotFound, nil, nil, now); err != nil {
			return Settlement{}, err
		}
		return Settlement{Outcome: OutcomeRejected, Events: []DomainEvent{NewWagerTransactionRejected(t)}}, nil
	}
	next := now.Add(p.Backoff(attempt))
	if t.ExpiresAt() != nil && next.After(*t.ExpiresAt()) {
		next = *t.ExpiresAt() // a última tentativa cai exatamente no prazo
	}
	if err := t.ScheduleRetry(next, now); err != nil {
		return Settlement{}, err
	}
	return Settlement{Outcome: OutcomeRetryScheduled}, nil
}
