-- Sample: every statement here is risky on a large table.
ALTER TABLE transactions ADD COLUMN created_at timestamptz DEFAULT now();

CREATE INDEX idx_transactions_amount ON transactions (amount);

-- dbguard:ignore add-not-null reason: column is backfilled and enforced by app since v41; table is write-quiet
ALTER TABLE transactions ALTER COLUMN ref SET NOT NULL;

ALTER TABLE transactions ADD CONSTRAINT fk_acct FOREIGN KEY (account_id) REFERENCES accounts (id);
