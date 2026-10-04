package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"wallet-service/internal/app"
	"wallet-service/internal/domain"
	"wallet-service/internal/domain/money"
)

// InsertLedgerEntry grava um lançamento. Não existe update nem delete. O
// banco confere a conta (CHECK) e, no COMMIT, se bate com o saldo.
func (r *txRepo) InsertLedgerEntry(ctx context.Context, e domain.LedgerEntry) error {
	_, err := r.tx.Exec(ctx, `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction, amount_units, currency,
			balance_before_units, balance_after_units, wallet_version, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		e.ID(), e.WalletID(), e.TransactionID(), string(e.Direction()), e.Amount().Units(),
		e.Amount().Currency().String(), e.BalanceBefore().Units(), e.BalanceAfter().Units(),
		e.WalletVersion(), e.CreatedAt())
	return wrap("insert ledger entry", err)
}

// ListLedger pagina por wallet_version (única e crescente por carteira).
func (r *txRepo) ListLedger(ctx context.Context, walletID uuid.UUID, afterVersion int64, limit int) ([]domain.LedgerEntry, error) {
	rows, err := r.tx.Query(ctx, `
		SELECT id, wallet_id, transaction_id, direction, amount_units, currency,
		       balance_before_units, balance_after_units, wallet_version, created_at
		  FROM wallet_ledger_entries
		 WHERE wallet_id = $1 AND wallet_version > $2
		 ORDER BY wallet_version
		 LIMIT $3`, walletID, afterVersion, limit)
	if err != nil {
		return nil, wrap("list ledger", err)
	}
	defer rows.Close()
	var out []domain.LedgerEntry
	for rows.Next() {
		var (
			id, wID, txID         uuid.UUID
			dir, currency         string
			amount, before, after int64
			version               int64
			created               time.Time
		)
		if err := rows.Scan(&id, &wID, &txID, &dir, &amount, &currency, &before, &after, &version, &created); err != nil {
			return nil, wrap("scan ledger", err)
		}
		cur, err := money.ParseCurrency(currency)
		if err != nil {
			return nil, err
		}
		m := func(u int64) money.Money { v, _ := money.FromUnits(u, cur); return v }
		e, err := domain.RehydrateLedgerEntry(domain.LedgerEntryParams{
			ID: id, WalletID: wID, TransactionID: txID, Direction: domain.Direction(dir),
			Amount: m(amount), BalanceBefore: m(before), BalanceAfter: m(after),
			WalletVersion: version, CreatedAt: created,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, wrap("iterate ledger", rows.Err())
}

// LedgerTotals soma créditos e débitos. Se a soma estourar BIGINT, o banco
// dá erro em vez de devolver valor errado.
func (r *txRepo) LedgerTotals(ctx context.Context, walletID uuid.UUID) (app.LedgerTotals, error) {
	var t app.LedgerTotals
	err := r.tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_units) FILTER (WHERE direction = 'CREDIT'), 0)::BIGINT,
		       COALESCE(SUM(amount_units) FILTER (WHERE direction = 'DEBIT'), 0)::BIGINT,
		       COUNT(*)
		  FROM wallet_ledger_entries WHERE wallet_id = $1`, walletID).Scan(&t.CreditUnits, &t.DebitUnits, &t.Entries)
	return t, wrap("ledger totals", err)
}
