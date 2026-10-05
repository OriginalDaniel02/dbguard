# Acceptance criteria (Phase 1)
- [ ] dbguard check identifies every operation in the rules table with the right risk level
- [ ] High-risk operations include a time-range estimate from the real row count
- [ ] CI posts a PR comment: table name, estimated duration, safer alternative
- [ ] Reads native Flyway files with no separate config format
- [ ] All DB access is read-only
- [ ] False positives can be overridden with a documented, auditable acknowledgment
- [ ] Connection strings are never logged or echoed in PR comments

Deferred: Liquibase, MySQL, drift detector (Phase 2); VS Code extension (Phase 3).
