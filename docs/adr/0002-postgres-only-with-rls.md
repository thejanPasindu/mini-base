# ADR-0002: PostgreSQL only, with row-level security as the authorisation layer

- **Status:** Proposed
- **Date:** 2026-10-14

## Context
We need one authorisation model for the REST API, storage, and realtime that is hard to bypass. PocketBase uses SQLite with app-level rules; Supabase uses Postgres RLS.

## Decision
v1 supports **PostgreSQL 16+ only**. Every request runs in a transaction with `SET LOCAL ROLE` and the JWT claims in `request.jwt.claims`; developers write standard RLS policies using `auth.uid()`. Storage objects and realtime row re-reads use the same mechanism.

## Alternatives considered
- **SQLite (PocketBase-style)** — lighter (no DB container), but no RLS, weaker concurrency, and we'd need our own rules engine. Possible v2 "lite mode" behind a `Database` port with app-level policies.
- **Database-agnostic ORM** — lowest-common-denominator features; loses RLS, LISTEN/NOTIFY, JSONB.

## Consequences
- ✅ Security enforced in the database: a bug in a handler cannot leak rows the policy forbids.
- ✅ Works with managed Postgres (Neon, RDS, Supabase DB) — no extensions required for v1.
- ⚠️ Postgres container adds ~30–60 MB RAM.
- ⚠️ Query builder must always run inside the role-scoped transaction; enforced via `platform/pgtx.WithTx` being the only way repositories get a connection.
