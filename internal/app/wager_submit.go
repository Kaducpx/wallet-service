package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"wallet-service/internal/domain"
)

// Caminho de uma operação de aposta, do recebimento ao commit. HTTP e SQS
// chamam exatamente isto. Bom ponto de partida para entender o serviço.

// Submit processa a operação. Se perder uma disputa de concorrência (outra
// instância gravou a mesma chave primeiro, deadlock...), refaz a transação
// inteira; na segunda volta ela enxerga o que foi gravado e responde replay.
func (s *WagerService) Submit(ctx context.Context, cmd SubmitCommand) (SubmitResult, error) {
	start := s.now()
	defer func() { s.metrics.ProcessingDuration(cmd.Channel, time.Since(start)) }()

	if err := s.faults.Hit(FaultWagerSubmit); err != nil { // só nos testes
		return SubmitResult{}, err
	}

	var res SubmitResult
	var err error
	for attempt := 0; attempt < maxConflictRetries; attempt++ {
		res, err = s.submitOnce(ctx, cmd)
		if !isConflict(err) {
			break
		}
		s.metrics.ConcurrencyConflict("wager.submit")
		s.log.WarnContext(ctx, "concurrency conflict, retrying",
			slog.Int("attempt", attempt+1), slog.String("error", err.Error()),
			slog.String("walletId", cmd.Op.WalletID.String()), slog.String("providerId", cmd.Op.ProviderID))
		if sleepErr := sleepCtx(ctx, time.Duration(attempt+1)*10*time.Millisecond); sleepErr != nil {
			return SubmitResult{}, sleepErr
		}
	}
	if err != nil {
		return SubmitResult{}, err
	}
	// Já commitou, mas ainda não respondeu. Os testes derrubam o processo aqui
	// para provar que nada duplica.
	if err := s.faults.Hit(FaultWagerAfterCommit); err != nil {
		return SubmitResult{}, err
	}

	if res.Replay || res.InboxDuplicate {
		kind := "idempotency_key"
		if res.InboxDuplicate {
			kind = "inbox"
		}
		s.metrics.DuplicateDetected(cmd.Channel, kind)
	}
	s.metrics.WagerResult(cmd.Channel, string(res.Tx.Status()), res.Replay)
	s.log.InfoContext(ctx, "wager transaction handled",
		slog.String("transactionId", res.Tx.ID().String()),
		slog.String("walletId", res.Tx.WalletID().String()),
		slog.String("providerId", cmd.Op.ProviderID),
		slog.String("kind", string(res.Tx.Kind())),
		slog.String("status", string(res.Tx.Status())),
		slog.String("failureCode", string(res.Tx.FailureCode())),
		slog.Bool("idempotentReplay", res.Replay),
		slog.String("channel", cmd.Channel))
	return res, nil
}

// submitOnce é uma tentativa, numa única transação SQL. Qualquer erro faz
// rollback de tudo.
func (s *WagerService) submitOnce(ctx context.Context, cmd SubmitCommand) (SubmitResult, error) {
	var res SubmitResult
	op := cmd.Op
	err := s.tx.InTx(ctx, func(ctx context.Context, tx Tx) error {
		// SQS entrega "pelo menos uma vez". Se a inbox já tem este messageId,
		// a mensagem já foi tratada: devolve o resultado gravado.
		if cmd.Inbox != nil {
			rec, err := tx.FindInbox(ctx, cmd.Inbox.Consumer, cmd.Inbox.MessageID)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			if rec != nil {
				// Mesmo messageId com conteúdo diferente: vai para a DLQ.
				if rec.PayloadHash != cmd.Inbox.PayloadHash {
					return ErrInboxPayloadMismatch
				}
				res.InboxDuplicate = true
				res.Replay = true
				res.Tx, err = tx.FindByIdempotencyKey(ctx, op.ProviderID, cmd.IdempotencyKey)
				if errors.Is(err, ErrNotFound) {
					return ErrIdempotencyConflict
				}
				return err
			}
		}

		// Reenvio? Checa antes de travar a carteira, para replays não
		// disputarem o lock com operações novas.
		if existing, err := s.findExisting(ctx, tx, op, cmd.IdempotencyKey); existing != nil || err != nil {
			res.Tx, res.Replay = existing, existing != nil
			return s.completeInbox(ctx, tx, cmd, existing, err)
		}

		// Trava a carteira (FOR UPDATE). Outra operação na mesma carteira, em
		// qualquer instância, espera aqui. Carteiras diferentes não se afetam.
		w, err := tx.LockWallet(ctx, op.WalletID)
		if errors.Is(err, ErrNotFound) {
			return ErrWalletNotFound
		}
		if err != nil {
			return err
		}

		// Checa de novo: uma requisição igual pode ter gravado enquanto
		// esperávamos o lock. Sem isso, a mesma aposta debitaria duas vezes.
		if existing, err := s.findExisting(ctx, tx, op, cmd.IdempotencyKey); existing != nil || err != nil {
			res.Tx, res.Replay = existing, existing != nil
			return s.completeInbox(ctx, tx, cmd, existing, err)
		}

		now := s.now()
		t, err := domain.NewExternalTransaction(domain.ExternalParams{
			ID: newID(), Kind: op.Kind, WalletID: op.WalletID, PlayerID: op.PlayerID, Money: op.Money,
			External: domain.ExternalInfo{
				ProviderID: op.ProviderID, ExternalTransactionID: op.ExternalTransactionID,
				IdempotencyKey: cmd.IdempotencyKey, PayloadHash: op.PayloadHash,
				RoundID: op.RoundID, GameID: op.GameID,
				ReferenceExternalTransactionID: op.ReferenceExternalTransactionID,
			},
			Now: now,
		})
		if err != nil {
			return err
		}
		ref, err := s.lookupReference(ctx, tx, t)
		if err != nil {
			return err
		}
		// Versão lida agora, antes do Settle mexer na carteira. O UPDATE só
		// passa se o banco ainda estiver nela.
		expectedVersion := w.Version()
		st, err := domain.Settle(domain.SettleInput{
			Wallet: w, Tx: t, Reference: ref, EntryID: newID(), Policy: s.policy, Now: now,
		})
		if err != nil {
			return fmt.Errorf("settle: %w", err)
		}

		// Grava transação, saldo, ledger, eventos e inbox. O commit acontece
		// quando esta função retorna nil.
		if err := tx.InsertTransaction(ctx, t, TxMeta{CorrelationID: cmd.CorrelationID}); err != nil {
			return err
		}
		if err := s.persistSettlement(ctx, tx, w, t, st, expectedVersion, cmd.CorrelationID, cmd.CausationID, now); err != nil {
			return err
		}
		res.Tx = t
		return s.completeInbox(ctx, tx, cmd, t, nil)
	})
	return res, err
}
