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

// Colunas na ordem que scanTransaction lê. As que aceitam NULL são lidas em
// ponteiros.
const txColumns = `id, origin, kind, status, wallet_id, player_id, amount_units, currency,
	provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
	reference_external_transaction_id, reference_transaction_id, failure_code,
	result_balance_units, result_balance_currency, attempts, next_retry_at, expires_at,
	correlation_id, created_at, updated_at`

// scanTransaction monta a transação a partir da linha (RehydrateTransaction
// valida tudo) e devolve também o correlationId.
func scanTransaction(row pgx.Row) (*domain.WagerTransaction, app.TxMeta, error) {
	var (
		id, walletID, playerID                        uuid.UUID
		origin, kind, status, currency, correlationID string
		amount                                        int64
		providerID, extID, key, hash, roundID, gameID *string
		refExtID, failureCode, resultCurrency         *string
		refTxID                                       *uuid.UUID
		resultUnits                                   *int64
		attempts                                      int
		nextRetry, expires                            *time.Time
		created, updated                              time.Time
	)
	err := row.Scan(&id, &origin, &kind, &status, &walletID, &playerID, &amount, &currency,
		&providerID, &extID, &key, &hash, &roundID, &gameID,
		&refExtID, &refTxID, &failureCode, &resultUnits, &resultCurrency, &attempts, &nextRetry, &expires,
		&correlationID, &created, &updated)
	if err != nil {
		return nil, app.TxMeta{}, err
	}
	cur, err := money.ParseCurrency(currency)
	if err != nil {
		return nil, app.TxMeta{}, err
	}
	amt, err := money.FromUnits(amount, cur)
	if err != nil {
		return nil, app.TxMeta{}, err
	}
	snap := domain.TransactionSnapshot{
		ID: id, Origin: domain.Origin(origin), Kind: domain.Kind(kind), Status: domain.Status(status),
		WalletID: walletID, PlayerID: playerID, Money: amt,
		ReferenceTransactionID: refTxID, Attempts: attempts, NextRetryAt: nextRetry, ExpiresAt: expires,
		CreatedAt: created, UpdatedAt: updated,
	}
	if failureCode != nil {
		snap.FailureCode = domain.FailureCode(*failureCode)
	}
	if resultUnits != nil && resultCurrency != nil {
		rc, err := money.ParseCurrency(*resultCurrency)
		if err != nil {
			return nil, app.TxMeta{}, err
		}
		rb, err := money.FromUnits(*resultUnits, rc)
		if err != nil {
			return nil, app.TxMeta{}, err
		}
		snap.ResultBalance = &rb
	}
	if snap.Origin == domain.OriginExternal {
		snap.External = &domain.ExternalInfo{
			ProviderID: deref(providerID), ExternalTransactionID: deref(extID),
			IdempotencyKey: deref(key), PayloadHash: deref(hash),
			RoundID: deref(roundID), GameID: deref(gameID),
			ReferenceExternalTransactionID: deref(refExtID),
		}
	}
	t, err := domain.RehydrateTransaction(snap)
	return t, app.TxMeta{CorrelationID: correlationID}, err
}

// InsertTransaction grava uma operação nova. Se outra instância já gravou a
// mesma chave ou o mesmo id externo, o índice único recusa (23505) e a
// aplicação refaz, virando replay.
func (r *txRepo) InsertTransaction(ctx context.Context, t *domain.WagerTransaction, meta app.TxMeta) error {
	// Ponteiro nil vira NULL (caso do OPENING, que não tem esses campos).
	var providerID, extID, key, hash, roundID, gameID, refExtID *string
	if e := t.External(); e != nil {
		providerID, extID, key, hash = &e.ProviderID, &e.ExternalTransactionID, &e.IdempotencyKey, &e.PayloadHash
		roundID, gameID, refExtID = &e.RoundID, &e.GameID, nullIfEmpty(e.ReferenceExternalTransactionID)
	}
	resUnits, resCur := resultBalance(t)
	_, err := r.tx.Exec(ctx, `
		INSERT INTO wager_transactions (
			id, origin, kind, status, wallet_id, player_id, amount_units, currency,
			provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
			reference_external_transaction_id, reference_transaction_id, failure_code,
			result_balance_units, result_balance_currency, attempts, next_retry_at, expires_at,
			correlation_id, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)`,
		t.ID(), string(t.Origin()), string(t.Kind()), string(t.Status()), t.WalletID(), t.PlayerID(),
		t.Money().Units(), t.Money().Currency().String(),
		providerID, extID, key, hash, roundID, gameID,
		refExtID, t.ReferenceTransactionID(), nullIfEmpty(string(t.FailureCode())),
		resUnits, resCur, t.Attempts(), t.NextRetryAt(), t.ExpiresAt(),
		meta.CorrelationID, t.CreatedAt(), t.UpdatedAt())
	return wrap("insert transaction", err)
}

// UpdateTransaction só mexe nas colunas que podem mudar; as outras são
// travadas por trigger e por GRANT.
func (r *txRepo) UpdateTransaction(ctx context.Context, t *domain.WagerTransaction) error {
	resUnits, resCur := resultBalance(t)
	tag, err := r.tx.Exec(ctx, `
		UPDATE wager_transactions
		   SET status = $2, reference_transaction_id = $3, failure_code = $4,
		       result_balance_units = $5, result_balance_currency = $6,
		       attempts = $7, next_retry_at = $8, expires_at = $9, updated_at = $10
		 WHERE id = $1`,
		t.ID(), string(t.Status()), t.ReferenceTransactionID(), nullIfEmpty(string(t.FailureCode())),
		resUnits, resCur, t.Attempts(), t.NextRetryAt(), t.ExpiresAt(), t.UpdatedAt())
	if err != nil {
		return wrap("update transaction", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("update transaction %s: %w", t.ID(), app.ErrNotFound)
	}
	return nil
}

func (r *txRepo) GetTransaction(ctx context.Context, id uuid.UUID) (*domain.WagerTransaction, error) {
	t, _, err := scanTransaction(r.tx.QueryRow(ctx, `SELECT `+txColumns+` FROM wager_transactions WHERE id = $1`, id))
	return t, wrap("get transaction", err)
}

// LockTransaction trava a transação (usado na retomada de pendências).
func (r *txRepo) LockTransaction(ctx context.Context, id uuid.UUID) (*domain.WagerTransaction, app.TxMeta, error) {
	t, meta, err := scanTransaction(r.tx.QueryRow(ctx, `SELECT `+txColumns+` FROM wager_transactions WHERE id = $1 FOR UPDATE`, id))
	return t, meta, wrap("lock transaction", err)
}

// FindByIdempotencyKey: cada provedor tem seu próprio espaço de chaves.
func (r *txRepo) FindByIdempotencyKey(ctx context.Context, providerID, key string) (*domain.WagerTransaction, error) {
	t, _, err := scanTransaction(r.tx.QueryRow(ctx,
		`SELECT `+txColumns+` FROM wager_transactions WHERE provider_id = $1 AND idempotency_key = $2`, providerID, key))
	return t, wrap("find by idempotency key", err)
}

// FindByExternalID busca pelo id do provedor. Também resolve referências.
func (r *txRepo) FindByExternalID(ctx context.Context, providerID, externalID string) (*domain.WagerTransaction, error) {
	t, _, err := scanTransaction(r.tx.QueryRow(ctx,
		`SELECT `+txColumns+` FROM wager_transactions WHERE provider_id = $1 AND external_transaction_id = $2`, providerID, externalID))
	return t, wrap("find by external id", err)
}

// HasProcessedReversal: já existe REFUND/ROLLBACK processado para ela? (O
// índice wager_single_successful_reversal garante no máximo um.)
func (r *txRepo) HasProcessedReversal(ctx context.Context, referenceTxID uuid.UUID) (bool, error) {
	var exists bool
	err := r.tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM wager_transactions
			 WHERE reference_transaction_id = $1
			   AND status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK'))`, referenceTxID).Scan(&exists)
	return exists, wrap("has processed reversal", err)
}

// WakePendingReferencing adianta para agora quem esperava por esta operação.
// Sem isso, o REFUND que chegou antes da BET esperaria o backoff inteiro.
func (r *txRepo) WakePendingReferencing(ctx context.Context, providerID, externalID string) error {
	_, err := r.tx.Exec(ctx, `
		UPDATE wager_transactions
		   SET next_retry_at = now()
		 WHERE status = 'PENDING_REFERENCE'
		   AND provider_id = $1 AND reference_external_transaction_id = $2
		   AND next_retry_at > now()`, providerID, externalID)
	return wrap("wake pending", err)
}
