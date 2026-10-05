-- Test migration for the DB Guard Action (not a real schema).
CREATE INDEX idx_transactions_amount ON transactions (amount);
ALTER TABLE transactions ADD CONSTRAINT fk_acct FOREIGN KEY (account_id) REFERENCES accounts (id);
