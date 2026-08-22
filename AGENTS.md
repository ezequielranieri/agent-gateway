# AGENTS.md

## Status

Greenfield scaffold. No Go code, `go.mod`, build/lint/test tooling, or CI yet — the repo is only `README.md`, `README.es.md`, `DECISIONS.md`, `DECISIONS.es.md`, `STACK.md`, `SPEC.md`, plus an empty directory skeleton (`.gitkeep` files). Not even a git repo is initialized. There is nothing to build or test.

## Source of truth

- `SPEC.md` is the authoritative functional spec (Spanish, state "Cerrada" = decisions are locked). Do not re-litigate its closed decisions: control plane (SuperAdmin/tenants) vs data plane (users/roles/permissions) never mix; manual tenant provisioning (no self-service); JWT HS256 access + opaque refresh tokens with mandatory rotation and `family_id` reuse detection; permissions as `recurso:accion`; MFA, SSO, M2M/service accounts, and admin UI are explicitly out of scope for v1.
- `STACK.md` is the authoritative technical spec (state "Cerrado" = decisions are locked). Go 1.22+, chi, PostgreSQL 16 + RLS FORCE, sqlc + pgx, goose, Redis 7 + redis_rate, JWT HS256, Argon2id, zerolog + Prometheus + OpenTelemetry, testcontainers-go, OpenAPI 3.1, Docker Compose + GitHub Actions. Do not substitute or second-guess these choices without a documented reason.
- `DECISIONS.md` / `DECISIONS.es.md` are the project constitution. Every ADR is binding. AD-001 through AD-009 are `Accepted` — do not reopen without strong justification. AD-010 and AD-011 are `Open` (design phase).
- All three files are locked. Any deviation from any must be raised explicitly with the user before implementation, not decided silently.

## Architecture (from directory layout — hexagonal/clean)

- `cmd/gateway` — service entrypoint (composition root)
- `cmd/bootstrap` — CLI to create first Super Admin
- `internal/domain` — core entities (Tenant, User, Role, Quota, AuditEvent, ReviewRequest, GuardrailViolation) + sentinel errors
- `internal/usecase` — application services (gateway, ratelimit, audit, hitl, guardrail)
- `internal/adapter/{postgres,redis,jwt,otel,guardrail,hitl}` — ports/adapters
- `internal/api` — generated OpenAPI handlers (thin, delegate to use cases)
- `internal/middleware` — chi middlewares (auth, tenant, ratelimit, audit, hitl)
- `migrations` — SQL migrations (goose)
- `test/integration` — integration tests (testcontainers-go per SPEC)
- `.github/workflows` — empty; no CI defined yet

## Documented technical debt (do not silently resolve)

From `STACK.md`: Redis is a single point of failure in the MVP (Sentinel/Cluster deferred); no tool sandbox (gateway routes, doesn't execute); no model routing/fallback; no external guardrail classifier; no schema-per-tenant isolation (RLS on shared instance is the tenancy mechanism).

## SDD Session Preflight (cached for this session)

- **Pace**: `auto` — run all phases back-to-back, gatekeeper validates between phases
- **Artifact store**: `engram` — persistent memory only, no `openspec/` files
- **Delivery strategy**: `auto-chain` — stacked PRs automatically if forecast >400 lines
- **Chain strategy**: `stacked-to-main` — each PR merges to main in order
- **Review budget**: `400` lines