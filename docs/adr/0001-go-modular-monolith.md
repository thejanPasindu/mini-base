# ADR-0001: Go modular monolith as the server architecture

- **Status:** Proposed
- **Date:** 2026-10-14

## Context
The project's core promise is *lightweight* and *modular*. Supabase achieves feature breadth with ~10 services in several runtimes, which costs memory, ops complexity, and contributor onboarding. We need modularity without that overhead.

## Decision
Build one Go binary containing all modules, each implementing `kernel.Module` and toggled at startup by `MINIBASE_MODULES`. Module isolation is enforced in code (Go `internal/` packages, `depguard` lint rules, port interfaces, per-module DB schema).

## Alternatives considered
- **Microservices (one container per module)** — independent scaling, but multiplies RAM, images, networking and failure modes. Rejected for v1.
- **Node.js / TypeScript** — same language as the SDK, large contributor pool; but higher idle memory, needs a runtime in the image, weaker for many concurrent WebSockets.
- **Rust** — smallest footprint, but slower iteration and a smaller contributor pool for a web backend.
- **Java / Spring Boot** — mature ecosystem, but JVM idle memory (≥150 MB) contradicts G1. GraalVM native-image adds build complexity.

## Consequences
- ✅ Single static binary (~15–25 MB), distroless image, ARM support for free.
- ✅ In-process module calls; one log stream; trivial deployment.
- ⚠️ Discipline required to keep boundaries clean — enforced by lint and reviews.
- ⚠️ Scaling one module independently needs running the same binary with a subset of modules (`--modules=realtime`) plus a shared event bus adapter (future).
