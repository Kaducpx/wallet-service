//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"wallet-service/internal/adapters/postgres"
)

// As invariantes valem mesmo para quem escreve direto no banco.

// pgCode extrai o SQLSTATE de um erro do Postgres ("" se não houver).
func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// exec roda uma instrução em transação própria e devolve o erro (inclusive
// das constraint triggers verificadas no COMMIT).
func execTx(t *testing.T, url, sql string, args ...any) error {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// As migrations sobem, descem por completo e sobem de novo num banco vazio.
func TestMigrationsUpDownUp(t *testing.T) {
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, fmt.Sprintf("postgres://%s@%s/postgres?sslmode=disable", ownerUser, pgHost))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	name := testDBName + "_mig"
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")

	url := fmt.Sprintf("postgres://%s@%s/%s?sslmode=disable", ownerUser, pgHost, name)
	tables := func() int {
		conn, err := pgx.Connect(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		var n int
		_ = conn.QueryRow(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public'
			AND table_name IN ('wallets','wager_transactions','wallet_ledger_entries','inbox_messages','outbox_events')`).Scan(&n)
		return n
	}
	m, err := postgres.NewMigrator(url)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Up(); err != nil {
		t.Fatal(err)
	}
	if v, dirty, _ := m.Version(); v != 2 || dirty || tables() != 5 {
		t.Fatalf("after up: version=%d dirty=%t tables=%d", v, dirty, tables())
	}
	if err := m.Down(0); err != nil {
		t.Fatal(err)
	}
	if tables() != 0 {
		t.Fatalf("tables after down = %d", tables())
	}
	if err := m.Up(); err != nil {
		t.Fatal(err)
	}
	if tables() != 5 {
		t.Fatal("re-apply failed")
	}
}

// Cada invariante financeira é imposta pelo próprio banco: tentativas de
// violá-la direto via SQL falham com o erro esperado.
func TestDatabaseInvariants(t *testing.T) {
	app := startApp(t, testConfig(newQueues(t, 5, 5)))
	w, p := openWallet(t, app.base, "100.00")
	bet := extID("bet")
	betRes := submit(t, app.base, "provider-a", op("provider-a", bet, p, w, "BET", "10.00", ""))
	refundRes := submit(t, app.base, "provider-a", op("provider-a", extID("refund"), p, w, "REFUND", "10.00", bet))
	betID, refundID := betRes.str("transactionId"), refundRes.str("transactionId")

	cases := []struct {
		name string
		url  string
		sql  string
		args []any
		want string // SQLSTATE ou trecho da mensagem
	}{
		{"ledger UPDATE blocked", ownerDBURL, `UPDATE wallet_ledger_entries SET amount_units = 1 WHERE wallet_id = $1`, []any{w}, "append-only"},
		{"ledger DELETE blocked", ownerDBURL, `DELETE FROM wallet_ledger_entries WHERE wallet_id = $1`, []any{w}, "append-only"},
		{"ledger TRUNCATE blocked", ownerDBURL, `TRUNCATE wallet_ledger_entries CASCADE`, nil, "append-only"},
		{"app role cannot delete ledger", appDBURL, `DELETE FROM wallet_ledger_entries WHERE wallet_id = $1`, []any{w}, "42501"},
		{"app role cannot update ledger", appDBURL, `UPDATE wallet_ledger_entries SET amount_units = 1 WHERE wallet_id = $1`, []any{w}, "42501"},
		{"negative balance", ownerDBURL, `UPDATE wallets SET balance_units = -1, version = version + 1 WHERE id = $1`, []any{w}, "23514"},
		{"balance change without ledger", ownerDBURL, `UPDATE wallets SET balance_units = balance_units + 100, version = version + 1 WHERE id = $1`, []any{w}, "no matching ledger entry"},
		{"version must follow balance", ownerDBURL, `UPDATE wallets SET version = version + 5 WHERE id = $1`, []any{w}, "version can only change"},
		{"duplicate opening", ownerDBURL, `INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_units, currency,
			result_balance_units, result_balance_currency, correlation_id, created_at, updated_at)
			SELECT gen_random_uuid(), 'INTERNAL', 'OPENING', 'PROCESSED', id, player_id, 100, currency, 100, currency, 'x', now(), now()
			  FROM wallets WHERE id = $1`, []any{w}, "23505"},
		{"duplicate ledger for transaction", ownerDBURL, `INSERT INTO wallet_ledger_entries
			(id, wallet_id, transaction_id, direction, amount_units, currency, balance_before_units, balance_after_units, wallet_version, created_at)
			SELECT gen_random_uuid(), wallet_id, transaction_id, direction, amount_units, currency, balance_before_units, balance_after_units, wallet_version + 100, now()
			  FROM wallet_ledger_entries WHERE transaction_id = $1`, []any{betID}, "23505"},
		{"terminal transaction immutable", ownerDBURL, `UPDATE wager_transactions SET failure_code = 'X', status = 'REJECTED' WHERE id = $1`, []any{betID}, "immutable"},
		{"second successful reversal", ownerDBURL, `INSERT INTO wager_transactions
			SELECT gen_random_uuid(), origin, kind, status, wallet_id, player_id, amount_units, currency,
			       provider_id, 'dup-' || external_transaction_id, 'dup-' || idempotency_key, payload_hash, round_id, game_id,
			       reference_external_transaction_id, reference_transaction_id, failure_code, result_balance_units,
			       result_balance_currency, attempts, next_retry_at, expires_at, correlation_id, created_at, updated_at
			  FROM wager_transactions WHERE id = $1`, []any{refundID}, "23505"},
		{"idempotency key unique", ownerDBURL, `INSERT INTO wager_transactions
			SELECT gen_random_uuid(), origin, kind, status, wallet_id, player_id, amount_units, currency,
			       provider_id, 'other-' || external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
			       reference_external_transaction_id, reference_transaction_id, failure_code, result_balance_units,
			       result_balance_currency, attempts, next_retry_at, expires_at, correlation_id, created_at, updated_at
			  FROM wager_transactions WHERE id = $1`, []any{betID}, "23505"},
		{"financial columns immutable", ownerDBURL, `UPDATE wager_transactions SET amount_units = 5 WHERE id = $1`, []any{betID}, "immutable"},
		{"outbox payload immutable", ownerDBURL, `UPDATE outbox_events SET payload = '{}'::json WHERE aggregate_id = $1`, []any{w}, "immutable snapshot"},
	}
	for _, c := range cases {
		err := execTx(t, c.url, c.sql, c.args...)
		if err == nil {
			t.Errorf("%s: expected failure", c.name)
			continue
		}
		if pgCode(err) != c.want && !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: expected %q, got %v", c.name, c.want, err)
		}
	}
	assertConsistent(t, app.base, w)
}
