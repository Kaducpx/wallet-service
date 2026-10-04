package app

import (
	"context"
	"errors"
	"time"

	"wallet-service/internal/domain"
)

// lookupReference busca a operação referenciada, sempre dentro do mesmo
// provedor (um provedor nunca reverte operação de outro). Para REFUND e
// ROLLBACK, também diz se ela já foi revertida.
func (s *WagerService) lookupReference(ctx context.Context, tx Tx, t *domain.WagerTransaction) (domain.ReferenceLookup, error) {
	ext := t.External()
	if ext == nil || ext.ReferenceExternalTransactionID == "" {
		return domain.ReferenceLookup{}, nil
	}
	ref, err := tx.FindByExternalID(ctx, ext.ProviderID, ext.ReferenceExternalTransactionID)
	if errors.Is(err, ErrNotFound) {
		return domain.ReferenceLookup{}, nil // ainda não chegou; o domínio decide esperar
	}
	if err != nil {
		return domain.ReferenceLookup{}, err
	}
	out := domain.ReferenceLookup{Tx: ref}
	if t.Kind().IsReversal() {
		out.AlreadyReversed, err = tx.HasProcessedReversal(ctx, ref.ID())
	}
	return out, err
}

// persistSettlement grava os efeitos da decisão, na mesma transação:
// saldo (se mudou, conferindo a versão), lançamento, eventos na outbox e
// "acorda" quem estava esperando por esta operação.
//
// No COMMIT o banco confere que saldo e lançamento batem; se não baterem, o
// commit falha e nada é gravado.
func (s *WagerService) persistSettlement(ctx context.Context, tx Tx, w *domain.Wallet, t *domain.WagerTransaction,
	st domain.Settlement, expectedVersion int64, correlationID, causationID string, now time.Time) error {
	if st.Entry != nil {
		if err := tx.UpdateWalletBalance(ctx, w, expectedVersion); err != nil {
			return err
		}
		if err := tx.InsertLedgerEntry(ctx, *st.Entry); err != nil {
			return err
		}
	}
	if len(st.Events) > 0 {
		if causationID == "" { // HTTP: a causa é a própria transação
			causationID = t.ID().String()
		}
		msgs, err := buildOutbox(st.Events, correlationID, causationID, now)
		if err != nil {
			return err
		}
		if err := tx.InsertOutbox(ctx, msgs); err != nil {
			return err
		}
	}
	// Esta operação pode ser a referência que outras esperam: antecipa a
	// próxima tentativa delas.
	if st.Outcome == domain.OutcomeProcessed && t.External() != nil {
		return tx.WakePendingReferencing(ctx, t.ProviderID(), t.External().ExternalTransactionID)
	}
	return nil
}
