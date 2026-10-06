-- add an index for the dashboard

CREATE INDEX idx_transactions_amount ON transactions (amount);
