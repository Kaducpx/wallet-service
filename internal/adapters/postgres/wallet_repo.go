package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"wallet-service/internal/app"
	"wallet-service/internal/domain"
	"wallet-service/internal/domain/money"
)

// Colunas na ordem que scanWallet lê.
const walletColumns = `id, player_id, currency, balance_units, version, created_at, updated_at`

// scanWallet monta a carteira a partir da linha (RehydrateWallet valida).
func scanWallet(row pgx.Row) (*domain.Wallet, error) {
	var (
		id, player       uuid.UUID
		currency         string
		units, version   int64
		created, updated time.Time
	)
	if err := row.Scan(&id, &player, &currency, &units, &version, &created, &updated); err != nil {
		return nil, err
	}
	cur, err := money.ParseCurrency(currency)
	if err != nil {
		return nil, err
	}
	bal, err := money.FromUnits(units, cur)
	if err != nil {
		return nil, err
	}
	return domain.RehydrateWallet(domain.WalletSnapshot{
		ID: id, PlayerID: player, Balance: bal, Version: version, CreatedAt: created, UpdatedAt: updated,
	})
}

// InsertWallet: se já existir carteira na moeda, o índice único recusa
// (vira ErrUniqueViolation).
func (r *txRepo) InsertWallet(ctx context.Context, w *domain.Wallet) error {
	_, err := r.tx.Exec(ctx, `
		INSERT INTO wallets (id, player_id, currency, balance_units, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		w.ID(), w.PlayerID(), w.Currency().String(), w.Balance().Units(), w.Version(), w.CreatedAt(), w.UpdatedAt())
	return wrap("insert wallet", err)
}

// GetWallet lê sem lock (consultas).
func (r *txRepo) GetWallet(ctx context.Context, id uuid.UUID) (*domain.Wallet, error) {
	w, err := scanWallet(r.tx.QueryRow(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1`, id))
	return w, wrap("get wallet", err)
}

// LockWallet trava a linha da carteira até o fim da transação. Outra
// transação na mesma carteira espera aqui; carteiras diferentes não se
// afetam.
//
// É isso que resolve duas apostas de 80 sobre saldo de 100: a segunda
// espera a primeira terminar, lê o saldo já atualizado (20) e é recusada.
// Funciona entre processos porque quem guarda o lock é o Postgres.
func (r *txRepo) LockWallet(ctx context.Context, id uuid.UUID) (*domain.Wallet, error) {
	w, err := scanWallet(r.tx.QueryRow(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1 FOR UPDATE`, id))
	return w, wrap("lock wallet", err)
}

// UpdateWalletBalance só grava se a versão ainda for a lida. Com o lock isso
// sempre bate; é uma segunda barreira contra "lost update" (dois processos
// gravando por cima um do outro) caso algum caminho esqueça o lock.
func (r *txRepo) UpdateWalletBalance(ctx context.Context, w *domain.Wallet, expectedVersion int64) error {
	tag, err := r.tx.Exec(ctx, `
		UPDATE wallets
		   SET balance_units = $2, version = $3, updated_at = $4
		 WHERE id = $1 AND version = $5`,
		w.ID(), w.Balance().Units(), w.Version(), w.UpdatedAt(), expectedVersion)
	if err != nil {
		return wrap("update wallet", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: wallet %s expected version %d", app.ErrConcurrentUpdate, w.ID(), expectedVersion)
	}
	return nil
}
