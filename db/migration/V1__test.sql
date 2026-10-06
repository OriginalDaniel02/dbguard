-- Test migration for the root action (not a real schema)
CREATE INDEX idx_transactions_amount ON transactions (amount);
ALTER TABLE transactions ADD COLUMN note text;
