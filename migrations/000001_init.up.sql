-- Convenções:
-- * dinheiro em BIGINT de unidades mínimas (nunca float, nunca NUMERIC
--   aproximado); moeda em CHAR(3) validado pelo formato ISO 4217;
-- * identificadores gerados na aplicação em UUIDv7; o banco só guarda;
-- * instantes sempre informados pela aplicação em UTC (sem now() implícito);
-- * unicidades que sustentam a idempotência e o ledger vivem aqui, não no
--   código: NULLs são distintos em UNIQUE no PostgreSQL, então as linhas
--   internas (sem provedor) nunca colidem com as externas.

CREATE TABLE wallets (
  id            uuid        PRIMARY KEY,
  player_id     uuid        NOT NULL,
  currency      char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  balance_minor bigint      NOT NULL CHECK (balance_minor >= 0),
  version       bigint      NOT NULL CHECK (version >= 1),
  created_at    timestamptz NOT NULL,
  updated_at    timestamptz NOT NULL,
  CONSTRAINT wallets_player_currency_uk UNIQUE (player_id, currency)
);

CREATE TABLE wager_transactions (
  id                              uuid        PRIMARY KEY,
  origin                          text        NOT NULL CHECK (origin IN ('INTERNAL', 'EXTERNAL')),
  kind                            text        NOT NULL CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
  status                          text        NOT NULL CHECK (status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
  wallet_id                       uuid        NOT NULL REFERENCES wallets (id),
  player_id                       uuid        NOT NULL,
  currency                        char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  amount_minor                    bigint      NOT NULL CHECK (amount_minor >= 0),
  provider_id                     text,
  external_transaction_id         text,
  idempotency_key                 text,
  payload_hash                    bytea,
  round_id                        text,
  game_id                         text,
  reference_external_transaction_id text,
  resolved_reference_id           uuid        REFERENCES wager_transactions (id),
  failure_code                    text,
  failure_detail                  text,
  result_balance_minor            bigint,
  result_wallet_version           bigint,
  attempts                        integer     NOT NULL DEFAULT 0,
  next_attempt_at                 timestamptz,
  expires_at                      timestamptz,
  correlation_id                  text        NOT NULL,
  created_at                      timestamptz NOT NULL,
  updated_at                      timestamptz NOT NULL,
  resolved_at                     timestamptz,

  -- Operação interna (abertura) não carrega metadado externo; operação
  -- externa carrega todos. O banco recusa qualquer mistura.
  CONSTRAINT origin_shape CHECK (
    (origin = 'INTERNAL' AND kind = 'OPENING'
       AND provider_id IS NULL AND external_transaction_id IS NULL
       AND idempotency_key IS NULL AND payload_hash IS NULL
       AND round_id IS NULL AND game_id IS NULL
       AND reference_external_transaction_id IS NULL)
    OR
    (origin = 'EXTERNAL' AND kind <> 'OPENING'
       AND provider_id IS NOT NULL AND external_transaction_id IS NOT NULL
       AND idempotency_key IS NOT NULL AND payload_hash IS NOT NULL
       AND round_id IS NOT NULL AND game_id IS NOT NULL)
  ),
  -- Zero só em LOSS; abertura e demais tipos exigem valor positivo.
  CONSTRAINT amount_by_kind CHECK (
    (kind = 'LOSS' AND amount_minor = 0) OR
    (kind = 'OPENING' AND amount_minor > 0) OR
    (kind IN ('BET', 'WIN', 'REFUND', 'ROLLBACK') AND amount_minor > 0)
  ),
  CONSTRAINT reversal_has_reference CHECK (
    kind NOT IN ('REFUND', 'ROLLBACK') OR reference_external_transaction_id IS NOT NULL
  ),
  CONSTRAINT failure_has_code CHECK (
    status NOT IN ('REJECTED', 'FAILED') OR failure_code IS NOT NULL
  ),
  CONSTRAINT processed_has_result CHECK (
    status <> 'PROCESSED' OR (result_balance_minor IS NOT NULL AND result_wallet_version IS NOT NULL)
  )
);

-- A mesma operação não entra duas vezes pela chave nem pelo id externo,
-- mesmo trocando a chave.
CREATE UNIQUE INDEX wt_provider_external_uk ON wager_transactions (provider_id, external_transaction_id);
CREATE UNIQUE INDEX wt_provider_idemkey_uk ON wager_transactions (provider_id, idempotency_key);
-- Uma abertura por carteira.
CREATE UNIQUE INDEX wt_one_opening_per_wallet_uk ON wager_transactions (wallet_id) WHERE kind = 'OPENING';
-- No máximo uma reversão bem-sucedida por alvo (REFUND ou ROLLBACK).
CREATE UNIQUE INDEX wt_single_reversal_uk ON wager_transactions (resolved_reference_id)
  WHERE kind IN ('REFUND', 'ROLLBACK') AND status = 'PROCESSED';
CREATE INDEX wt_due_idx ON wager_transactions (next_attempt_at)
  WHERE status IN ('PENDING', 'PENDING_REFERENCE');
CREATE INDEX wt_waiting_idx ON wager_transactions (provider_id, reference_external_transaction_id)
  WHERE status = 'PENDING_REFERENCE';
CREATE INDEX wt_wallet_idx ON wager_transactions (wallet_id, created_at);

CREATE TABLE wallet_ledger_entries (
  seq                  bigint      GENERATED ALWAYS AS IDENTITY,
  id                   uuid        PRIMARY KEY,
  wallet_id            uuid        NOT NULL REFERENCES wallets (id),
  transaction_id       uuid        NOT NULL REFERENCES wager_transactions (id),
  direction            text        NOT NULL CHECK (direction IN ('DEBIT', 'CREDIT')),
  amount_minor         bigint      NOT NULL CHECK (amount_minor > 0),
  currency             char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  balance_before_minor bigint      NOT NULL CHECK (balance_before_minor >= 0),
  balance_after_minor  bigint      NOT NULL CHECK (balance_after_minor >= 0),
  created_at           timestamptz NOT NULL,
  CONSTRAINT ledger_wallet_tx_uk UNIQUE (wallet_id, transaction_id),
  CONSTRAINT ledger_arith CHECK (
    (direction = 'CREDIT' AND balance_after_minor = balance_before_minor + amount_minor) OR
    (direction = 'DEBIT' AND balance_after_minor = balance_before_minor - amount_minor)
  )
);
CREATE INDEX ledger_wallet_seq_idx ON wallet_ledger_entries (wallet_id, seq);

CREATE TABLE inbox_messages (
  consumer_name text        NOT NULL,
  message_id    text        NOT NULL,
  payload_hash  bytea       NOT NULL,
  received_at   timestamptz NOT NULL,
  completed_at  timestamptz NOT NULL,
  PRIMARY KEY (consumer_name, message_id)
);

CREATE TABLE outbox_events (
  id             uuid        PRIMARY KEY,
  aggregate_type text        NOT NULL,
  aggregate_id   uuid        NOT NULL,
  event_type     text        NOT NULL,
  event_version  integer     NOT NULL,
  ordering_key   text        NOT NULL,
  correlation_id text        NOT NULL,
  causation_id   text,
  payload        jsonb       NOT NULL,
  occurred_at    timestamptz NOT NULL,
  attempts       integer     NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL,
  lease_owner    text,
  lease_until    timestamptz,
  published_at   timestamptz,
  last_error     text
);
CREATE INDEX outbox_due_idx ON outbox_events (next_attempt_at) WHERE published_at IS NULL;
