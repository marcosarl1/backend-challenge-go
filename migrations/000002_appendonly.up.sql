CREATE FUNCTION forbid_ledger_mutation() RETURNS trigger
  LANGUAGE plpgsql AS
$$
BEGIN
  RAISE EXCEPTION 'wallet_ledger_entries is append-only'
    USING ERRCODE = 'integrity_constraint_violation';
  RETURN NULL;
END
$$;

CREATE TRIGGER ledger_no_update
  BEFORE UPDATE ON wallet_ledger_entries
  FOR EACH ROW EXECUTE FUNCTION forbid_ledger_mutation();

CREATE TRIGGER ledger_no_delete
  BEFORE DELETE ON wallet_ledger_entries
  FOR EACH ROW EXECUTE FUNCTION forbid_ledger_mutation();

CREATE TRIGGER ledger_no_truncate
  BEFORE TRUNCATE ON wallet_ledger_entries
  FOR EACH STATEMENT EXECUTE FUNCTION forbid_ledger_mutation();

-- Transação em estado final não muda mais, nem pelo dono.
CREATE FUNCTION forbid_terminal_tx_change() RETURNS trigger
  LANGUAGE plpgsql AS
$$
BEGIN
  IF OLD.status IN ('PROCESSED', 'REJECTED', 'FAILED') THEN
    RAISE EXCEPTION 'terminal wager_transactions are immutable'
      USING ERRCODE = 'integrity_constraint_violation';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER wager_tx_terminal_immutable
  BEFORE UPDATE ON wager_transactions
  FOR EACH ROW EXECUTE FUNCTION forbid_terminal_tx_change();

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app') THEN
    CREATE ROLE app LOGIN PASSWORD 'app';
  END IF;
END
$$;

GRANT USAGE ON SCHEMA public TO app;
GRANT SELECT, INSERT ON wallet_ledger_entries TO app;
GRANT SELECT, INSERT, UPDATE ON wallets TO app;
GRANT SELECT, INSERT, UPDATE ON wager_transactions TO app;
GRANT SELECT, INSERT ON inbox_messages TO app;
GRANT SELECT, INSERT, UPDATE ON outbox_events TO app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO app;
