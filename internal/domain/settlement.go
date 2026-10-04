package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"wallet-service/internal/domain/money"
)

// Regras dos cinco tipos externos. Função pura: recebe a carteira (já
// travada no banco), a operação e a referência, e devolve a decisão. Não
// acessa banco nem rede.
//
//	BET       debita; sem saldo -> REJECTED INSUFFICIENT_FUNDS
//	WIN       credita; referência opcional (precisa ser BET da mesma rodada)
//	LOSS      não mexe no saldo; só registra o resultado
//	REFUND    credita; devolve uma BET inteira
//	ROLLBACK  desfaz BET (credita), WIN ou REFUND (debita);
//	          sem saldo -> REJECTED REVERSAL_INSUFFICIENT_FUNDS

// ReferenceLookup é o resultado da busca pela referência.
type ReferenceLookup struct {
	Tx *WagerTransaction // nil se ainda não chegou
	// Já existe REFUND/ROLLBACK processado para ela? (o banco também garante
	// isso com um índice único)
	AlreadyReversed bool
}

// Outcome resume o que aconteceu com a operação.
type Outcome string

const (
	OutcomeProcessed         Outcome = "PROCESSED"
	OutcomeRejected          Outcome = "REJECTED"
	OutcomeAwaitingReference Outcome = "AWAITING_REFERENCE" // acabou de entrar em espera
	OutcomeRetryScheduled    Outcome = "RETRY_SCHEDULED"    // continua esperando
)

// Settlement é a decisão. A aplicação grava tudo num único commit.
type Settlement struct {
	Outcome Outcome
	Entry   *LedgerEntry  // nil se o saldo não mudou
	Events  []DomainEvent // vão para a outbox
}

// SettleInput é o que Settle precisa para decidir.
type SettleInput struct {
	Wallet    *Wallet
	Tx        *WagerTransaction // em PENDING ou PENDING_REFERENCE
	Reference ReferenceLookup
	EntryID   uuid.UUID // id do lançamento, se houver
	Policy    PendingPolicy
	Now       time.Time
}

// needsReference: REFUND e ROLLBACK sempre; WIN só se informar a aposta.
func needsReference(t *WagerTransaction) bool {
	e := t.External()
	return e != nil && e.ReferenceExternalTransactionID != ""
}

// Settle aplica a regra do tipo da operação.
//
// Rejeição de negócio não é erro: vira Outcome REJECTED com código e evento,
// porque é um resultado válido e definitivo. Erro só sai quando algo
// impossível acontece (bug ou dado corrompido).
func Settle(in SettleInput) (Settlement, error) {
	w, t, now := in.Wallet, in.Tx, in.Now.UTC()

	// Se isto falhar é bug de quem chamou, não regra de negócio.
	if w == nil || t == nil {
		return Settlement{}, fmt.Errorf("%w: wallet and transaction are required", ErrInvalidTransaction)
	}
	if t.Status() != StatusPending && t.Status() != StatusPendingReference {
		return Settlement{}, fmt.Errorf("%w: cannot settle a %s transaction", ErrInvalidStateTransition, t.Status())
	}
	if t.WalletID() != w.ID() {
		return Settlement{}, fmt.Errorf("%w: transaction wallet differs from locked wallet", ErrInvalidTransaction)
	}

	// A operação tem que bater com a carteira: mesmo jogador e mesma moeda.
	if t.PlayerID() != w.PlayerID() {
		return reject(t, w, FailureWalletPlayerMismatch, nil, now)
	}
	if t.Money().Currency() != w.Currency() {
		return reject(t, w, FailureWalletCurrencyMismatch, nil, now)
	}

	var ref *WagerTransaction
	if needsReference(t) {
		r := in.Reference.Tx
		// Referência ainda não chegou (ou ainda está sendo decidida): espera.
		if r == nil || !r.Status().IsTerminal() {
			return awaitReference(t, in.Policy, now)
		}
		refID := r.ID()
		// Não dá para reverter algo que foi rejeitado ou falhou.
		if r.Status() != StatusProcessed {
			return reject(t, w, FailureReferenceNotProcessed, &refID, now)
		}
		if code, ok := validateReference(t, r); !ok {
			return reject(t, w, code, &refID, now)
		}
		// Cada transação só pode ser revertida uma vez.
		if t.Kind().IsReversal() && in.Reference.AlreadyReversed {
			return reject(t, w, FailureReferenceAlreadyReversed, &refID, now)
		}
		ref = r
	}

	var refID *uuid.UUID
	if ref != nil {
		id := ref.ID()
		refID = &id
	}
	var entry *LedgerEntry
	var err error
	switch t.Kind() {
	case KindBet:
		entry, err = debit(w, in.EntryID, t, now)
		if errors.Is(err, ErrInsufficientFunds) {
			return reject(t, w, FailureInsufficientFunds, refID, now)
		}
	case KindWin, KindRefund:
		entry, err = credit(w, in.EntryID, t, now)
	case KindRollback:
		// Faz o contrário do original: BET debitou, então credita;
		// WIN/REFUND creditaram, então debita.
		if ref.Kind() == KindBet {
			entry, err = credit(w, in.EntryID, t, now)
		} else {
			entry, err = debit(w, in.EntryID, t, now)
			// Código próprio, para não confundir com aposta sem saldo.
			if errors.Is(err, ErrInsufficientFunds) {
				return reject(t, w, FailureReversalInsufficientFunds, refID, now)
			}
		}
	case KindLoss:
		// O dinheiro já saiu na BET. Nada a fazer no saldo.
	default:
		return Settlement{}, fmt.Errorf("%w: %s", ErrInvalidKind, t.Kind())
	}
	if errors.Is(err, money.ErrAmountOverflow) {
		return reject(t, w, FailureBalanceLimitExceeded, refID, now)
	}
	if err != nil {
		return Settlement{}, err
	}

	// Guarda o saldo resultante: é o que um replay vai devolver.
	if err := t.MarkProcessed(w.Balance(), refID, now); err != nil {
		return Settlement{}, err
	}
	s := Settlement{Outcome: OutcomeProcessed, Entry: entry}
	s.Events = append(s.Events, NewWagerTransactionProcessed(t))
	if entry != nil {
		s.Events = append(s.Events, NewWalletBalanceChanged(*entry))
	}
	return s, nil
}

// validateReference confere se a referência combina com a operação. Devolve
// o código da falha e false quando não combina.
func validateReference(t, r *WagerTransaction) (FailureCode, bool) {
	te, re := t.External(), r.External()

	// Mesmo provedor, carteira, jogador, moeda e rodada.
	if re == nil || te.ProviderID != re.ProviderID ||
		r.WalletID() != t.WalletID() || r.PlayerID() != t.PlayerID() ||
		r.Money().Currency() != t.Money().Currency() || re.RoundID != te.RoundID {
		return FailureReferenceMismatch, false
	}

	switch t.Kind() {
	case KindWin, KindRefund:
		// WIN aponta para a aposta da rodada; REFUND devolve uma BET.
		if r.Kind() != KindBet {
			return FailureReferenceInvalidKind, false
		}
	case KindRollback:
		// ROLLBACK desfaz BET, WIN ou REFUND (não LOSS nem outro ROLLBACK).
		if r.Kind() != KindBet && r.Kind() != KindWin && r.Kind() != KindRefund {
			return FailureReferenceInvalidKind, false
		}
	}

	// Reversão é sempre do valor inteiro.
	if t.Kind().IsReversal() && !t.Money().Equal(r.Money()) {
		return FailureReferenceAmountMismatch, false
	}
	return "", true
}

func debit(w *Wallet, entryID uuid.UUID, t *WagerTransaction, now time.Time) (*LedgerEntry, error) {
	e, err := w.Debit(entryID, t.ID(), t.Money(), now)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func credit(w *Wallet, entryID uuid.UUID, t *WagerTransaction, now time.Time) (*LedgerEntry, error) {
	e, err := w.Credit(entryID, t.ID(), t.Money(), now)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// reject fecha a operação como REJECTED, guardando o saldo do momento e
// gerando o evento de rejeição. O saldo não muda.
func reject(t *WagerTransaction, w *Wallet, code FailureCode, refID *uuid.UUID, now time.Time) (Settlement, error) {
	bal := w.Balance()
	if err := t.MarkRejected(code, &bal, refID, now); err != nil {
		return Settlement{}, err
	}
	return Settlement{Outcome: OutcomeRejected, Events: []DomainEvent{NewWagerTransactionRejected(t)}}, nil
}
