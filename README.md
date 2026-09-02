# agent-gateway

🇪🇸 [Versión en español](./README.es.md)

Multi-tenant Agent Gateway / Control Plane for LLM agents — the **only path** between your application and the model. Every call authenticated, authorized, rate-limited, audited, and guarded.

> Status: **MVP complete** — Phases 0-8 implemented (Foundation, Rate Limiting, Audit Log, HITL, Guardrails, **Model Routing**, **Tool Sandbox**, **External Guardrail Classifier**, **CI/CD + Observability**). **Pricing tables (migration 0014)** seeded with OpenAI, Anthropic, and Ollama model costs.

## The problem

Any system running LLM agents at scale eventually faces three uncomfortable questions:

- **How do you ensure no call bypasses the gateway?** Without a mandatory interception layer, a single `http.Post` to an LLM endpoint leaks data, burns budget, and evades every control.
- **How does one tenant's data, budget, and agent behavior stay isolated from another's?** A missing `WHERE tenant_id = ?` in one query, a shared rate limit bucket, or a leaked tool execution context is a cross-tenant incident — not a bug, a breach.
- **How do you prove what happened when something goes wrong?** "The model did it" is not an audit trail. You need immutable logs of every model call, tool execution, guardrail decision, and human approval — queryable, replayable, tamper-evident.

`agent-gateway` answers those three with the simplest tools that provide the right guarantee:

- **Zero-bypass architecture**: chi middleware chain — auth → tenant resolution → rate limit → audit → guardrails → model router. No endpoint reaches the model without passing the full chain.
- **Tenant isolation enforced at the database**: Row Level Security (RLS) **FORCE** on every tenanted table, composite primary keys `(id, tenant_id)`, tenant context bound per transaction via `set_config(..., true)`. Two independent layers (DB + middleware), not one.
- **Audit log that survives compromise**: Append-only PostgreSQL table with per-tenant hash chaining (`seq`, `prev_hash`, `chain_hash`), canonicalized JSON payloads, `VerifyChain` detector. Tampering leaves evidence.
- **Human-in-the-Loop as a reusable service**: State machine in PostgreSQL + SSE streaming. Create approval request → human reviews via token → re-validate context → materialize. The same service for any write action across any agent.
- **Guardrails as a domain interface**: `Guardrail` interface in `internal/domain/guardrail` — local implementation (regex, wordlist, PII patterns) ships by default; external classifier (Claude API, Llama Guard) plugs in as an adapter without touching domain logic.
- **Model Routing with fallback & pricing**: `ModelProvider` port in `internal/domain/model` — OpenAI adapter (full), Anthropic/Ollama (stubbed), priority-based router, fallback chain with bounded retries + half-open circuit breaker, versioned pricing tables (provider+model → USD/1k tokens), pre-estimate/post-actual cost tracking integrated with rate-limit and audit.
- **Tool Sandbox with WebAssembly isolation**: `ToolExecutor` port in `internal/domain/tool` — `WasmExecutor` (wazero) with fuel/memory/wall-time limits, read-only FS mounts, no network by default, per-execution module instantiation; `MockExecutor` for tests; bounded agent loop (max 5 iterations) with HITL gate, per-step cost/audit/rate-limit accounting.

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                         HTTP Layer (chi)                            │
│  router.go · middleware.go (auth, tenant, ratelimit, audit, hitl)   │
└───────────────────────────▲───────────────────────────▲─────────────┘
                             │                           │
           ports.AuthService  │                    ports.Guardrail
                             │                           │
┌───────────────────────────┴───────────────────────────┴─────────────┐
│                      Application Layer (Use Cases)                 │
│  services/gateway.go  — request pipeline, model routing            │
│  services/ratelimit.go — token bucket per tenant/user/role         │
│  services/audit.go     — append-only + hash chain                  │
│  services/hitl.go      — approval state machine + SSE              │
│  services/guardrail.go — input/output validation pipeline          │
│  services/chat.go      — chat orchestration (pre-estimate → route) │
│  ports/repositories.go · ports/services.go  (DIP: interfaces only) │
└───────────────────────────▲───────────────────────────▲─────────────┘
                             │                           │
┌───────────────────────────┴───────────────────────────┴─────────────┐
│                    Infrastructure Layer (Adapters)                 │
│  postgres/  (pgx, sqlc, RLS tenant sessions, audit chain)          │
│  redis/     (go-redis + redis_rate token bucket)                   │
│  jwt/       (golang-jwt/v5 HS256, multi-key rotation via kid)      │
│  otel/      (OpenTelemetry stdout exporter + Prometheus /metrics)  │
│  guardrail/ (LocalGuardrail — regex/wordlist/PII patterns)         │
│  guardrail/ (ExternalClassifier — OpenAI/Anthropic/LlamaGuard)     │
│  hitl/      (SSE handler + PG state machine)                       │
│  provider/  (OpenAI, Anthropic, Ollama, Mock)                      │
│  pricing/   (versioned price tables, migration 0014 seeded)           │
│  tool/      (WasmExecutor — wazero, MockExecutor — tests)          │
└─────────────────────────────────────────────────────────────────────┘

Domain Layer (internal/domain): pure entities + sentinel errors, ZERO
dependencies — imports nothing but the standard library.
```

```
cmd/
├── gateway/     composition root: HTTP server, middleware chain, DI
├── bootstrap/   CLI: creates first Super Admin + default tenant
internal/
├── domain/           entities: Tenant, User, Role, Quota, AuditEvent, ReviewRequest, GuardrailViolation
│   └── model/        ModelProvider port, ChatRequest/Response, RouterConfig, PricingService, CircuitBreaker
│   └── tool/         ToolExecutor port, ToolCall/ToolResult, ToolConfig, sentinel errors
├── usecase/          application services (gateways, ratelimit, audit, hitl, guardrail, chat)
├── adapter/          postgres, redis, jwt, otel, guardrail, hitl, provider, pricing, tool
├── api/              OpenAPI 3.1 handlers (oapi-codegen generated)
└── middleware/       chi middlewares: auth, tenant, ratelimit, audit, hitl
migrations/           SQL schema + RLS policies + seed
docker-compose.yml    postgres:16 + redis:7 + migrate
Makefile · .env.example · sqlc.yaml · goose.yaml
```

## Tenancy Model & RLS (defense in depth)

Tenants and roles are **global**: `public.tenants` is the registry; `public.roles` is the shared role catalog. Neither carries `tenant_id`, neither has RLS.

Every business table carries `tenant_id` and is RLS-protected **and FORCED**:

```sql
CREATE POLICY tenant_isolation ON private.audit_events
  USING (tenant_id = current_setting('app.tenant_id')::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);
ALTER TABLE private.audit_events FORCE ROW LEVEL SECURITY;
```

- `current_setting('app.tenant_id')` returns `NULL` when unset — a NULL predicate is **always false**, so a missing tenant context yields **zero rows, never a leak**.
- The application binds the tenant **inside a transaction**: `SELECT set_config('app.tenant_id', $1, true)` — the `true` makes it LOCAL. Plain `SET` on a pooled connection would leak tenant A's identity into tenant B's reused connections; transaction scoping prevents that.
- `FORCE` extends RLS to the table owner, so even a raw `psql` session or a buggy query cannot accidentally cross tenants without superuser rights.
- Tenanted tables use **composite primary keys** `(id, tenant_id)`. This closes the side channel where a foreign key from an unprotected table could reference (and therefore reveal) another tenant's row.

## Security Design

| Concern | Mechanism |
|---|---|
| Password storage | Argon2id (m=65536, t=1, p=4, 32-byte key), PHC-encoded, `Verify` parses the format — never trusts config |
| Access token | JWT HS256, `sub`/`tenant_id`/`role`/`scopes`, **15 min TTL**, algorithm pinned to HS256, multi-key rotation via `kid` |
| Refresh token | Opaque 256-bit random, stored as SHA-256 only, **mandatory rotation** |
| Rotation | Every refresh revokes the old token, issues a successor in the same `family_id` |
| Replay detection | Presenting an already-rotated (revoked + `replaced_by`) token revokes the **whole family** |
| Tenant isolation | PostgreSQL RLS FORCE + composite PK + middleware cross-check (token tenant = URL tenant) |
| Audit integrity | Append-only `private.audit_events` with per-tenant hash chaining (`seq`, `prev_hash`, `chain_hash`); `VerifyChain` detects tampering; events carry severity (info/warn/critical) |
| Rate limiting | Redis + `redis_rate` (token bucket), 3 dimensions: requests/min, tokens/min, tool_execs/min — per tenant, user, role |
| Guardrails | Domain interface `Guardrail` + `LocalGuardrail` (regex, wordlist, PII, injection patterns); `CompositeGuardrail` with external classifiers (OpenAI Moderation, Anthropic, Llama Guard) — merge logic: any/all/weighted, fail behaviors: fallback_local/fail_open/fail_closed |
| HITL | State machine in PG (`PENDING` → `APPROVED`/`REJECTED`/`EXPIRED`), opaque token (SHA-256 stored), SSE streaming, full re-validation on approve |

## Quickstart

Requires: Go 1.22+, Docker with Compose.

```bash
# 1. start db + redis, apply migrations
make docker-up
make migrate-up
```

14 migrations will run (0001_extensions through 0014_pricing_tables).

```bash
# 2. configure the app
cp .env.example .env          # set DATABASE_URL, JWT_SECRET (never "change-me")

# 3. create the first Super Admin
make bootstrap

# 4. run the gateway
make run

# sanity check
curl http://localhost:8080/health   # -> {"status":"ok"}
```

### Demo flow (after bootstrap)

```bash
# 1. Super Admin creates a tenant
curl -s -X POST http://localhost:8080/v1/tenants \
  -H "Authorization: Bearer $SUPER_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Acme Inc"}'

# 2. Register the first user of that tenant (auto-assigned "admin" role)
curl -s -X POST http://localhost:8080/v1/tenants/<tenantID>/users \
  -H "Authorization: Bearer $SUPER_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@acme.com","password":"a-strong-password"}'

# 3. That user logs in
curl -s -X POST http://localhost:8080/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@acme.com","password":"a-strong-password","tenant_id":"<tenantID>"}'
# -> {"access_token":"...","refresh_token":"...","expires_in":900}

# 4. Call an LLM through the gateway (rate limited, audited, guarded)
curl -s -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hello"}]}'

# 5. Request a human approval (HITL) — see SSE stream in terminal
curl -N -H "Authorization: Bearer $ACCESS_TOKEN" \
  http://localhost:8080/v1/reviews/<request_id>/stream
```

The full contract — every endpoint, every status code, every request/response schema — is in [`openapi.yaml`](openapi.yaml) (generated from source of truth).

## How to Test

```bash
# Unit tests (no DB required) — table-driven, mocks on ports
go test ./internal/domain/... ./internal/usecase/...

# Integration tests (real Postgres + Redis via testcontainers-go)
# Requires Docker running
make test

# Lint
make lint
```

Integration tests run against **real** PostgreSQL and Redis via testcontainers-go — no mocks in the persistence, rate-limiting, or audit layers. Every repository, every migration (including RLS policies), and the rate limiter's atomicity are tested against real infrastructure.

## Verified Guarantees

Two of the system's more important correctness claims are measured, not asserted:

- **Tenant isolation holds at the database level.** Verified by integration tests that assert: (a) with no tenant context, a raw `SELECT * FROM private.audit_events` returns zero rows; (b) inside a tenant-bound transaction the same table returns exactly that tenant's rows; (c) a cross-tenant INSERT is rejected by `WITH CHECK` policy.
- **Rate limiter is atomic under real concurrency.** Verified by firing 50 concurrent goroutines at the same rate limit key with a limit of 3, and asserting exactly 3 succeed and 47 are rejected — not "approximately," exactly.

## What's Deliberately Not Implemented (Yet)

Same principle as `go-authz` and `agro-iam`: named explicitly, not silently absent.

| Item | Reason | Exit Criteria |
|---|---|---|
| Schema-per-tenant isolation | RLS on shared instance is sufficient for current threat model | Only if concrete requirement for stronger isolation appears |
| Full-history secret scanning on every push | CI scans PR diffs + pushed range; full history = alert fatigue | Gitleaks + Trivy in CI; scheduled job if needed |

## CI/CD Pipeline (Phase 8)

### Workflows

| Workflow | Trigger | Purpose |
|---|---|---|
| `.github/workflows/ci.yml` | Push to master, PR | Lint, test, build, secret scan, vulnerability scan |
| `.github/workflows/cd-staging.yml` | Push to master | Deploy to staging (auto) |
| `.github/workflows/release.yml` | Tag push (v*) | Build release, sign images, deploy to production |

### Secret Management

- **GitHub Secrets**: CI/CD secrets (registry tokens, SSH keys, webhook URLs)
- **SOPS + age**: Encrypted `.env.staging.enc` and `.env.prod.enc` in repo
  - Only authorized age public keys can decrypt
  - Keys managed via `.sops.yaml` config
- **No secrets in plaintext** in repo or Docker images

### Canary Deployment

```bash
# Full canary flow (deploy → monitor → promote)
./scripts/canary-deploy.sh full v1.0.0-rc2

# Or step by step:
./scripts/canary-deploy.sh deploy v1.0.0-rc2
./scripts/canary-deploy.sh monitor
./scripts/canary-deploy.sh promote

# Rollback if needed
./scripts/canary-deploy.sh rollback
```

**Promotion criteria** (configurable):
- Success rate ≥ 95% (default)
- Monitor duration: 10 minutes (default)
- P99 latency < 5s
- Error rate < 5%

### Observability Stack

| Component | Purpose |
|---|---|
| **Prometheus** | Metrics collection + alerting rules |
| **Grafana** | Dashboards (auto-provisioned) |
| **Alertmanager** | Alert routing (email, Slack, PagerDuty) |
| **Loki + Promtail** | Log aggregation |
| **Jaeger** | Distributed tracing |

**Key dashboards** (auto-provisioned):
- `Agent Gateway - Overview` — Service health, latency, costs, guardrails, HITL, tools

**Alert rules** (`prometheus/rules/alerts.yml`):
- Service down, high error rate, high latency
- Rate limit exhaustion, DB/Redis saturation
- Guardrail violation spikes, auth failures
- Cost spikes, model fallback rate

### Quickstart: Local CI

```bash
# Run CI locally (requires act)
act -j lint
act -j test
act -j build

# Or just run the steps manually:
golangci-lint run ./...
go test -race -count=1 ./...
go build -o bin/gateway ./cmd/gateway
docker build -t agent-gateway:local .
```

### Release Process

```bash
# 1. Create and push tag
git tag v1.0.0
git push origin v1.0.0

# 2. GitHub Actions runs:
#    - GoReleaser creates GitHub Release + binaries
#    - Docker images built & pushed to GHCR (signed with cosign)
#    - Production deployment (blue-green)

# 3. Verify deployment
curl https://api.agent-gateway.com/health
```

## Roadmap

- [x] **Phase 0** — Foundation: domain entities, RLS schema, auth primitives, middleware chain
- [x] **Phase 1** — Rate limiting: Redis token bucket (reqs/tokens/tools), per tenant/user/role
- [x] **Phase 2** — Audit log: append-only + hash chaining + VerifyChain
- [x] **Phase 3** — HITL: state machine + SSE + re-validation + atomic approve
- [x] **Phase 4** — Guardrails: domain interface + LocalGuardrail (regex/wordlist/PII)
- [x] **Phase 5** — Model routing: provider port + fallback chain + circuit breaker + pricing abstraction + OpenAI adapter
- [x] **Phase 6** — Tool sandbox: `ToolExecutor` interface + `WasmExecutor` (wazero) with fuel/memory/wall-time limits, read-only FS mounts, no network by default, per-execution instantiation, bounded agent loop (max 5), HITL gate, per-step cost/audit/rate-limit
- [x] **Phase 7** — External guardrail classifier adapter (OpenAI Moderation, Anthropic, Llama Guard) with composite merge logic (any/all/weighted), fail behaviors (fallback_local/fail_open/fail_closed), HTTP client with retry + circuit breaker
- [x] **Phase 8** — CI/CD pipeline (GitHub Actions), secret management (SOPS + GitHub Secrets), canary deploy, observability stack (Prometheus/Grafana/Alertmanager/Loki/Jaeger)

## License

MIT — see [LICENSE](LICENSE).