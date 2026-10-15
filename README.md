# mini-base

A **lightweight, modular, open-source backend** — auth, database REST API, file storage, and realtime — in a single Go binary on top of PostgreSQL.

Think Firebase / Supabase, but small enough for a $5 VPS or a Raspberry Pi, and you only turn on what you use.

> 🚧 Status: accounts and sign-in, an automatic REST API over your tables with per-user row level security, and an admin dashboard. File storage is next. See the [roadmap](docs/architecture.md#10-roadmap).

## Why

| | Supabase (self-hosted) | mini-base |
|---|---|---|
| Containers | ~10 | **2** (minibase + Postgres) |
| Runtimes | Elixir, Go, Node, Haskell, Deno… | **Go** |
| Pick features | All or nothing | `MINIBASE_MODULES=admin,auth,database` |

## Modules

| Module | Status | What it gives you |
|--------|--------|-------------------|
| `example` | ✅ | Reference module and template for contributors |
| `auth` | ✅ | Email/password sign-up and sign-in, rotating refresh tokens, sessions (OAuth and magic links planned) |
| `database` | ✅ | Auto REST API over your Postgres tables, secured by row-level security |
| `storage` | planned | File buckets on local disk or any S3-compatible store |
| `realtime` | planned | WebSocket subscriptions to table changes, broadcast, presence |
| `hooks` | planned | Signed webhooks on events, with retries |
| `admin` | ✅ | Web dashboard at `/_/`: table designer, RLS policy templates, CSV import/export, SQL editor with snippets, users, API docs and keys, audit log, admin management |

## Quick start (Docker)

Full walkthrough: [docs/getting-started.md](docs/getting-started.md).

```bash
# 1. One-time: resolve dependencies (writes go.sum). No Go install? use: make tidy-docker
go mod tidy

# 2. Configure: generate the secrets (see docs/getting-started.md, step 4)
cp deploy/compose/.env.example deploy/compose/.env

# 3. Start minibase + bundled Postgres
docker compose -f deploy/compose/docker-compose.yml up -d --build

# 4. Check it, then follow the sign-up + REST walkthrough in docs/getting-started.md, step 6
curl localhost:8090/health/ready
```

### Bundled or your own Postgres

`COMPOSE_PROFILES` in `.env` decides which containers start next to minibase:

| `.env` | Result |
|--------|--------|
| `COMPOSE_PROFILES=db` (default) | minibase + bundled PostgreSQL 16 |
| `COMPOSE_PROFILES=` and `MINIBASE_DATABASE_URL=postgres://…` | minibase only, using your Postgres (Neon, RDS, a LAN server…) |
| `COMPOSE_PROFILES=db,dev` | + Mailpit for auth emails |

Features inside minibase are chosen separately with `MINIBASE_MODULES`.

## Develop

Requires Go 1.24+ and Docker.

```bash
make test               # unit + architecture tests, no database needed
make test-integration   # starts a throwaway Postgres and runs integration tests
make run                # runs the server locally with the example module
make help               # everything else
```

Configuration is environment variables prefixed `MINIBASE_` — see [`deploy/compose/.env.example`](deploy/compose/.env.example).

## Docs

- [Getting started](docs/getting-started.md) — step-by-step setup, everyday commands, troubleshooting
- [Architecture](docs/architecture.md)
- [Coding standards](docs/coding-standards.md)
- [Contributing](CONTRIBUTING.md)
- [Architecture decision records](docs/adr/)

## License

TBD (Apache-2.0 proposed).
