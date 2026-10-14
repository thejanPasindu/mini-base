# Coding Standards

These rules apply to every contribution. When code and this document disagree, fix one of them in the same PR.

## 1. Principles

- **Clean Architecture / Hexagonal** — dependencies point inwards: `adapter → service → domain`. Domain code has no I/O and no framework imports.
- **SOLID**, applied pragmatically:
  - *Single responsibility*: one reason to change per package/type. Handlers translate HTTP, services hold use cases, repositories talk SQL.
  - *Open/closed*: new storage drivers, OAuth providers, change sources are **new adapters**, not edits to services.
  - *Liskov / Interface segregation*: small interfaces, defined **where they are consumed** (Go idiom), e.g. a service declares the 2 methods it needs from a repository.
  - *Dependency inversion*: services receive ports via constructors. No package-level globals, no `init()` side effects.
- **Explicit over implicit** — no reflection-based DI, no magic config. Wiring lives in each module's `module.go`.
- **YAGNI** — build the v1 scope in `architecture.md`; anything else needs an issue first.

## 2. Design patterns in use

| Pattern | Where | Why |
|---------|-------|-----|
| Ports & Adapters | Every module | Swap Postgres/S3/SMTP without touching use cases; easy fakes in tests |
| Plugin / Registry | `kernel.Registry` + `Module` interface | Enable/disable modules at startup |
| Strategy | `BlobStore` drivers, OAuth providers, `ChangeSource` | Runtime-selectable implementations |
| Repository | `adapter/postgres` | Isolate SQL from business logic |
| Observer (pub/sub) | `kernel.EventBus` | Decoupled cross-module notifications |
| Chain of Responsibility | HTTP middleware | Composable request pipeline |
| Builder | REST query builder (`database` module) | Safe, validated SQL construction |
| Unit of Work | `platform/pgtx.WithTx(ctx, principal, fn)` | One tx per request with role + claims applied |
| Functional options | Constructors with optional config | Readable, backward-compatible APIs |

Patterns are tools, not goals: don't introduce one where a plain function works.

## 3. Go conventions

- Format: `gofumpt` + `goimports`. Lint: `golangci-lint` v2 with the repo's `.golangci.yml` (incl. `depguard`, `gosec`, `errorlint`, `revive`, `gocyclo` ≤ 15).
- Layering rules are also enforced by `test/arch`, which runs with the normal `go test ./...`.
- Naming: Effective Go. Packages are short, lowercase, singular (`auth`, `storage`), no `util`/`common`/`helpers`.
- `context.Context` is the first parameter of anything that does I/O; never stored in structs.
- Errors:
  - Wrap with context: `fmt.Errorf("create user: %w", err)`.
  - Compare with `errors.Is/As`, never string matching.
  - Domain errors carry a stable machine code (`auth.email_taken`) used in the API envelope.
  - Never `panic` for expected failures; `Recoverer` middleware is a last resort.
- Concurrency: every goroutine has an owner and a stop path (context cancel or channel close). Use `errgroup` for fan-out.
- Logging: `slog` only, structured keys (`slog.String("user_id", id)`). Never log secrets, tokens, passwords, or full request bodies.
- SQL: parameterised queries only. Identifiers from user input must be validated against the schema cache and quoted with `pgx.Identifier`.
- Functions: aim for < 50 lines; files < 500 lines. Split by responsibility, not by line count alone.
- Comments: every exported identifier has a doc comment starting with its name. Comment *why*, not *what*.

## 4. Testing

- New behaviour ships with tests. Bug fixes ship with a test that failed before the fix.
- Unit tests: table-driven, `t.Parallel()` where safe, fakes over mocks (hand-written fakes for ports).
- Integration tests use build tag `integration` and read `MINIBASE_TEST_DATABASE_URL`; they live in `test/integration` (or next to an adapter). `make test-integration` provides the database.
- Coverage target: ≥ 80 % for `domain` and `service` packages; adapters covered by integration tests.
- Security-sensitive parsers (query params, JWT, multipart) get fuzz tests.

## 5. API design

- Resource-oriented JSON, `snake_case` fields, RFC 3339 UTC timestamps, UUIDv7 IDs.
- One error envelope everywhere: `{ "error": { "code", "message", "request_id" } }`.
- Every endpoint is described in `api/<module>.yaml` (OpenAPI 3.1) **before** implementation.
- Breaking change = new version prefix. Additive changes are fine within a version.

## 6. Database & migrations

- Each module owns one schema and its migrations under `internal/modules/<name>/migrations`.
- Migrations are forward-only: files `NNNN_description.sql`, applied once each in a transaction, checksummed. The server refuses to start if an applied migration file was edited.
- Statements that cannot run inside a transaction (e.g. `CREATE INDEX CONCURRENTLY`) are not supported yet.
- Never edit a released migration — add a new one.
- Every table that holds user data has RLS **enabled** by default.

## 7. Git workflow

- Trunk-based: short-lived branches off `main`, PRs required, squash merge.
- Branch names: `feat/auth-refresh-rotation`, `fix/storage-range-header`.
- Commit messages: [Conventional Commits](https://www.conventionalcommits.org/) — `feat(auth): rotate refresh tokens on use`. Drives the changelog and semver releases.
- PR checklist: tests, docs updated, OpenAPI updated, no new dependency without justification, ADR if an architectural decision changed.

## 8. Architecture Decision Records

Significant decisions go in `docs/adr/NNNN-title.md` using the template in `docs/adr/0000-template.md`. ADRs are immutable once accepted; supersede them with a new ADR.
