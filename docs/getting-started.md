# Getting Started

Run mini-base on your machine, check it works, and learn the everyday commands.
Commands are for macOS/Linux Terminal and start from the repository root unless a step says otherwise.

**Contents**

1. [Prerequisites](#1-prerequisites)
2. [Install dependencies](#2-install-dependencies)
3. [Run the unit tests](#3-run-the-unit-tests)
4. [Configure](#4-configure)
5. [Start mini-base and Postgres](#5-start-mini-base-and-postgres)
6. [Try the API](#6-try-the-api)
7. [Look inside Postgres](#7-look-inside-postgres)
8. [Logs, stop, reset](#8-logs-stop-reset)
9. [Integration tests](#9-integration-tests)
10. [Run the server without Docker (faster while coding)](#10-run-the-server-without-docker)
11. [Use your own Postgres instead of the bundled one](#11-use-your-own-postgres)
12. [Troubleshooting](#12-troubleshooting)

---

## How the pieces fit

There are two independent switches, both in `deploy/compose/.env`:

| Setting | What it controls | Example |
|---|---|---|
| `MINIBASE_MODULES` | Features **inside** the minibase program | `example` (later `auth,database,storage`) |
| `COMPOSE_PROFILES` | Extra **containers** started next to it | `db` = bundled PostgreSQL (default), `dev` = Mailpit, `s3` = MinIO |

PostgreSQL is not a mini-base module: modules are code inside the binary, while Postgres is a separate server that can run next to minibase or anywhere else. That is why it is switched with a Compose profile, and why it is never built into the minibase image.

---

## 1. Prerequisites

```bash
docker version            # Docker Desktop (or Docker Engine) must be running
docker compose version    # v2.20 or newer
go version                # 1.24 or newer — optional, see below
```

- **Docker** is required.
- **Go** is needed to run tests and the server outside Docker. Install with `brew install go` on macOS. Without Go you can still use the `*-docker` make targets and Docker Compose.
- **make** ships with macOS (Xcode Command Line Tools) and most Linux distributions.

✅ All three commands print a version.

## 2. Install dependencies

First time only, and again whenever `go.mod` changes:

```bash
go mod tidy               # without Go installed: make tidy-docker
```

✅ `go.sum` exists in the repository root. Commit it.

## 3. Run the unit tests

```bash
make test
```

Runs unit tests and the architecture tests (which fail if a module imports another module's code). No database needed.

✅ Every line ends in `ok`.

## 4. Configure

```bash
cd deploy/compose
cp .env.example .env
# put a random password in .env
sed -i '' "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=$(openssl rand -hex 16)/" .env   # macOS
# sed -i "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=$(openssl rand -hex 16)/" .env    # Linux
grep -E "COMPOSE_PROFILES|POSTGRES_PASSWORD|MINIBASE_MODULES" .env
```

✅ Shows `COMPOSE_PROFILES=db`, a long random password, and `MINIBASE_MODULES=example`.

> If Postgres already runs on your machine, port 5432 is taken. Set `POSTGRES_PORT=5433` in `.env`.
> `.env` holds secrets and is git-ignored. Never commit it.

## 5. Start mini-base and Postgres

Still inside `deploy/compose`:

```bash
docker compose up -d --build
docker compose ps
```

✅ After about 15 seconds both `postgres` and `minibase` show `healthy`.

```bash
curl localhost:8090/health/ready
```

✅ `{"status":"ok","checks":{"database":"ok"}}`

## 6. Try the API

```bash
curl -X POST localhost:8090/example/v1/greetings \
  -H 'Content-Type: application/json' -d '{"message":"hello"}'
curl localhost:8090/example/v1/greetings
curl localhost:8090/
```

✅ The second command returns your `"hello"` inside `"data"`. The last shows the version and enabled modules.

## 7. Look inside Postgres

From `deploy/compose`:

```bash
docker compose exec postgres psql -U postgres -d minibase -c '\dt example.*'
```

✅ Lists `example.greetings`.

For a GUI client (TablePlus, DBeaver, pgAdmin), connect to:

| Field | Value |
|---|---|
| Host | `localhost` |
| Port | `5432` (or your `POSTGRES_PORT`) |
| User | `postgres` |
| Database | `minibase` |
| Password | `POSTGRES_PASSWORD` from `.env` |

The port is bound to `127.0.0.1` only, so it is not reachable from other machines.

## 8. Logs, stop, reset

From `deploy/compose`:

```bash
docker compose logs -f minibase   # follow logs, Ctrl+C to exit
docker compose down               # stop; data is kept
docker compose up -d              # start again
docker compose up -d --build      # rebuild after code changes
```

Full reset:

```bash
docker compose down -v
```

> ⚠️ `-v` **permanently deletes the database volume and all data in it.** Use it only for a clean start.

## 9. Integration tests

From the repository root:

```bash
make test-integration   # starts a throwaway Postgres on 127.0.0.1:55432 and runs tests against it
make db-down            # removes the throwaway Postgres
```

These use their own container, separate from the Compose stack, so your development data is never touched.

✅ Every line ends in `ok`.

## 10. Run the server without Docker

Faster feedback while writing Go code: Postgres runs in Docker, the server runs directly on your machine.

```bash
make run    # builds ./bin/minibase, starts the throwaway Postgres, serves on :8090 with the example module
```

Stop with `Ctrl+C`. If the Compose stack is also running, stop it first (`docker compose down` in `deploy/compose`), because both use port 8090.

Useful binary commands:

```bash
./bin/minibase modules          # modules compiled into this build
./bin/minibase version
./bin/minibase migrate          # apply migrations for MINIBASE_MODULES and exit
./bin/minibase serve --modules=example
```

## 11. Use your own Postgres

Use this for an existing server, Neon, Supabase DB, RDS and so on (PostgreSQL 16+). In `deploy/compose/.env`:

```bash
COMPOSE_PROFILES=
MINIBASE_DATABASE_URL=postgres://user:password@db.example.com:5432/minibase?sslmode=require
```

Then:

```bash
docker compose up -d --build
docker compose ps        # ✅ only minibase is listed
```

mini-base creates its own schemas (`minibase`, plus one per enabled module) on first start. The database user needs permission to create schemas in that database.

### Optional extras

| `.env` | Adds |
|---|---|
| `COMPOSE_PROFILES=db,dev` | Mailpit at http://localhost:8025, which catches emails sent by the auth module |
| `COMPOSE_PROFILES=db,s3` | MinIO console at http://localhost:9001, for the S3 storage driver |

## 12. Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `port is already allocated` for 5432 | Another Postgres is running. Set `POSTGRES_PORT=5433` in `.env`. |
| `port is already allocated` for 8090 | Something else uses 8090, often `make run`. Stop it or set `MINIBASE_PORT=8091`. |
| `password authentication failed` after changing `POSTGRES_PASSWORD` | Postgres sets the password only when the volume is first created. Change it back, or reset with `docker compose down -v` (deletes data). |
| `"COPY go.sum": not found` during build | Run step 2 (`go mod tidy`) first. |
| `depends_on ... required` is rejected | Docker Compose is older than v2.20. Update Docker Desktop. |
| Postgres container exits immediately | `POSTGRES_PASSWORD` is empty in `.env`. |
| `/health/ready` returns `unavailable` | The database is unreachable. Check `docker compose logs minibase` and `MINIBASE_DATABASE_URL`. |
| `unknown module "x"` at startup | A typo in `MINIBASE_MODULES`. Run `./bin/minibase modules` (or check the error text) to see available names. |
| `migration ... was modified after being applied` | An already-applied SQL migration file was edited. Revert the edit and add a new migration file instead. |
