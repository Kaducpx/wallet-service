-- Reversão completa do schema financeiro (apenas para ambientes locais/teste:
-- em produção o ledger nunca deve ser descartado).
DROP TABLE IF EXISTS outbox_events;
DROP FUNCTION IF EXISTS outbox_guard();

DROP TABLE IF EXISTS inbox_messages;

DROP TABLE IF EXISTS wallet_ledger_entries;
DROP FUNCTION IF EXISTS ledger_requires_processed_transaction();
DROP FUNCTION IF EXISTS ledger_append_only();

DROP TABLE IF EXISTS wager_transactions;
DROP FUNCTION IF EXISTS wager_transactions_guard();

DROP TABLE IF EXISTS wallets;
DROP FUNCTION IF EXISTS wallets_require_ledger();
DROP FUNCTION IF EXISTS wallets_guard();
