-- Test migration for the DB Guard Action (MySQL)
ALTER TABLE orders ADD CONSTRAINT chk_total CHECK (total >= 0);
ALTER TABLE orders ADD COLUMN note VARCHAR(10);
