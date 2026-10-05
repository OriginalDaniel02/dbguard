--liquibase formatted sql

--changeset dev:1
CREATE TABLE widgets (id bigint, name text);
CREATE INDEX idx_widgets_name ON widgets (name);
--rollback DROP TABLE widgets;

--changeset dev:2
ALTER TABLE transactions ADD COLUMN created_at timestamptz DEFAULT now();

--changeset dev:3
ALTER TABLE transactions ADD COLUMN score float8 DEFAULT random();

--changeset dev:4
CREATE INDEX idx_tx_amount ON transactions (amount);

--changeset dev:7 runInTransaction:false
CREATE INDEX CONCURRENTLY idx_tx_ref ON transactions (ref);
