# ADR-0004: Opaque API keys, stdlib HS256 access tokens, and refusing tables without RLS

- **Status:** Accepted
- **Date:** 2026-10-14

## Context
Phase 1 needs a credential model shared by every module, and a REST API over user tables. The classic failure of Supabase-style backends is a table created without row-level security, which then becomes readable by anyone holding the public anon key.

## Decision
1. **API keys are opaque random strings** (`MINIBASE_ANON_KEY`, `MINIBASE_SERVICE_KEY`) checked by the kernel (`platform/authn`) in constant time. They are not JWTs.
2. **Access tokens are HS256 JWTs** produced by a ~100-line standard-library implementation that accepts no other algorithm. Refresh tokens are opaque, hashed, single-use, with reuse detection.
3. **Request roles** `minibase_anon` and `minibase_authenticated` are created by the kernel. The service key runs as the connection user rather than a `BYPASSRLS` role, which would need superuser rights that managed Postgres providers don't grant.
4. **Secure by default:** the `database` module refuses anon and signed-in requests to tables without RLS, to views without `security_invoker`, and to materialized views and foreign tables. The error message says how to fix it. An explicit opt-out exists.

## Alternatives considered
- **Keys as long-lived JWTs (Supabase)** — self-describing, but key rotation means re-signing, and a leaked JWT secret also forges keys. Opaque keys are simpler and independent of the token secret.
- **`golang-jwt/jwt`** — mature, but supports many algorithms we would have to lock down, and adds a dependency for ~100 lines of code.
- **Trust developers to enable RLS** — matches Supabase, but the failure mode is silent data exposure. A loud 403 during development is cheaper.

## Consequences
- ✅ One credential path for all modules; a missing key or secret is a startup error, not a silent default.
- ✅ Forgetting RLS fails closed, with instructions.
- ⚠️ HS256 means anything that verifies tokens holds the signing secret. Fine for a single process; an asymmetric option (ES256) is needed before verification moves to other services.
- ⚠️ The kernel's roles migration needs `CREATEROLE` (or superuser) on the database user.
