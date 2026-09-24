# Tasks: Tool Registry Hardening

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | 1200-1500 |
| 400-line budget risk | High |
| Chained PRs recommended | Yes |
| Suggested split | PR 1: Foundation (DB, Domain, Repo) → PR 2: Core (Validation, Executor) → PR 3: Integration (Handlers, Admin API, Boot) → PR 4: Tests & Docs |
| Delivery strategy | ask-on-risk |
| Chain strategy | stacked-to-main |

Decision needed before apply: Yes
Chained PRs recommended: Yes
Chain strategy: stacked-to-main
400-line budget risk: High

### Suggested Work Units

| Unit | Goal | Likely PR | Focused test command | Runtime harness | Rollback boundary |
|------|------|-----------|----------------------|-----------------|-------------------|
| 1 | Database schema, domain entities, SQLC repository, hash computation | PR 1 | `go test ./internal/domain/tool/... -v` | Unit tests only (no DB) | Migration 0018 down.sql; revert domain files |
| 2 | Tool repository with LRU cache + audit, admin API handlers | PR 2 | `go test ./internal/adapter/postgres/... -v -run TestToolRepository` | Test DB required | Revert repo + admin handlers |
| 3 | Chat handler validation, usecase wiring, WASM executor limits enforcement | PR 3 | `go test ./internal/api/handlers/... -v -run TestChatValidation` | Test DB + wasm executor | Revert chat.go, router.go, executor.go |
| 4 | Boot seed, config, main.go init, mettle corpus, limitations doc | PR 4 | `go test ./test/mettle/... -v` | Full integration | Revert config.go, main.go, docs |

---

## Phase 1: Foundation — Database, Domain, Repository Interface

- [x] 1.1 Create `migrations/0018_tool_definitions.up.sql` with table schema per spec (id, name, description, input_schema, grants, fuel_limit, memory_pages, hash, is_active, created_at, updated_at; unique index on name; RLS enabled)
- [x] 1.2 Create `migrations/0018_tool_definitions.down.sql` with `DROP TABLE tool_definitions`
- [x] 1.3 Create `internal/domain/tool/registry.go` with:
  - `ToolDefinition` entity (ID, Name, Description, InputSchema, Grants, FuelLimit, MemoryPages, Hash, IsActive, CreatedAt, UpdatedAt)
  - `ToolRepository` interface (GetByName, CreateToolDefinition, UpdateToolDefinition, DeactivateToolDefinition, UpsertToolDefinition, InitFromConfig)
  - Sentinel errors: `ErrToolNotFound`, `ErrToolDefinitionMismatch`
- [x] 1.4 Create `internal/domain/tool/errors.go` (or add to registry.go) — ensure `ErrToolDefinitionMismatch` and `ErrToolResourceExhausted` are defined
- [x] 1.5 Create `internal/domain/tool/config.go` with `CacheTTL`, `CacheMaxEntries` fields in `ToolConfig`; ensure `EffectiveFuel()` and `EffectiveMemoryPages()` exist as fallback
- [x] 1.6 Create `internal/adapter/postgres/sqlc/tool.sql` with SQLC queries for all CRUD operations + upsert + boot seed bulk upsert
- [x] 1.7 Run `sqlc generate` to produce `internal/adapter/postgres/sqlc/tool.sql.go`
- [x] 1.8 **RED Test**: Create `internal/domain/tool/hash_test.go` — table-driven tests for `ComputeHash` canonicalization (identical defs → same hash; field changes → different hash; key reordering → same hash; empty description/parameters handled)
- [x] 1.9 **GREEN Test**: Implement `ComputeHash` in `internal/domain/tool/registry.go` using `github.com/gibson042/canonicaljson-go` — make hash tests pass
- [x] 1.10 **REFACTOR**: Verify hash computation matches proposal spec exactly (SHA-256 lowercase hex, 64 chars, canonical JSON with sorted keys, no whitespace)

---

## Phase 2: Core Implementation — Repository with Cache & Audit, Admin API

- [ ] 2.1 Create `internal/adapter/postgres/tool_repository.go` implementing `ToolRepository`:
  - Embed SQLC queries
  - Add LRU cache (`github.com/hashicorp/golang-lru/v2` or similar) with 5-min TTL, max 1000 entries
  - Cache key: tool name (exact case, no normalization — **STRICT case-sensitive per resolved Q4**)
  - Cache invalidation on Create/Update/Deactivate; full clear after boot seed
  - TTL expiration via LRU library
- [ ] 2.2 Implement all `ToolRepository` methods in `tool_repository.go`:
  - `GetByName`: cache lookup → DB query → cache store
  - `CreateToolDefinition`: DB insert → invalidate cache → emit audit event
  - `UpdateToolDefinition`: DB update → invalidate cache → emit audit event; return `hashChanged` bool
  - `DeactivateToolDefinition`: DB soft delete → invalidate cache → emit audit event
  - `UpsertToolDefinition`: DB upsert (ON CONFLICT DO UPDATE) → no cache invalidation (boot seed handles bulk)
  - `InitFromConfig`: **Single transaction for all upserts** (per resolved Q3 — fail-closed)
- [ ] 2.3 Add audit event emission in repository write methods using existing `AuditRepository.Append()`:
  - Payload: `Operation` (CREATE/UPDATE/DELETE), `ToolName`, `OldHash`, `NewHash`, `ChangedFields`, `FullDefinition`
  - Actor: super-admin user ID for admin API; `system` for boot seed
  - Severity per design table (CREATE/upsert=info, UPDATE grants/limits=warn, UPDATE definition=critical, DELETE=warn)
- [ ] 2.4 **RED Test**: Create `internal/adapter/postgres/tool_repository_cache_test.go` — test cache hit/miss, TTL expiry, invalidation on write
- [ ] 2.5 **RED Test**: Create `internal/adapter/postgres/tool_repository_audit_test.go` — verify audit events emitted with correct payload for each operation
- [ ] 2.6 **GREEN Test**: Implement repository methods to pass cache and audit tests
- [ ] 2.7 Create `internal/api/handlers/admin_tools.go` with `AdminToolsHandler`:
  - `POST /admin/tools` — CreateToolRequest validation, compute hash, call repo.Create, return **computed hash in response** (per resolved Q2)
  - `PATCH /admin/tools/{name}` — UpdateToolRequest validation, call repo.Update, return new hash in response if definition fields changed
  - `DELETE /admin/tools/{name}` — call repo.Deactivate, return 204
  - All endpoints: require super-admin via existing `middleware.NewAuth` with role check
- [ ] 2.8 Register admin routes in `cmd/gateway/main.go` (or router setup) under `/admin/tools` with auth middleware
- [ ] 2.9 **RED Test**: Create `internal/api/handlers/admin_tools_test.go` — test request parsing, validation, auth (403 without super-admin), CRUD flows

---

## Phase 3: Integration — Handler Validation, Usecase Wiring, Executor Enforcement

- [ ] 3.1 Modify `internal/api/handlers/chat.go`:
  - In `convertToUsecaseRequest` (or new validation function): parse `req.Tools` (json.RawMessage array)
  - For each tool: call `ToolRepository.GetByName` → if not found, return `ErrToolNotFound` → HTTP 400 `tool_not_found`
  - Compute hash of request tool definition (name, description, parameters) using same `ComputeHash`
  - Compare with registry hash → if mismatch, return `ErrToolDefinitionMismatch` → HTTP 400 `tool_definition_mismatch`
  - Build validated tool refs with registry grants/limits for downstream
  - **Reject entire request on any validation failure** (per spec)
- [ ] 3.2 Modify `internal/usecase/chat/router.go`:
  - Inject `ToolRepository` into chat usecase constructor
  - Pass validated tool definitions (with grants, fuel_limit, memory_pages from registry) to orchestrator/executor
- [ ] 3.3 Modify `internal/usecase/chat/orchestrator.go`:
  - Accept validated tools from router
  - Pass tool config (grants, limits) to `ToolExecutor.Execute`
- [ ] 3.4 Modify `internal/adapter/tool/wazero/executor.go`:
  - Read `fuel_limit` and `memory_pages` from validated tool definition
  - **Resolver priority**: registry limits (if > 0) → config fallback (`ToolConfig.EffectiveFuel()`, `EffectiveMemoryPages()`)
  - **Fail closed**: if both registry and config yield zero limits, return `ErrToolResourceExhausted` immediately (no module creation)
  - Call `wazero.NewModuleConfig().WithFuel(fuel_limit).WithMemoryPages(memory_pages)`
  - Catch wazero fuel/memory exhaustion errors → wrap and return `ErrToolResourceExhausted`
- [ ] 3.5 **RED Test**: Create `internal/api/handlers/chat_validation_test.go` — integration test with test DB: register tool, send chat request with valid/invalid/tampered tools, assert HTTP codes and error codes
- [ ] 3.6 **RED Test**: Create `internal/adapter/tool/wazero/executor_limits_test.go` — mock wazero; verify `WithFuel`/`WithMemoryPages` called with correct values; test fail-closed when limits missing
- [ ] 3.7 **GREEN Test**: Implement validation and executor changes to pass integration tests

---

## Phase 4: Configuration, Boot Seed, Startup Wiring

- [ ] 4.1 Modify `internal/config/config.go`:
  - Add `CacheTTL` and `CacheMaxEntries` to `ToolConfig` with viper bindings
  - **Env override**: `TOOL_CACHE_TTL` and `TOOL_CACHE_MAX_ENTRIES` (per resolved Q1)
  - Add `InitToolRegistry(dbPool, auditRepo, cfg)` function called after DB pool creation
- [ ] 4.2 Modify `cmd/gateway/main.go`:
  - Initialize tool repository after DB pool, before handlers
  - Pass `ToolRepository` to chat usecase builder
  - Call `InitToolRegistry` during startup (single-threaded, before HTTP server starts)
- [ ] 4.3 Verify boot seed in `InitFromConfig`:
  - Iterate `ToolConfig.Tools[]`
  - For each: compute hash from `FunctionDef` (name, description, parameters)
  - Upsert with grants, fuel_limit, memory_pages from config
  - **Single transaction** for all upserts (per resolved Q3)
  - Clear cache after bulk upsert
- [ ] 4.4 **RED Test**: Create `internal/config/tool_registry_init_test.go` — test boot seed populates registry, idempotent on restart, updates on config change
- [ ] 4.5 **GREEN Test**: Implement boot seed logic to pass tests

---

## Phase 5: Testing — Mettle Corpus & Integration Coverage

- [ ] 5.1 Create `test/mettle/tool_registry/scenario_01_injection.yaml` — unknown tool ID → 400 tool_not_found
- [ ] 5.2 Create `test/mettle/tool_registry/scenario_02_hash_mismatch.yaml` — known tool ID, modified definition → 400 tool_definition_mismatch
- [ ] 5.3 Create `test/mettle/tool_registry/scenario_03_resource_exhaustion.yaml` — valid tool, low fuel limit → ErrToolResourceExhausted
- [ ] 5.4 Create `test/mettle/tool_registry/scenario_04_baseline.yaml` — valid tool, correct hash, adequate limits → 200 OK
- [ ] 5.5 Create `test/mettle/tool_registry/main_test.go` — mettle test runner for the 4 scenarios
- [ ] 5.6 **RED Test**: Create integration test for Admin API CRUD — register via POST, verify in DB, update via PATCH (hash unchanged for grants/limits, recomputed for definition), deactivate via DELETE, verify lookup fails
- [ ] 5.7 **RED Test**: Create integration test for audit events — after each admin write, query audit log; verify event exists with correct operation, old/new hash, changed fields
- [ ] 5.8 **RED Test**: Create integration test for concurrent tool executions — each gets fresh fuel/memory limits independently
- [ ] 5.9 **GREEN Test**: Run all mettle scenarios and integration tests; fix any failures

---

## Phase 6: Documentation & Cleanup

- [ ] 6.1 Update `docs/limitations.md` — add cache staleness entry per design (multi-instance: local LRU 5-min TTL, invalidation only on local writes, other instances serve stale data up to 5 min)
- [ ] 6.2 Add code comments for hash computation canonicalization requirements (sorted keys, no whitespace, deterministic)
- [ ] 6.3 Verify no dead code or unused imports introduced
- [ ] 6.4 Run full test suite: `go test ./...` — ensure no regressions in existing chat completion flows

---

## Explicit Constraints from Resolved Questions

The following constraints MUST be enforced in the corresponding tasks:

| Resolved Question | Constraint | Enforced In Task |
|-------------------|------------|------------------|
| Cache TTL config location | `CacheTTL`/`CacheMaxEntries` in `internal/config` with viper; env override `TOOL_CACHE_TTL`, `TOOL_CACHE_MAX_ENTRIES` | 4.1 |
| Admin response hash | `POST/PATCH /admin/tools` response body MUST include computed `hash` field | 2.7 |
| Boot seed transaction | `InitFromConfig` MUST run all upserts in a **single transaction** (fail-closed) | 2.2, 4.3 |
| Case sensitivity | Registry lookup uses **exact case-sensitive match**; no normalization to lowercase | 1.3, 2.1, 3.1 |

---

## Implementation Order Summary

1. **Phase 1 (Foundation)**: Database migration → Domain entities → SQLC queries → Hash computation with tests. This establishes the data model and core algorithm that everything else depends on.
2. **Phase 2 (Core)**: Repository implementation with cache + audit → Admin API handlers. The repository is the single write path and must emit audit events; admin API exercises the write path.
3. **Phase 3 (Integration)**: Chat handler validation → Usecase wiring → Executor limits. This connects the registry to the request flow and enforces WASM limits.
4. **Phase 4 (Boot/Config)**: Config changes → main.go wiring → Boot seed with single-transaction upserts. Startup sequence must initialize registry before handlers.
5. **Phase 5 (Tests)**: Mettle corpus (4 scenarios) + integration tests for admin CRUD, audit, concurrency. These verify the security properties end-to-end.
6. **Phase 6 (Docs)**: Limitations doc + cleanup.

---

## Next Step

**Ready for implementation (sdd-apply)** — however, per delivery strategy `ask-on-risk` and High 400-line budget risk, the user must confirm the **chained PR strategy (stacked-to-main)** before apply phase begins. The suggested split is:

- **PR 1**: Phase 1 (Foundation) — ~300 lines
- **PR 2**: Phase 2 (Core) — ~400 lines
- **PR 3**: Phase 3 (Integration) — ~350 lines
- **PR 4**: Phase 4-6 (Boot, Tests, Docs) — ~400 lines

Each PR targets `main` with focused test commands and independent rollback boundaries as defined in the Work Units table above.