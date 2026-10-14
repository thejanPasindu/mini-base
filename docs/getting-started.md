# Getting Started

Run mini-base on your machine, check it works, and learn the everyday commands.
Commands are for macOS/Linux Terminal and start from the repository root unless a step says otherwise.

**Contents**

1. [Prerequisites](#1-prerequisites)
2. [Install dependencies](#2-install-dependencies)
3. [Run the unit tests](#3-run-the-unit-tests)
4. [Configure](#4-configure)
5. [Start mini-base and Postgres](#5-start-mini-base-and-postgres)
6. [Try the API](#6-try-the-api), then [open the admin dashboard](#6b-open-the-admin-dashboard)
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
| `MINIBASE_MODULES` | Features **inside** the minibase program | `admin,auth,database` (available: `admin`, `auth`, `database`, `example`) |
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
# replace every "change-me" with its own random secret
for k in POSTGRES_PASSWORD MINIBASE_ANON_KEY MINIBASE_SERVICE_KEY MINIBASE_AUTH_JWT_SECRET; do
  sed -i '' "s/^$k=.*/$k=$(openssl rand -hex 32)/" .env      # macOS
  # sed -i "s/^$k=.*/$k=$(openssl rand -hex 32)/" .env       # Linux
done
grep -E "^(COMPOSE_PROFILES|MINIBASE_MODULES|POSTGRES_PASSWORD|MINIBASE_.*KEY|MINIBASE_AUTH_JWT_SECRET)=" .env
```

✅ Shows `COMPOSE_PROFILES=db`, `MINIBASE_MODULES=admin,auth,database`, and four different 64-character secrets. No `change-me` is left.

> ⚠️ **Already started the stack before?** Keep your old `POSTGRES_PASSWORD`. The bundled Postgres sets its password only when its data volume is first created; a new value in `.env` makes mini-base fail with `password authentication failed`. Copy the old value back, or see Troubleshooting.

| Secret | What it is |
|---|---|
| `MINIBASE_ANON_KEY` | Public key for your apps. Safe in a browser or mobile app, because RLS policies limit what it can do. |
| `MINIBASE_SERVICE_KEY` | Full-access key that bypasses RLS. Only for your own servers; never ship it in an app. |
| `MINIBASE_AUTH_JWT_SECRET` | Signs access tokens. Changing it signs everyone out. |

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

✅ `"status":"ok"`, with every check `ok`.

## 6. Try the API

This walkthrough creates a table, signs up two users, and shows that each user only sees their own rows.

**6.1 Load the keys into your shell** (still inside `deploy/compose`)

```bash
set -a; source .env; set +a
API=http://localhost:8090
```

**6.2 Create a table with row level security**

```bash
docker compose exec -T postgres psql -U postgres -d minibase <<'SQL'
CREATE TABLE public.todos (
  id      bigint  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id uuid    NOT NULL DEFAULT auth.uid(),   -- the signed-in user
  title   text    NOT NULL,
  done    boolean NOT NULL DEFAULT false
);
ALTER TABLE public.todos ENABLE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON public.todos FOR ALL TO minibase_authenticated
  USING (user_id = auth.uid()) WITH CHECK (user_id = auth.uid());
SQL
```

✅ Prints `CREATE TABLE`, `ALTER TABLE`, `CREATE POLICY`. There's no need to restart mini-base: it notices new tables on first use.

**6.3 Sign up two users**

```bash
ALICE=$(curl -s -X POST $API/auth/v1/signup -H "apikey: $MINIBASE_ANON_KEY" \
  -H 'Content-Type: application/json' -d '{"email":"alice@example.com","password":"correct horse battery"}' \
  | sed -E 's/.*"access_token":"([^"]+)".*/\1/')
BOB=$(curl -s -X POST $API/auth/v1/signup -H "apikey: $MINIBASE_ANON_KEY" \
  -H 'Content-Type: application/json' -d '{"email":"bob@example.com","password":"correct horse battery"}' \
  | sed -E 's/.*"access_token":"([^"]+)".*/\1/')
echo "${ALICE:0:20}…  ${BOB:0:20}…"
```

✅ Two token prefixes starting with `eyJ`. (If you run this twice, sign-up returns `auth.email_taken`. Sign in instead with `POST $API/auth/v1/token?grant_type=password` and the same body.)

**6.4 Alice adds a todo; Bob can't see it**

```bash
curl -s -X POST "$API/rest/v1/todos" -H "apikey: $MINIBASE_ANON_KEY" -H "Authorization: Bearer $ALICE" \
  -H 'Content-Type: application/json' -H 'Prefer: return=representation' -d '{"title":"buy milk"}'
echo
curl -s "$API/rest/v1/todos?select=id,title,done" -H "apikey: $MINIBASE_ANON_KEY" -H "Authorization: Bearer $ALICE"; echo
curl -s "$API/rest/v1/todos" -H "apikey: $MINIBASE_ANON_KEY" -H "Authorization: Bearer $BOB"; echo
```

✅ The first command returns the new row with Alice's `user_id`. Alice's list shows `buy milk`. Bob's list is `[]`.

**6.5 Filter, update, delete**

```bash
AUTH=(-H "apikey: $MINIBASE_ANON_KEY" -H "Authorization: Bearer $ALICE")
curl -s "$API/rest/v1/todos?done=eq.false&order=id.desc&limit=10" "${AUTH[@]}"; echo
curl -s -X PATCH "$API/rest/v1/todos?title=eq.buy%20milk" "${AUTH[@]}" \
  -H 'Content-Type: application/json' -H 'Prefer: return=representation' -d '{"done":true}'; echo
curl -s -o /dev/null -w "%{http_code}\n" -X DELETE "$API/rest/v1/todos?done=is.true" "${AUTH[@]}"
```

✅ The row comes back with `"done":true`, then the delete prints `204`.

**6.6 Who am I; sign out**

```bash
curl -s $API/auth/v1/user "${AUTH[@]}"; echo
curl -s -o /dev/null -w "%{http_code}\n" -X POST $API/auth/v1/logout "${AUTH[@]}"
```

✅ Alice's id and email, then `204`.

**Query cheat-sheet**

| Want | Query string |
|---|---|
| Some columns | `select=id,title` |
| Equals / not equals | `done=eq.false`, `done=neq.true` |
| Compare | `id=gt.10`, `due=lte.2026-12-31` |
| Pattern (`*` = any text) | `title=ilike.*milk*` |
| One of | `id=in.(1,2,3)` |
| Null / boolean | `due=is.null`, `done=is.true` |
| Negate any filter | `title=not.ilike.*milk*` |
| Sort | `order=due.desc.nullslast,id` |
| Page | `limit=20&offset=40` |

PATCH and DELETE need at least one filter, so a whole table is never changed by accident. Tables without RLS are refused with a message explaining how to enable it.

The `example` module still works if you add it to `MINIBASE_MODULES` (`auth,database,example`).

## 6b. Open the admin dashboard

The dashboard runs inside mini-base when `admin` is in `MINIBASE_MODULES`. Its admins are separate from your app's users.

**Create your admin account** (from `deploy/compose`):

```bash
docker compose exec minibase /minibase admin create --email you@example.com
```

✅ Prints `Admin you@example.com created.` and a generated password. It is shown only once, so save it in your password manager.

Open **http://localhost:8090/_/** and sign in.

| Page | What you can do |
|---|---|
| Overview | Version, modules, database size, and which tables are **not** protected by row level security |
| Tables | Browse, sort and page rows; add, edit and delete rows (tables with a primary key); see columns; turn RLS on or off; read policies |
| SQL editor | Run any SQL as the database owner, e.g. create tables and policies. Several statements at once; `Ctrl`/`⌘` + `Enter` runs them |
| Users | Search app users, add one, sign a user out everywhere, delete a user (needs the `auth` module) |

Tables without row level security are marked with an amber striped square, and their page shows a **Turn on row level security** button.

Other admin commands:

```bash
docker compose exec minibase /minibase admin list
docker compose exec minibase /minibase admin reset-password --email you@example.com
docker compose exec minibase /minibase admin delete --email someone@example.com
```

> The SQL editor bypasses row level security, so admin accounts are powerful. Admin passwords need at least 12 characters. When you expose the dashboard over HTTPS, set `MINIBASE_ADMIN_SECURE_COOKIE=true`. To drop the dashboard entirely, remove `admin` from `MINIBASE_MODULES`: `/_/` then returns 404.

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

To run `auth` and `database` this way, give the binary the same secrets as step 4:

```bash
make build db-up
set -a; source deploy/compose/.env; set +a        # keys and JWT secret
MINIBASE_DATABASE_URL="postgres://postgres:postgres@127.0.0.1:55432/postgres?sslmode=disable" \
  ./bin/minibase serve --modules=auth,database
```

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
| Dashboard says "Sign in" but you have no account | Create one: `docker compose exec minibase /minibase admin create --email you@example.com` |
| Dashboard sign-in loops back to the sign-in page | You set `MINIBASE_ADMIN_SECURE_COOKIE=true` but opened the dashboard over plain `http://`. Use HTTPS, or set it back to `false` for local use. |
| `Too many attempts` on the dashboard sign-in | Login is limited to 10 attempts per minute per IP. Wait a minute. |
| `MINIBASE_ANON_KEY and MINIBASE_SERVICE_KEY must be set` at startup | Step 4 was skipped or `.env` still says `change-me`. Generate the keys and restart. |
| `MINIBASE_AUTH_JWT_SECRET must be at least 32 characters` | Same: generate it with `openssl rand -hex 32`. |
| `permission denied to create role` at startup | The database user needs `CREATEROLE` (or superuser): mini-base creates the `minibase_anon` and `minibase_authenticated` roles. |
| `401 auth.missing_api_key` | Every `/auth/v1` and `/rest/v1` request needs the `apikey` header. |
| `401 auth.invalid_token` | The access token expired (15 min) or was signed with an old JWT secret. Refresh it with `grant_type=refresh_token`, or sign in again. |
| `403 database.rls_disabled` | The table has no row level security. Run `ALTER TABLE … ENABLE ROW LEVEL SECURITY;` and add policies. Or use the service key from a trusted server. |
| `403 database.permission_denied` | A policy blocked the write (`WITH CHECK`), or the table was created by a different database user, so the request roles got no grants. Fix with: `GRANT SELECT, INSERT, UPDATE, DELETE ON public.<table> TO minibase_anon, minibase_authenticated;` |
| `404 database.table_not_found` | The table isn't in `public` (or `MINIBASE_DATABASE_SCHEMAS`), or there's a typo in the name. |
| Reads return `[]` though rows exist | RLS is working: no policy lets this caller see them. Anonymous callers need their own policy (`TO minibase_anon`). |
| `port is already allocated` for 5432 | Another Postgres is running. Set `POSTGRES_PORT=5433` in `.env`. |
| `port is already allocated` for 8090 | Something else uses 8090, often `make run`. Stop it or set `MINIBASE_PORT=8091`. |
| `database rejected the credentials` / `password authentication failed` | `POSTGRES_PASSWORD` in `.env` differs from the password the data volume was created with. Keep your data and update Postgres to the `.env` value (in `deploy/compose`): `set -a; source .env; set +a; docker compose exec -T postgres psql -U postgres -c "ALTER USER postgres PASSWORD '$POSTGRES_PASSWORD';"` then `docker compose restart minibase`. Or start over with `docker compose down -v` (⚠️ deletes all data). |
| `"COPY go.sum": not found` during build | Run step 2 (`go mod tidy`) first. |
| `depends_on ... required` is rejected | Docker Compose is older than v2.20. Update Docker Desktop. |
| Postgres container exits immediately | `POSTGRES_PASSWORD` is empty in `.env`. |
| `/health/ready` returns `unavailable` | The database is unreachable. Check `docker compose logs minibase` and `MINIBASE_DATABASE_URL`. |
| `unknown module "x"` at startup | A typo in `MINIBASE_MODULES`. Run `./bin/minibase modules` (or check the error text) to see available names. |
| `migration ... was modified after being applied` | An already-applied SQL migration file was edited. Revert the edit and add a new migration file instead. |
