# ADR-0003: Realtime change capture via triggers + LISTEN/NOTIFY (v1)

- **Status:** Proposed
- **Date:** 2026-10-14

## Context
Realtime needs table change events. Options are trigger-based `pg_notify` or logical replication (WAL decoding). Subscribers must only receive rows RLS allows them to see.

## Decision
v1 installs an `AFTER INSERT/UPDATE/DELETE` trigger on realtime-enabled tables that sends a small payload (`table`, `op`, primary key) via `pg_notify`. The realtime module re-reads the row **as each subscriber's role** before sending, so RLS is respected. Capture is behind a `ChangeSource` port so logical replication can replace it later.

## Alternatives considered
- **Logical replication (`pgoutput` / `wal2json`)** — higher throughput, captures old values; but needs `wal_level=logical`, replication slots (which can fill the disk if the consumer stops), and isn't available on every managed Postgres.
- **Polling** — simplest, but latency and wasted queries.

## Consequences
- ✅ Zero Postgres configuration; works everywhere.
- ✅ RLS-correct delivery by construction.
- ⚠️ Per-event re-read costs one query per subscriber group; acceptable at v1 scale, mitigated by grouping subscribers by role+claims hash.
- ⚠️ NOTIFY payload limit (8 KB) — we only send keys, so not an issue.
- ⚠️ Events during a listener disconnect are lost; clients resync on reconnect (documented behaviour).
