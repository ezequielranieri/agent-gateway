# AD-001: Clean/Hexagonal Architecture

**Status:** Accepted
**Date:** 2026-08-22

## Context

The agent-gateway needs a maintainable, testable architecture that separates domain logic from infrastructure concerns. The gateway must not know about PostgreSQL, Redis, or specific LLM providers.

## Decision

Adopt Clean/Hexagonal Architecture with four layers:

1. **Domain** (`internal/domain`): Pure entities, sentinel errors, zero external dependencies (stdlib only)
2. **Application** (`internal/usecase`): Use cases depending only on port interfaces (DIP)
3. **Infrastructure** (`internal/adapter`): Adapters implementing ports — postgres, redis, jwt, otel, guardrail, hitl
4. **Interface** (`internal/api`, `internal/middleware`): HTTP handlers (generated), chi middlewares

Composed in `cmd/gateway` (composition root).

## Consequences

- More files and indirection than a thin service
- Each feature slice (rate limiting, audit, HITL, guardrails) lands without touching domain
- Tests use fakes everywhere — unit tests run with `go test ./...` and no testcontainers
- Clear dependency direction: Domain ← Use Cases ← Adapters ← Interface