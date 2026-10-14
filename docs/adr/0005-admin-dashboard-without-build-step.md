# ADR-0005: Admin dashboard without a frontend build step, with its own operator accounts

- **Status:** Accepted
- **Date:** 2026-10-14
- **Supersedes:** the "SvelteKit admin UI" choice in architecture.md §3

## Context
The dashboard was pulled forward ahead of storage and realtime. The original plan was SvelteKit, which needs Node.js to build and adds a Node stage to the Docker build. Operators also need a way to sign in that does not depend on the app's own users.

## Decision
1. **No build step.** The UI is plain HTML, CSS and ES modules embedded in the Go binary, using Preact + htm (13 KB, vendored with its licence and checksum) for components.
2. **Separate operator accounts** in the admin module's own `admin` schema, created with `minibase admin create` or bootstrapped from environment variables. They are not app users and not API keys.
3. **Cookie sessions with strict CSRF and CSP rules** (see architecture.md §5.7).
4. **The SQL editor runs on a connection that is discarded afterwards.**

## Alternatives considered
- **SvelteKit / React + Vite** — richer tooling, but every contributor and every Docker build would need Node.js; bundles are larger.
- **Sign in with the service key** — no accounts to manage, but one shared secret, no per-person audit, and the key ends up in browsers.
- **App users with an admin flag** — mixes operators with customers and requires the auth module.

## Consequences
- ✅ `go build` produces the whole product; the image stays small; the dashboard works with `auth` disabled.
- ✅ A strict Content-Security-Policy is possible because there is no inline script.
- ⚠️ No TypeScript or bundler conveniences in the UI. If it grows past a few thousand lines, revisit (a build step can be added without changing the API).
- ⚠️ The SQL editor is deliberately all-powerful; admin accounts must be protected accordingly.
