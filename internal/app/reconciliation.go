package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"

	"wallet-service/internal/domain/money"
)

// Reconciliation compara o saldo guardado com o saldo refeito pelo ledger.
type Reconciliation struct {
	WalletID          uuid.UUID
	StoredBalance     money.Money
	CalculatedBalance money.Money
	Difference        money.Money // armazenado - reconstruído
	Consistent        bool
	CheckedEntries    int64
}

// Reconcile soma créditos menos débitos do ledger e compara com o saldo
// guardado. Não altera nada.
//
// Divergência aparece na resposta, num log de erro e numa métrica.
//
// Usa REPEATABLE READ para ler saldo e ledger do mesmo momento; sem isso,
// uma aposta no meio do caminho daria uma divergência falsa.
func (s *WalletService) Reconcile(ctx context.Context, walletID uuid.UUID) (Reconciliation, error) {
	var r Reconciliation
	err := s.tx.ReadOnly(ctx, func(ctx context.Context, tx Tx) error {
		w, err := tx.GetWallet(ctx, walletID)
		if err != nil {
			return err
		}
		totals, err := tx.LedgerTotals(ctx, walletID)
		if err != nil {
			return err
		}
		credits, err := money.FromUnits(totals.CreditUnits, w.Currency())
		if err != nil {
			return err
		}
		debits, err := money.FromUnits(totals.DebitUnits, w.Currency())
		if err != nil {
			return err
		}
		calculated, err := credits.Sub(debits)
		if err != nil {
			return err
		}
		diff, err := w.Balance().Sub(calculated)
		if err != nil {
			return err
		}
		r = Reconciliation{
			WalletID: walletID, StoredBalance: w.Balance(), CalculatedBalance: calculated,
			Difference: diff, Consistent: diff.IsZero(), CheckedEntries: totals.Entries,
		}
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return Reconciliation{}, ErrWalletNotFound
	}
	if err != nil {
		return Reconciliation{}, err
	}
	if !r.Consistent {
		s.metrics.ReconciliationDivergence()
		s.log.ErrorContext(ctx, "reconciliation divergence",
			slog.String("walletId", walletID.String()),
			slog.String("stored", r.StoredBalance.Amount()),
			slog.String("calculated", r.CalculatedBalance.Amount()),
			slog.String("difference", r.Difference.Amount()))
	}
	return r, nil
}
