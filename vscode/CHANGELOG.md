# Changelog

## 0.4.0

- Java-based Flyway migrations (`V2__Add_index.java`): the SQL in the source is analyzed, findings are shown on the Java line, and the quick fix inserts a `// dbguard:ignore` comment. Requires dbguard 0.4.0.

## 0.3.0

First release.

- Inline diagnostics for Flyway and Liquibase migrations (PostgreSQL and MySQL) on open, save and while typing.
- Quick fix that acknowledges a risk with an auditable `dbguard:ignore` comment.
- Status bar summary, schema changelog command, and settings for engine, sizes, severity and file globs.
