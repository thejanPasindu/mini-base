# Contributing to mini-base

Thanks for helping! Please read [docs/architecture.md](docs/architecture.md) and [docs/coding-standards.md](docs/coding-standards.md) first.

## Setup

```bash
go mod tidy          # first time only
make test            # must pass before you open a PR
make lint            # needs golangci-lint v2
make test-integration
```

## Adding a module

1. Copy `internal/modules/example` to `internal/modules/<name>` and rename the package.
2. Keep the layers: `domain` (no I/O) → `service` (use cases, depends on `port`) → `adapter/{postgres,http}`.
3. Put SQL in `migrations/NNNN_description.sql`; create and use only your own schema.
4. Register the module in `cmd/minibase/modules.go` — one line.
5. Need something from another module? Use a port interface in `pkg/ports` via `deps.Services`, or subscribe to its events. Never import another module's packages — `test/arch` will fail.
6. Describe endpoints in `api/<name>.yaml` (OpenAPI 3.1).

## Pull requests

- Branch from `main`: `feat/<topic>` or `fix/<topic>`.
- Conventional Commit messages: `feat(auth): rotate refresh tokens on use`.
- Include tests; update docs; add an ADR in `docs/adr/` if you change an architectural decision.
