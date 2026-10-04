-- =============================================================================
-- 000002 - Privilégios mínimos para o papel da aplicação
--
-- As migrations rodam com o dono do schema; a aplicação conecta com o papel
-- wallet_app, que NÃO tem DELETE/TRUNCATE em nenhuma tabela e só pode
-- inserir no ledger. Mesmo um bug ou SQL injection na aplicação não consegue
-- editar ou apagar lançamentos (e os triggers bloqueiam inclusive o dono).
--
-- O papel é criado pelo script de inicialização do Postgres
-- (deploy/postgres/init). Se não existir, esta migration não faz nada.
-- =============================================================================
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'wallet_app') THEN
        GRANT USAGE ON SCHEMA public TO wallet_app;

        GRANT SELECT, INSERT ON wallets TO wallet_app;
        GRANT UPDATE (balance_units, version, updated_at) ON wallets TO wallet_app;

        GRANT SELECT, INSERT ON wager_transactions TO wallet_app;
        GRANT UPDATE (status, reference_transaction_id, failure_code, result_balance_units,
                      result_balance_currency, attempts, next_retry_at, expires_at, updated_at)
            ON wager_transactions TO wallet_app;

        GRANT SELECT, INSERT ON wallet_ledger_entries TO wallet_app;

        GRANT SELECT, INSERT ON inbox_messages TO wallet_app;

        GRANT SELECT, INSERT ON outbox_events TO wallet_app;
        GRANT UPDATE (attempts, next_attempt_at, locked_by, locked_until, published_at, last_error)
            ON outbox_events TO wallet_app;
    END IF;
END $$;
