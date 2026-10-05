# Security Policy

DB Guard is run in CI with a database credential, so security reports are taken seriously.

## Reporting a vulnerability

Please **do not open a public issue**. Report privately using GitHub's
**Security → Report a vulnerability** on this repository, or email the maintainer
listed on the GitHub profile. Include the version, reproduction steps, and impact.
You can expect an acknowledgement within a few days.

## Scope and design guarantees

The following are intended guarantees; a way to violate any of them is a vulnerability:

- DB Guard never writes to a database (sessions are read-only, queries run in `READ ONLY` transactions).
- Connection strings and passwords are never logged, echoed in errors, or posted in PR comments.
- Table contents are never read; only catalog metadata and planner row estimates are.
- The release binaries are published with a `checksums.txt`, which the GitHub Action verifies.

## Hardening advice for users

- Use a dedicated role that can only `CONNECT`; store its connection string as a CI secret and rotate it.
- Prefer pointing DB Guard at a replica or snapshot rather than the primary.
- Pin the Action to a release tag (or commit SHA) and set the `version` input.
