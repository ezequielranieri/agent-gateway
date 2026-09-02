# DECISIONS.md — agent-gateway

Architecture decisions & project constitution. Every entry answers: what we chose, why, and what we traded away. Written for the next engineer (or the interviewer) who reads this repository cold.

> 🇪🇸 [Versión en español](./DECISIONS.es.md)

This file is the source of truth for any agent (human or AI) working on this repository. Every implementation must respect these rules. If a task conflicts with something defined here, **stop and consult before proceeding**.

Each decision records: the rule (what the code MUST do), the alternatives rejected and why, and the cost the project accepts for choosing it. This last part matters: knowing what a decision costs is what lets you reopen it later with a strong justification instead of a hunch.

Status legend: `Accepted` = settled; `Open` = proposal, do not implement.

---

## 1. Tech Stack (fixed, non-negotiable without explicit discussion)

| Layer | Technology | Notes |
|---|---|---|
| Language | Go 1.22+ | Concurrency native, low overhead |
| Architecture | Clean / Hexagonal | Domain independent of frameworks |
| HTTP | chi | Minimalist, no extra opinions |
| Validation | go-playground/validator | Typed structs + declarative validation |
| Database | PostgreSQL 16 | Source of truth (ACID) + Row Level Security as defense in depth |
| Queries | sqlc + pgx | Complementary: sqlc generates type-safe code over pgx |
| Migrations | goose | Required — sqlc doesn't manage schema |
| Cache / Rate Limit | Redis 7 + redis_rate | Token bucket implemented |
| Access Token | JWT HS256 | Multi-key rotation via `kid` |
| Refresh Token | Opaque + mandatory rotation + reuse detection (family_id) | SHA-256 stored only |
| Password Hashing | Argon2id | OWASP-aligned parameters (m=65536, t=1, p=4) |
| Rate Limiting | Redis + redis_rate (token bucket) | 3 dimensions: requests, tokens, tool_execs per tenant/user/role |
| Observability | OpenTelemetry (stdout exporter) + Prometheus /metrics | Corriable locally, no vendor lock-in |
| Healthchecks | /health, /ready | Live DB + Redis connectivity |
| Testing | testify + table-driven + mocks on ports | testcontainers-go for integration |
| API Contract | OpenAPI 3.1 (oapi-codegen) | Spec = truth, handlers generated |
| Local / Demo | Docker Compose (Postgres + Redis + Gateway) | `make docker-up` → everything runs |
| CI | GitHub Actions (lint, test, build, secret scan) | gitleaks on PR diffs + push range |
| Secrets | Env vars + .env.example — nothing hardcoded | Vault/SealedSecrets for production |

**Explicitly out of MVP:**
- Terraform / Kubernetes
- gRPC (can add later)
- Service mesh
- Multi-region
- Admin UI

**Technical debt (explicit, named):**
1. **Redis SPOF** — single instance in MVP. Exit: migrate to Sentinel/Cluster before production.
2. **Schema-per-tenant isolation** — RLS on shared instance is sufficient. Exit: only if concrete requirement for stronger isolation appears.

---

## 2. Confirmed Design Decisions (don't reopen without strong justification)

### AD-001 · Clean/Hexagonal Architecture — Status: Accepted

**Rule**
- `internal/domain` — pure entities, sentinel errors, **zero external dependencies** (stdlib only).
- `internal/usecase` — application services depending only on `ports` interfaces (DIP).
- `internal/adapter` — implementations: `postgres`, `redis`, `jwt`, `otel`, `guardrail`, `hitl`.
- `internal/api` — generated OpenAPI handlers (thin, delegate to use cases).
- `internal/middleware` — chi middlewares: auth, tenant, ratelimit, audit, hitl.
- Composed in `cmd/gateway` (composition root).

**Why:** The gateway must not know PostgreSQL, Redis, or Gemini exist. The HITL service must not know pgx exists. Tests use fakes everywhere, which is what makes unit tests run with `go test ./...` and no testcontainers.

**Trade-off:** More files and indirection than a thin service. The payoff is each slice (rate limiting, audit, HITL, guardrails) lands without touching the domain.

---

### AD-002 · RLS FORCE + Composite PK (id, tenant_id) as Tenancy Mechanism — Status: Accepted

**Rule**
- Tenancy MUST be enforced by **PostgreSQL Row Level Security FORCE**, never by the application layer alone. Isolation MUST hold against a buggy query, a broken repository layer, or a raw `psql` session.
- Every tenanted table MUST carry `tenant_id`, MUST have RLS enabled with `FORCE`, and MUST have a policy filtering rows on `tenant_id = current_setting('app.tenant_id')::uuid`.
- Global tables (`public.tenants`, `public.roles`) are **global**: neither carries `tenant_id`, neither has RLS, neither may be tenanted.
- Tenanted tables MUST use **composite primary keys `(id, tenant_id)`**.
- The application MUST bind the tenant context per transaction via `SELECT set_config('app.tenant_id', $1, true)` inside a dedicated transaction — the `true` makes it LOCAL. Never a plain `SET` on a pooled connection.

**Alternatives rejected**
- *Schema-per-tenant*: hundreds of small tenants explode catalog and migration/backup tooling; product needs cheap per-tenant operations.
- *Database-per-tenant*: per-tenant dedicated infrastructure must be cheap to operate; one instance serves thousands.
- *Application-layer filtering only (`WHERE tenant_id = ...`)*: not structural — a buggy query or raw SQL session bypasses it; isolation must survive application bugs.
- *ENABLE RLS without FORCE*: a table owner (often the migration user or DBA) bypasses RLS entirely; an accidentally unfiltered query from the app's connection could cross tenants.
- *Single-column `id` primary key*: creates a side channel — a foreign key on an unprotected table, or any `JOIN`, can reference another tenant's row by its plain `id`, and a query returning that id reveals a row exists even if RLS hides its columns.

**Cost accepted**
- Every query needs a tenant context or it silently returns zero rows — debugging an "empty" result requires knowing the GUC.
- Postgres-only mechanism: RLS has no portable equivalent, so the database choice is locked in.
- Every tenant-scoped unit of work pays the cost of a transaction.
- `FOREIGN KEY` references to tenanted tables must repeat the composite column pair.
- Migration and seeding operations that must touch every tenant require elevated privileges.

---

### AD-003 · JWT HS256 + Multi-Key Rotation (kid) — Status: Accepted

**Rule**
- Access tokens MUST be **HS256 JWTs** with **15-minute TTL**, carrying `sub` (user id), `tenant_id`, `role`, `scopes`.
- Multiple signing keys can be active for verification simultaneously, identified by `kid`. Exactly one key is used to *sign* new tokens; all known keys (current and previously-rotated) are accepted for *verification*.
- Algorithm MUST be **pinned to HS256**: verification MUST reject any other signing method, closing classic `alg=none` and key-confusion attacks.

**Alternatives rejected**
- *RS256 (asymmetric)*: more key-management moving parts; rejected for now in favor of implementation simplicity, with documented path to RS256/JWKS when multi-service verification is needed.
- *Opaque session tokens (server-side state)*: no revocation problem, but they need per-request server state; stateless tokens chosen for simplicity.

**Cost accepted**
- HS256 is symmetric — the secret both signs and verifies; a leaked `JWT_SECRET` forges tokens for any tenant, so the secret must be rotated on a schedule.
- Access tokens cannot be revoked before expiry (mitigated by short TTL).
- A second service can only verify tokens it shares the secret with.

---

### AD-004 · Refresh Tokens: Opaque + Rotation + Family Reuse Detection — Status: Accepted

**Rule**
- Refresh tokens MUST be **opaque 256-bit random values** (`crypto/rand`, base64url) returned to the client exactly once and persisted **only as their SHA-256 digest**.
- Rotation MUST keep a **per-family chain**: every refresh revokes the old token, issues a successor in the same `family_id`, and records `replaced_by`.
- Presenting an already-rotated token (revoked with `replaced_by` set) IS the replay signature and MUST revoke the **whole family** (`RevokeFamily`), killing the attacker's freshly issued successor too.
- Revoked-vs-expired check order matters: a token that is both revoked and expired is reported as reuse, not as "expired" — otherwise an attacker's reuse attempt could hide behind a benign-looking error.
- Rotation order MUST fail safely: persist the new token *first*; only revoke the old one after the new one is durably stored; issue the new access token last (least likely to fail, in-memory signing with no I/O).

**Alternatives rejected**
- *Store plaintext refresh tokens*: a database breach would let an attacker replay the token forever.
- *Rotation without family tracking*: in the classic race where both attacker and legitimate user present the same token, whoever is second must not succeed — without family-wide revocation the attacker's stolen successor survives.

**Cost accepted**
- Rotation adds state and concurrency-sensitive writes.
- A lost or untransmitted successor after a refresh forces a re-login.
- Replay of an *expired* token must not be misclassified as theft — rotation logic must distinguish Expired from Revoked.

---

### AD-005 · Argon2id Password Hashing over bcrypt — Status: Accepted

**Rule**
- Passwords MUST be hashed with **Argon2id** using OWASP interactive parameters `m=65536, t=1, p=4` (64 MiB memory, 1 iteration, 4 lanes), a 32-byte key, and a random 16-byte salt.
- Hashes MUST be encoded in the **PHC format** (`$argon2id$v=19$m=65536,t=1,p=4$...`), and `Verify` MUST re-derive the parameters from the stored string itself — never from configuration — so a future parameter bump is transparent to existing hashes.

**Alternatives rejected**
- *bcrypt*: its memory footprint is tiny, so it resists ASIC/GPU brute force poorly by design; the community recommendation for interactive logins has moved on.

**Cost accepted**
- Hashing costs 64 MiB per call — more CPU/memory per login.
- Not a FIPS-approved primitive in some compliance contexts.
- Parameters are fixed in code, so re-hashing on a parameter change is a migration.

---

### AD-006 · Rate Limiting: Redis Token Bucket (3 Dimensions) — Status: Accepted

**Rule**
- Rate limiting MUST use a **token bucket** model via `redis_rate` (Redis + Lua, atomic and distributed).
- Three independent buckets per identity: **requests/min**, **tokens/min** (cost), **tool_execs/min**.
- Identity dimensions: **per tenant**, **per user**, **per role** — configurable precedence.
- Redis MUST NOT be a hard dependency for availability: a nil client or Redis error fails **open** (allows the request) with a WARN log — a healthy rate limiter never becomes a denial of service for legitimate users.
- Exceeded limits MUST return `429` with JSON error body and `Retry-After` header (ceiling seconds).

**Alternatives rejected**
- *Fixed window / sliding window*: token bucket handles burst better and maps naturally to "tokens" and "tool executions" as consumable resources.
- *Fail-closed on Redis outage*: would turn a Redis blip into an availability outage for every authenticated call — unacceptable for a gateway.
- *Single bucket per tenant*: a single compromised user could exhaust the whole tenant's quota; per-`tenant:user:role` keeps one bad actor from DoSing the tenant.

**Cost accepted**
- In-memory fallback (if Redis unavailable) is per-process state: lost on restart and wrong across instances — documented limitation, fine for single-instance posture.
- Three buckets per identity = more Redis keys; accepted for granularity.

---

### AD-007 · Audit Log: Append-Only + Per-Tenant Hash Chaining — Status: Accepted

**Rule**
- The audit trail MUST be **hash-chained per tenant**: every `private.audit_events` row carries `seq`, `prev_hash`, and `chain_hash` with `UNIQUE (tenant_id, seq)`.
- The genesis entry uses `seq = 1` and `prev_hash` = 64 hex zeros; every subsequent row's `prev_hash` MUST equal the previous row's `chain_hash`.
- `chain_hash` MUST be `SHA-256` over the fixed field order `prev_hash|seq|tenant_id|actor_user_id|action|entity_type|entity_id|payload|created_at` where `payload` is the **canonicalized** JSON (decode → re-marshal), and `created_at` is truncated to microsecond precision (Postgres `timestamptz` resolution) — the exact same code path for insert and verify, so no drift.
- Chain verification (`VerifyChain`) MUST be internal: recompute the chain from all stored rows and report the first broken `seq`.
- Audit appends MUST run inside the tenant-bound transaction (RLS `FORCE` would otherwise reject the insert) and MUST be fail-open: an audit failure never fails the main flow, only a WARN log.

**Alternatives rejected**
- *Global chain (one sequence for all tenants)*: a tenant session cannot read other tenants' rows under RLS, so the append would break; it also serializes every tenant on one hot row and leaks cross-tenant ordering.
- *Database trigger computing the hash*: zero drift, but security logic hidden in migration SQL, untestable in pure Go, and against the convention of keeping logic in services.
- *Hashing the full SQL row*: any future migration adding a column would invalidate the whole chain.

**Cost accepted**
- One indexed tail read + insert per audit event (sync, on the write path).
- Verification is O(n) — fine at portfolio volume, revisit if it grows.
- Payloads must stay float64-safe so canonicalization is stable.

---

### AD-008 · Human-in-the-Loop: State Machine + SSE + Full Re-validation — Status: Accepted

**Rule**
- Write actions never mutate directly. The agent creates a `PENDING` approval request with an **opaque token** (32 random bytes, hex) whose **SHA-256 hash** is persisted.
- Approve/reject requires the token (timing-safe compare), a pending non-expired request (24h TTL), and RBAC (`admin`/`operator`).
- On approve, the service **re-validates** the context: payload re-parsed with `DisallowUnknownFields`, resource IDs resolved inside the tenant, then materialize the action and mark request `EXECUTED`.
- Audit is fail-open: if writing the audit row fails, the flow continues with a WARN.
- SSE streaming (`net/http` native) for real-time status updates — simpler than WebSockets, traverses proxies, auto-reconnect in browser.

**Alternatives rejected**
- *WebSockets (nhooyr.io/websocket)*: require sticky sessions, proxy config, reconnection logic; SSE is native, simpler, works with `curl -N`.
- *Approved-but-not-executed state*: adds complexity; approve == execute for MVP. `APPROVED` state exists in enum for future deferred worker.
- *Handler-level approval checks*: project convention is middleware (RequireAuth, RateLimit, Audit); scattered checks are harder to audit and easier to skip.

**Cost accepted**
- No "approved but not executed" state yet — a deferred worker is future work.
- SSE is unidirectional (server→client); if bidirectional becomes needed, WebSockets can be added.

---

### AD-009 · Guardrails: Domain Interface + Local + External Implementation — Status: Accepted

**Rule**
- Guardrails MUST be a **domain interface** (`Guardrail`) in `internal/domain/guardrail` with `CheckInput`, `CheckOutput`, `Violation` types.
- A **local implementation** (`LocalGuardrail`) ships by default: regex patterns, wordlists, PII detection (email, credit card, SSN), prompt injection heuristics — zero network dependency, zero API keys, runs in-process.
- **External classifiers** (OpenAI Moderation, Anthropic, Llama Guard via Ollama) plug in as **adapters** implementing `ExternalClassifier` — never in the domain.
- **CompositeGuardrail** merges local + external results with configurable merge logic (`any_violation`, `all_violation`, `weighted`) and fail behavior (`fallback_local`, `fail_open`, `fail_closed`).
- **ExternalClassifier** adapters implement retry + circuit breaker, support per-category thresholds, and run behind `CompositeGuardrail` with `SendContentExternal` flag for data residency.
- Input validation fails closed (reject); output validation fails closed (reject or sanitize based on severity).
- Guardrail violations are recorded in audit log with severity `critical`.

**Alternatives rejected**
- *External classifier only (Claude API / Llama Guard)*: network dependency, latency, cost, API keys, their rate limits — breaks "runs locally with one command".
- *Hardcoded rules in middleware*: not testable, not extensible, couples HTTP layer to policy.

**Cost accepted**
- Local implementation catches common patterns but not semantic attacks; external classifier adapter fills that gap when needed.
- Regex/wordlist maintenance is manual; accepted for portfolio scope.
- External classifier adds network dependency and cost; gated by `SendContentExternal` flag for data residency requirements.

---

### AD-010 · Model Routing: Provider Port + Fallback + Pricing Abstraction — Status: Accepted (Implemented)

**Rule**
- Model routing MUST be behind a **provider port** (`ModelProvider` interface) in `internal/domain/model`.
- Router selects provider based on: availability, latency SLO, cost budget, tenant preference.
- Fallback chain: primary → secondary → local (Ollama/Llama.cpp) — bounded retries, circuit breaker (half-open).
- **PricingService** maps `model + input_tokens + output_tokens → USD` with versioned price tables per provider (provider + model → USD/1k tokens).
- Pre-estimate cost before routing, post-actual cost after completion, integrated with rate-limit and audit.

**Why:** A gateway that only calls one provider isn't a gateway — it's a proxy. Production needs routing, fallback, and cost control.

**Trade-off:** Adds provider abstraction layer; pricing tables must be maintained.

**Implementado:** Adapter OpenAI (completo), adapters Anthropic/Ollama (stubbed), `FallbackChain` con retries acotados + circuit breaker half-open, `PricingService` con tablas versionadas (migración 0014 poblada con modelos OpenAI, Anthropic, Ollama), pre-estimación/post-costo real integrado con rate-limit y audit.

---

### AD-011 · Tool Sandbox: ToolExecutor Interface + WASM (wazero) — Status: Accepted (Implemented)

**Rule**
- Tool execution MUST be behind a **`ToolExecutor` interface** in `internal/domain/tool`.
- Default implementation: `MockExecutor` for tests, `WasmExecutor` (wazero) for production — WASM runs in-process, no kernel mods, portable, strong isolation.
- Each execution gets isolated module instance; resource limits (fuel, memory, wall time) enforced per execution; read-only FS mounts, no network by default.
- **Bounded agent loop** (max 5 iterations) with HITL gate for tools requiring approval, per-step cost/audit/rate-limit accounting.

**Why:** If the agent calls `rm -rf /` or accesses another tenant's secrets, the gateway must prevent it. Sandbox is the enforcement point.

**Trade-off:** wazero adds binary size and compilation complexity; gVisor/Firecracker are heavier alternatives.

**Implemented:** `WasmExecutor` (wazero) with fuel/memory/wall-time limits, read-only FS mounts, no network by default, per-execution module instantiation; `MockExecutor` for tests; bounded agent loop (max 5 iterations) with HITL gate, per-step cost/audit/rate-limit accounting.

---

## 3. Code Conventions

- **Domain is pure**: `internal/domain` imports nothing but the standard library — entities and sentinel errors, zero external dependencies.
- **SQL lives in two places only**: `migrations/` and `internal/adapter/postgres`. SQL is never called directly from `internal/api` handlers or `internal/usecase` services.
- **All tenant-scoped access goes through the `WithTenant` helper** (`internal/adapter/postgres/db.go`); repositories run every query inside it so RLS is the enforcement point, not a `WHERE` clause.
- **Errors are mapped at the adapter boundary**: pgx driver errors translated into domain errors (`mapPgErr` — e.g. `unique_violation` 23505 → `domain.ErrConflict`); application code never depends on pgx error types.
- **Composite-key awareness**: tenanted tables keyed `(id, tenant_id)`; foreign keys repeat the composite pair.
- **Naming**: tables and columns in `snake_case`; Go types and functions in idiomatic style (`CamelCase` / `camelCase`).
- **Comments are written in English** (EN).
- **Configuration**: `koanf` (YAML + env) — single `configs/config.yaml` versioned, env overrides.

---

## 4. Explicit Prohibitions

- Don't disable, weaken, or bypass FORCE RLS on any tenanted table; don't run tenant-scoped queries outside the `WithTenant` helper.
- Don't add an ORM (GORM, sqlc codegen for queries is fine, ent, or similar) — hand-written pgx SQL for queries, sqlc for type-safe generation only.
- Don't add chi middleware that bypasses the standard chain (auth → tenant → ratelimit → audit → guardrails → router).
- Don't store plaintext refresh tokens or plaintext passwords — SHA-256 digests and Argon2id PHC strings only.
- Don't allow JWT algorithm flexibility — HS256 is pinned; verification rejects any other method (no `alg=none`, no key confusion).
- Don't add `tenant_id` to the global tables `public.tenants` and `public.roles` — they are global by design.
- Don't introduce schema-per-tenant or database-per-tenant — RLS on one shared instance is the tenancy mechanism.

---

## 5. Reference Roadmap (high level)

- [x] **Phase 0** — Foundation: domain entities, RLS schema, auth primitives, middleware chain
- [x] **Phase 1** — Rate limiting: Redis token bucket (reqs/tokens/tools), per tenant/user/role
- [x] **Phase 2** — Audit log: append-only + hash chaining + VerifyChain
- [x] **Phase 3** — HITL: state machine + SSE + re-validation on approve
- [x] **Phase 4** — Guardrails: domain interface + LocalGuardrail (regex/wordlist/PII)
- [x] **Phase 5** — Model routing: provider port + fallback + pricing abstraction
- [x] **Phase 6** — Tool sandbox: `ToolExecutor` interface + wazero WASM adapter
- [x] **Phase 7** — External guardrail classifier adapter (OpenAI Moderation, Llama Guard)
- [x] **Phase 8** — CI/CD pipeline + secret management + canary deploy + observability

---

## How to Add a Decision

1. Pick the next free number (`AD-012`, ...) in section 2.
2. Copy the template below, fill it in, and keep the section ordered.

```markdown
### AD-XXX · <Title> — Status: Accepted

**Rule**
- <what the code MUST do, as MUST-style bullets>

**Alternatives rejected**
- *<alternative>*: <technical reason it was rejected>

**Cost accepted**
- <honest trade-off the project accepts>
```

3. Update the Spanish mirror in `DECISIONS.es.md` in the same session so both languages stay in sync.