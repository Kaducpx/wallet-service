package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"wallet-service/internal/domain"
)

// Retomada das operações em PENDING_REFERENCE. Quem chama é o worker de
// pendências: a referência chegou? processa. Ainda não? agenda de novo.
// Prazo acabou? rejeita.

// Resume pode rodar em qualquer instância: trava a carteira, relê a operação
// e, se outra instância já resolveu, não faz nada.
func (s *WagerService) Resume(ctx context.Context, ref PendingRef) (domain.Outcome, error) {
	var outcome domain.Outcome
	var err error
	for attempt := 0; attempt < maxConflictRetries; attempt++ {
		outcome, err = s.resumeOnce(ctx, ref)
		if !isConflict(err) {
			break
		}
		s.metrics.ConcurrencyConflict("wager.resume")
	}
	// Deu certo, ou foi falha passageira (continua pendente e tenta depois).
	if err == nil || IsTransient(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return outcome, err
	}
	// Erro que não vai se resolver sozinho (dado inconsistente): marca FAILED
	// para auditoria em vez de tentar para sempre.
	s.log.ErrorContext(ctx, "permanent failure resuming pending transaction",
		slog.String("transactionId", ref.TransactionID.String()), slog.String("error", err.Error()))
	if ferr := s.markFailed(ctx, ref.TransactionID); ferr != nil {
		return "", errors.Join(err, ferr)
	}
	s.metrics.WagerResult(ChannelPending, string(domain.StatusFailed), false)
	return "", err
}

func (s *WagerService) resumeOnce(ctx context.Context, ref PendingRef) (domain.Outcome, error) {
	var outcome domain.Outcome
	var final *domain.WagerTransaction
	err := s.tx.InTx(ctx, func(ctx context.Context, tx Tx) error {
		// Mesma ordem de lock do Submit (carteira, depois transação). Ordens
		// diferentes poderiam causar deadlock.
		w, err := tx.LockWallet(ctx, ref.WalletID)
		if err != nil {
			return err
		}
		t, meta, err := tx.LockTransaction(ctx, ref.TransactionID)
		if err != nil {
			return err
		}
		if t.Status() != domain.StatusPendingReference {
			return nil // outra instância já resolveu
		}

		lookup, err := s.lookupReference(ctx, tx, t)
		if err != nil {
			return err
		}
		now := s.now()
		expectedVersion := w.Version()
		st, err := domain.Settle(domain.SettleInput{
			Wallet: w, Tx: t, Reference: lookup, EntryID: newID(), Policy: s.policy, Now: now,
		})
		if err != nil {
			return fmt.Errorf("settle pending: %w", err)
		}

		// Aqui é UPDATE (a transação já existe). Os eventos usam o
		// correlationId original da operação.
		if err := tx.UpdateTransaction(ctx, t); err != nil {
			return err
		}
		if err := s.persistSettlement(ctx, tx, w, t, st, expectedVersion, meta.CorrelationID, t.ID().String(), now); err != nil {
			return err
		}
		outcome, final = st.Outcome, t
		return nil
	})
	if err == nil && final != nil && final.Status().IsTerminal() {
		s.metrics.WagerResult(ChannelPending, string(final.Status()), false)
		s.log.InfoContext(ctx, "pending transaction resolved",
			slog.String("transactionId", final.ID().String()),
			slog.String("walletId", final.WalletID().String()),
			slog.String("providerId", final.ProviderID()),
			slog.String("status", string(final.Status())),
			slog.String("failureCode", string(final.FailureCode())))
	}
	return outcome, err
}

// markFailed roda numa transação separada, porque a anterior falhou e foi
// desfeita.
func (s *WagerService) markFailed(ctx context.Context, id uuid.UUID) error {
	return s.tx.InTx(ctx, func(ctx context.Context, tx Tx) error {
		t, _, err := tx.LockTransaction(ctx, id)
		if err != nil {
			return err
		}
		if t.Status().IsTerminal() {
			return nil
		}
		if err := t.MarkFailed(domain.FailurePermanentProcessingError, s.now()); err != nil {
			return err
		}
		return tx.UpdateTransaction(ctx, t)
	})
}
