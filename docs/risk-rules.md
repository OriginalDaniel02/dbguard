# Migration risk rules (PostgreSQL)

| Operation | Lock behavior | Risk | Safer alternative |
|---|---|---|---|
| ADD COLUMN, no default or constant default (PG 11+) | Metadata-only | Safe | None needed |
| ADD COLUMN, non-constant default (e.g. now()) | Full table rewrite | High on large tables | Add nullable, backfill in batches, then set default |
| ALTER COLUMN TYPE | Full rewrite, ACCESS EXCLUSIVE | High | New column, backfill, swap, drop old; or confirm binary-compatible |
| CREATE INDEX | SHARE lock, blocks writes | High on large/hot tables | CREATE INDEX CONCURRENTLY |
| ADD UNIQUE / ADD PRIMARY KEY via ALTER TABLE ADD CONSTRAINT | ACCESS EXCLUSIVE while index builds | High | Build index CONCURRENTLY, then ADD CONSTRAINT ... USING INDEX |
| ADD NOT NULL | Full scan under ACCESS EXCLUSIVE (unless matching CHECK exists, PG 12+) | Medium-high on large tables | NOT VALID CHECK, validate separately, then SET NOT NULL |
| ADD FOREIGN KEY | Locks both tables, scans referencing table | Medium-high | Add NOT VALID, then VALIDATE CONSTRAINT separately |
| DROP COLUMN | Metadata-only | Low (breaks running code reading the column) | Deploy code that stops reading the column first |

Duration is reported as a range (operation type x row count x timing stats), never a promise.
