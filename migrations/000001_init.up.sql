-- =============================================================================
-- 000001 - Schema financeiro
--
-- As invariantes financeiras ficam no banco, independentemente da aplicação:
--   * saldo nunca negativo (CHECK);
--   * uma carteira por (player_id, currency) (UNIQUE);
--   * ledger append-only (triggers bloqueiam UPDATE/DELETE/TRUNCATE);
--   * cada mudança de saldo exige o lançamento correspondente no MESMO commit
--     (constraint trigger DEFERRABLE verificada no COMMIT);
--   * idempotência: (provider_id, idempotency_key) e
--     (provider_id, external_transaction_id) únicos;
--   * no máximo uma reversão bem-sucedida por transação referenciada;
--   * transação terminal é imutável.
--
-- Dinheiro: unidades mínimas (centavos) em BIGINT + moeda CHAR(3). Nenhuma
-- coluna usa REAL/DOUBLE PRECISION.
-- =============================================================================

-- -----------------------------------------------------------------------------
-- Carteiras
-- -----------------------------------------------------------------------------
CREATE TABLE wallets (
    id            UUID        PRIMARY KEY,
    player_id     UUID        NOT NULL,
    currency      CHAR(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    balance_units BIGINT      NOT NULL,
    version       BIGINT      NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,
    CONSTRAINT wallets_balance_non_negative CHECK (balance_units >= 0),
    CONSTRAINT wallets_version_positive     CHECK (version >= 1),
    CONSTRAINT wallets_player_currency_key  UNIQUE (player_id, currency),
    -- Alvo das FKs compostas que garantem a moeda do ledger = moeda da carteira.
    CONSTRAINT wallets_id_currency_key      UNIQUE (id, currency)
);

-- Protege identidade e regra de versão: a versão só avança (+1) quando o
-- saldo muda e nunca muda sem mudança de saldo. Carteiras não são apagadas.
CREATE FUNCTION wallets_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'wallets cannot be deleted (wallet %)', OLD.id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF NEW.id <> OLD.id OR NEW.player_id <> OLD.player_id
       OR NEW.currency <> OLD.currency OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'wallet identity is immutable (wallet %)', OLD.id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF NEW.balance_units <> OLD.balance_units AND NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'balance change must increment version by one (wallet %)', OLD.id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF NEW.balance_units = OLD.balance_units AND NEW.version <> OLD.version THEN
        RAISE EXCEPTION 'version can only change with the balance (wallet %)', OLD.id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER wallets_guard
    BEFORE UPDATE OR DELETE ON wallets
    FOR EACH ROW EXECUTE FUNCTION wallets_guard();

-- -----------------------------------------------------------------------------
-- Transações de aposta (operações externas e OPENING interno)
-- -----------------------------------------------------------------------------
CREATE TABLE wager_transactions (
    id                                UUID        PRIMARY KEY,
    origin                            TEXT        NOT NULL CHECK (origin IN ('INTERNAL', 'EXTERNAL')),
    kind                              TEXT        NOT NULL CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    -- PENDING nunca é confirmado: a operação é decidida no mesmo commit.
    -- O único estado não terminal persistido é PENDING_REFERENCE.
    status                            TEXT        NOT NULL CHECK (status IN ('PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    wallet_id                         UUID        NOT NULL REFERENCES wallets (id),
    player_id                         UUID        NOT NULL,
    amount_units                      BIGINT      NOT NULL CHECK (amount_units >= 0),
    currency                          CHAR(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    -- Metadados externos (NULL para OPENING).
    provider_id                       TEXT,
    external_transaction_id           TEXT,
    idempotency_key                   TEXT,
    payload_hash                      TEXT,
    round_id                          TEXT,
    game_id                           TEXT,
    reference_external_transaction_id TEXT,
    -- Resultado.
    reference_transaction_id          UUID REFERENCES wager_transactions (id),
    failure_code                      TEXT,
    result_balance_units              BIGINT,
    result_balance_currency           CHAR(3),
    -- Espera por referência.
    attempts                          INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_retry_at                     TIMESTAMPTZ,
    expires_at                        TIMESTAMPTZ,
    -- Rastreamento (fora do domínio).
    correlation_id                    TEXT        NOT NULL,
    created_at                        TIMESTAMPTZ NOT NULL,
    updated_at                        TIMESTAMPTZ NOT NULL,

    -- Operações internas e externas têm formatos diferentes.
    CONSTRAINT wager_origin_shape CHECK (
        (origin = 'INTERNAL' AND kind = 'OPENING'
            AND provider_id IS NULL AND external_transaction_id IS NULL
            AND idempotency_key IS NULL AND payload_hash IS NULL
            AND round_id IS NULL AND game_id IS NULL
            AND reference_external_transaction_id IS NULL AND reference_transaction_id IS NULL)
        OR
        (origin = 'EXTERNAL' AND kind <> 'OPENING'
            AND provider_id IS NOT NULL AND external_transaction_id IS NOT NULL
            AND idempotency_key IS NOT NULL AND payload_hash IS NOT NULL
            AND round_id IS NOT NULL AND game_id IS NOT NULL)
    ),
    -- Política de valores: LOSS = 0; demais > 0.
    CONSTRAINT wager_amount_policy CHECK (
        (kind = 'LOSS' AND amount_units = 0) OR (kind <> 'LOSS' AND amount_units > 0)
    ),
    CONSTRAINT wager_reference_required CHECK (
        kind NOT IN ('REFUND', 'ROLLBACK') OR reference_external_transaction_id IS NOT NULL
    ),
    -- failure_code existe se, e somente se, a transação falhou/foi rejeitada.
    CONSTRAINT wager_failure_code CHECK (
        (status IN ('REJECTED', 'FAILED')) = (failure_code IS NOT NULL)
    ),
    CONSTRAINT wager_pending_schedule CHECK (
        status <> 'PENDING_REFERENCE' OR (next_retry_at IS NOT NULL AND expires_at IS NOT NULL)
    ),
    CONSTRAINT wager_processed_result CHECK (
        status <> 'PROCESSED' OR (result_balance_units IS NOT NULL AND result_balance_currency IS NOT NULL)
    ),
    CONSTRAINT wager_result_balance_non_negative CHECK (
        result_balance_units IS NULL OR result_balance_units >= 0
    ),
    -- Reversão concluída sempre aponta para a transação revertida.
    CONSTRAINT wager_reversal_resolved CHECK (
        status <> 'PROCESSED' OR kind NOT IN ('REFUND', 'ROLLBACK') OR reference_transaction_id IS NOT NULL
    ),
    -- Idempotência persistente. NULLs (OPENING) não colidem.
    CONSTRAINT wager_provider_idempotency_key UNIQUE (provider_id, idempotency_key),
    CONSTRAINT wager_provider_external_id_key UNIQUE (provider_id, external_transaction_id),
    -- Alvo da FK composta do ledger (transação pertence à carteira).
    CONSTRAINT wager_id_wallet_key UNIQUE (id, wallet_id)
);

-- Impede crédito inicial duplicado.
CREATE UNIQUE INDEX wager_single_opening_per_wallet
    ON wager_transactions (wallet_id) WHERE kind = 'OPENING';

-- Uma transação recebe no máximo UMA reversão bem-sucedida (REFUND ou
-- ROLLBACK). Isso impede devolver duas vezes o mesmo débito.
CREATE UNIQUE INDEX wager_single_successful_reversal
    ON wager_transactions (reference_transaction_id)
    WHERE status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK');

-- Fila do worker de referências pendentes.
CREATE INDEX wager_pending_due_idx
    ON wager_transactions (next_retry_at) WHERE status = 'PENDING_REFERENCE';
CREATE INDEX wager_pending_by_reference_idx
    ON wager_transactions (provider_id, reference_external_transaction_id) WHERE status = 'PENDING_REFERENCE';
CREATE INDEX wager_wallet_created_idx ON wager_transactions (wallet_id, created_at);

-- Transação terminal é imutável; identidade e conteúdo financeiro nunca mudam;
-- transações não são apagadas.
CREATE FUNCTION wager_transactions_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'wager transactions cannot be deleted (transaction %)', OLD.id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF OLD.status IN ('PROCESSED', 'REJECTED', 'FAILED') THEN
        RAISE EXCEPTION 'terminal transaction % is immutable', OLD.id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF NEW.id <> OLD.id OR NEW.origin <> OLD.origin OR NEW.kind <> OLD.kind
       OR NEW.wallet_id <> OLD.wallet_id OR NEW.player_id <> OLD.player_id
       OR NEW.amount_units <> OLD.amount_units OR NEW.currency <> OLD.currency
       OR NEW.provider_id IS DISTINCT FROM OLD.provider_id
       OR NEW.external_transaction_id IS DISTINCT FROM OLD.external_transaction_id
       OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
       OR NEW.payload_hash IS DISTINCT FROM OLD.payload_hash
       OR NEW.round_id IS DISTINCT FROM OLD.round_id
       OR NEW.game_id IS DISTINCT FROM OLD.game_id
       OR NEW.reference_external_transaction_id IS DISTINCT FROM OLD.reference_external_transaction_id
       OR NEW.correlation_id <> OLD.correlation_id
       OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'immutable columns of transaction % cannot change', OLD.id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER wager_transactions_guard
    BEFORE UPDATE OR DELETE ON wager_transactions
    FOR EACH ROW EXECUTE FUNCTION wager_transactions_guard();

-- -----------------------------------------------------------------------------
-- Ledger (append-only)
-- -----------------------------------------------------------------------------
CREATE TABLE wallet_ledger_entries (
    id                   UUID        PRIMARY KEY,
    wallet_id            UUID        NOT NULL,
    transaction_id       UUID        NOT NULL,
    direction            TEXT        NOT NULL CHECK (direction IN ('DEBIT', 'CREDIT')),
    amount_units         BIGINT      NOT NULL CHECK (amount_units > 0),
    currency             CHAR(3)     NOT NULL,
    balance_before_units BIGINT      NOT NULL CHECK (balance_before_units >= 0),
    balance_after_units  BIGINT      NOT NULL CHECK (balance_after_units >= 0),
    -- Versão da carteira produzida por este lançamento: chave de ordenação
    -- estável e garantia de uma única mudança por versão.
    wallet_version       BIGINT      NOT NULL CHECK (wallet_version >= 1),
    created_at           TIMESTAMPTZ NOT NULL,

    CONSTRAINT ledger_arithmetic CHECK (
        (direction = 'CREDIT' AND balance_after_units = balance_before_units + amount_units) OR
        (direction = 'DEBIT'  AND balance_after_units = balance_before_units - amount_units)
    ),
    CONSTRAINT ledger_wallet_transaction_key UNIQUE (wallet_id, transaction_id),
    CONSTRAINT ledger_wallet_version_key     UNIQUE (wallet_id, wallet_version),
    -- Moeda do lançamento = moeda da carteira.
    CONSTRAINT ledger_wallet_currency_fk FOREIGN KEY (wallet_id, currency) REFERENCES wallets (id, currency),
    -- O lançamento pertence a uma transação DA MESMA carteira.
    CONSTRAINT ledger_transaction_wallet_fk FOREIGN KEY (transaction_id, wallet_id) REFERENCES wager_transactions (id, wallet_id)
);

CREATE FUNCTION ledger_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'wallet_ledger_entries is append-only (% blocked)', TG_OP
        USING ERRCODE = 'integrity_constraint_violation';
END $$;

CREATE TRIGGER ledger_no_update_delete
    BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_append_only();
CREATE TRIGGER ledger_no_truncate
    BEFORE TRUNCATE ON wallet_ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_append_only();

-- No COMMIT: o lançamento só pode existir para uma transação PROCESSED que
-- movimenta saldo (LOSS e rejeições não produzem lançamentos).
CREATE FUNCTION ledger_requires_processed_transaction() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    tx_status TEXT;
    tx_kind   TEXT;
    tx_amount BIGINT;
BEGIN
    SELECT status, kind, amount_units INTO tx_status, tx_kind, tx_amount
      FROM wager_transactions WHERE id = NEW.transaction_id;
    IF tx_status IS DISTINCT FROM 'PROCESSED' OR tx_kind = 'LOSS' OR tx_amount <> NEW.amount_units THEN
        RAISE EXCEPTION 'ledger entry % requires a PROCESSED moving transaction with the same amount', NEW.id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER ledger_requires_processed_transaction
    AFTER INSERT ON wallet_ledger_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ledger_requires_processed_transaction();

-- No COMMIT: toda mudança de saldo da carteira precisa do lançamento que a
-- explica (mesma versão, saldo anterior e posterior). Assim nenhum caminho
-- de código consegue alterar saldo sem ledger, nem gravar ledger divergente.
CREATE FUNCTION wallets_require_ledger() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    expected_before BIGINT;
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.balance_units = 0 THEN
            RETURN NULL; -- abertura com saldo zero não tem lançamento
        END IF;
        expected_before := 0;
    ELSE
        IF NEW.balance_units = OLD.balance_units THEN
            RETURN NULL;
        END IF;
        expected_before := OLD.balance_units;
    END IF;
    PERFORM 1 FROM wallet_ledger_entries
     WHERE wallet_id = NEW.id
       AND wallet_version = NEW.version
       AND balance_before_units = expected_before
       AND balance_after_units = NEW.balance_units;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'balance change of wallet % (version %) has no matching ledger entry', NEW.id, NEW.version
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER wallets_require_ledger
    AFTER INSERT OR UPDATE ON wallets
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION wallets_require_ledger();

-- -----------------------------------------------------------------------------
-- Inbox (deduplicação de mensagens por consumidor)
-- -----------------------------------------------------------------------------
CREATE TABLE inbox_messages (
    consumer_name  TEXT        NOT NULL,
    message_id     TEXT        NOT NULL,
    payload_hash   TEXT        NOT NULL,
    transaction_id UUID REFERENCES wager_transactions (id),
    received_at    TIMESTAMPTZ NOT NULL,
    completed_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (consumer_name, message_id)
);

-- -----------------------------------------------------------------------------
-- Outbox (publicação após commit)
-- -----------------------------------------------------------------------------
CREATE TABLE outbox_events (
    id              UUID        PRIMARY KEY,             -- eventId estável
    aggregate_type  TEXT        NOT NULL,
    aggregate_id    UUID        NOT NULL,
    event_type      TEXT        NOT NULL,
    event_version   INTEGER     NOT NULL CHECK (event_version >= 1),
    -- JSON (não JSONB) preserva os bytes exatos do envelope: republicar envia
    -- o mesmo snapshot.
    payload         JSON        NOT NULL,
    correlation_id  TEXT        NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL,
    attempts        INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL,
    locked_by       TEXT,
    locked_until    TIMESTAMPTZ,
    published_at    TIMESTAMPTZ,
    last_error      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX outbox_pending_idx ON outbox_events (next_attempt_at) WHERE published_at IS NULL;

-- O snapshot do evento é imutável e uma publicação confirmada não é desfeita.
CREATE FUNCTION outbox_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'outbox events cannot be deleted' USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF NEW.id <> OLD.id OR NEW.payload::text <> OLD.payload::text
       OR NEW.event_type <> OLD.event_type OR NEW.aggregate_id <> OLD.aggregate_id
       OR NEW.occurred_at <> OLD.occurred_at THEN
        RAISE EXCEPTION 'outbox event % is an immutable snapshot', OLD.id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF OLD.published_at IS NOT NULL AND NEW.published_at IS DISTINCT FROM OLD.published_at THEN
        RAISE EXCEPTION 'outbox event % already published', OLD.id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER outbox_guard
    BEFORE UPDATE OR DELETE ON outbox_events
    FOR EACH ROW EXECUTE FUNCTION outbox_guard();
