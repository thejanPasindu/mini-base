# ADR-0006: Audit log in Postgres, history derived from it, and a kernel record of settings

- **Status:** Accepted
- **Date:** 2026-10-15

## Context
The dashboard gained tools that change schemas, data and accounts: a table designer, policy templates, CSV import, and admin management. Teams need to see who did what. The SQL editor needs a run history, and an API and keys page needs the anon key and the configuration in effect, which belongs to other modules.

## Decision
1. **One audit table, `admin.audit_log`,** written by the admin services (not the HTTP layer) after each successful action, plus failed sign-ins and failed SQL runs. The acting admin travels in the request context (`service.WithActor`); command-line changes record as `cli`. A failure to write the log is logged, not returned: the action has already happened.
2. **SQL history is read from the audit log** (`action = 'sql.run'`, partial index per account), not kept in a second table.
3. **Retention, not growth:** rows older than `MINIBASE_ADMIN_AUDIT_RETENTION` (90 days by default, `0` = forever) are deleted hourly.
4. **The kernel records every setting it or a module reads** (`kernel.Settings`), masking secrets by name. The admin module shows that record; modules never report their settings, and the anon key reaches the dashboard without giving modules a new dependency.
5. **Designer DDL is built in the domain layer** from allow-lists and quoted identifiers, and every designer endpoint has a `?preview=1` mode returning the SQL.

## Alternatives considered
- **Log to stdout only** — no setup, but not searchable from the dashboard and lost with container logs.
- **A tamper-proof log** (append-only role, hash chain, external sink) — admins can run arbitrary SQL as the owner, so in-database protection would be theatre. An external sink can come with the `hooks` module.
- **Recording in HTTP middleware** — generic, but it would log requests rather than outcomes, and miss CLI changes.
- **Passing `Config.Keys` to every module** — simplest for the key page, but hands the service key to modules that don't need it.

## Consequences
- The audit log is a record for honest operators and incident review, not a security boundary against a malicious admin. The docs say so.
- New settings appear on the Settings page automatically; secret detection relies on naming (`*_SECRET`, `*_PASSWORD`, `*_KEY`, `*_URL`), which new settings must follow.
- History entries disappear with retention, like the rest of the log.
