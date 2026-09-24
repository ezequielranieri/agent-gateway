# Design: Tool Registry Hardening

## Technical Approach

This design implements the **Hybrid Registry + Configuration (Approach C)** from the proposal, adding two critical hardening capabilities:

1. **`tool-registry`**: Persistent PostgreSQL tool definitions with SHA-256 hash-based integrity validation at the handler boundary
2. **`wasm-resource-limits`**: Enforced WebAssembly fuel and memory limits per tool execution at the executor boundary

The design integrates with existing patterns:
- **Audit logging**: Uses the existing hash-chained `AuditRepository` and `AuditEvent` domain model
- **Redis**: Leverages existing Redis client for potential distributed cache invalidation
- **Configuration**: Extends `ToolConfig` for boot seeding and fallback defaults
- **Middleware**: Follows the existing admin handler patterns with super-admin role requirement

---

## Architecture Decisions

### Decision: Audit Log Integration for Admin API Mutations

**Choice**: Emit audit events directly from the tool registry repository layer on every write (CREATE/UPDATE/DELETE), using the existing `AuditRepository.Append()` with a dedicated `tool_definition` entity type and structured payload containing old/new hashes.

**Alternatives considered**:
1. **Middleware-based audit**: Wrap admin handlers with audit middleware (like HTTP request audit)
   - *Rejected*: Tool registry writes may happen outside HTTP context (e.g., boot seed, background jobs). Repository-layer emission captures all write paths.
2. **Application-level service audit**: Create a `ToolRegistryService` that orchestrates repository + audit
   - *Rejected*: Adds unnecessary indirection; repository is already the single write path. The existing `AuditRepository.Append()` is designed for direct use.

**Rationale**: The hash-chained audit log is the governance backbone. Every `tool_definition` mutation must be captured with actor, timestamp, and diff (old hash → new hash). Repository-layer emission ensures completeness regardless of invocation path (admin API, boot seed, future MCP client).

**Audit Event Structure**:
```go
// Payload for tool_definition audit events
type ToolDefinitionAuditPayload struct {
    Operation    string          `json:"operation"`      // "CREATE" | "UPDATE" | "DELETE"
    ToolName     string          `json:"tool_name"`
    OldHash      *string         `json:"old_hash,omitempty"`      // nil for CREATE
    NewHash      *string         `json:"new_hash,omitempty"`      // nil for DELETE
    ChangedFields []string       `json:"changed_fields,omitempty"` // for UPDATE: which fields changed
    FullDefinition json.RawMessage `json:"full_definition,omitempty"` // for CREATE/UPDATE: full tool def
}
```

**Emission Points**:
| Operation | Repository Method | Audit Action | Severity |
|-----------|------------------|--------------|----------|
| Boot seed upsert | `UpsertToolDefinition` | `tool_definition.upsert` | `info` |
| Admin create | `CreateToolDefinition` | `tool_definition.create` | `info` |
| Admin update (grants/limits) | `UpdateToolDefinition` | `tool_definition.update` | `warn` |
| Admin update (definition fields → hash change) | `UpdateToolDefinition` | `tool_definition.update` | `critical` |
| Admin deactivate | `DeactivateToolDefinition` | `tool_definition.delete` | `warn` |

**Actor Context**: Admin API mutations carry the authenticated super-admin user ID. Boot seed runs as `system` actor (no user ID).

**Concurrency Control**: Chain appends are serialized per-tenant using PostgreSQL advisory locks (`pg_try_advisory_xact_lock`). This eliminates the need for retry loops on unique constraint violations — the lock is held for the entire transaction duration and released automatically on commit/rollback. This ensures:
- No two concurrent transactions can append to the same tenant's chain simultaneously
- The chain sequence (`seq`) is always consistent without retries
- The unique constraint on `(tenant_id, seq)` serves as a safety net, not the primary synchronization mechanism

---

### Decision: LRU Cache Invalidation Strategy (5-min TTL)

**Choice**: **Local per-instance LRU cache with 5-min TTL, documented as known limitation for multi-instance deployments**. Cache invalidation on local writes only. Other instances serve stale data for up to 5 minutes.

**Alternatives considered**:
1. **Distributed invalidation via Redis pub/sub**
   - *Pros*: Strong consistency across instances
   - *Cons*: Adds complexity (pub/sub protocol, connection management, failure handling). Redis already used for rate limiting; adding pub/sub increases operational surface.
   - *Deferred*: Implement when multi-instance deployment is confirmed.
2. **Lower TTL to 30 seconds**
   - *Pros*: Reduces staleness window
   - *Cons*: Increases DB load 10x. 5-min TTL was explicitly specified in spec for performance reasons.
3. **Write-through cache with synchronous invalidation**
   - *Pros*: Strong consistency
   - *Cons*: Adds latency to writes; requires distributed lock or consensus.

**Rationale**:
- Current architecture: Single-instance deployment (per `main.go` initialization pattern)
- Project convention: "Document what's not covered" — see `README.md` pattern
- 5-min TTL is a deliberate performance/consistency tradeoff from the spec
- Redis pub/sub invalidation is straightforward to add later without API changes

**Implementation**:
```go
// Local cache in ToolRepository implementation
type ToolRepository struct {
    queries *sqlc.Queries
    pool    *pgxpool.Pool
    cache   *lru.Cache[string, *domain.ToolDefinition] // LRU with TTL
    mu      sync.RWMutex
}

// Cache key: tool name (lowercase)
// TTL: 5 minutes (configurable via ToolConfig.CacheTTL)
// Max entries: 1000 (configurable via ToolConfig.CacheMaxEntries)
```

**Invalidation**:
- On `CreateToolDefinition`, `UpdateToolDefinition`, `DeactivateToolDefinition`: remove key from local cache
- On boot seed: clear entire cache after bulk upsert
- TTL expiration: automatic via LRU library

**Documentation**: Add `docs/limitations.md` entry:
> **Tool Registry Cache Staleness (Multi-Instance)**
> 
> The tool registry uses a local in-memory LRU cache with 5-minute TTL per instance. In multi-instance deployments, a write to the registry (admin API, boot seed) invalidates the cache only on the instance receiving the write. Other instances continue serving cached data for up to 5 minutes.
> 
> **Impact**: A tool definition update may not be visible to all instances for up to 5 minutes.
> 
> **Mitigation**: For strong consistency requirements, deploy single-instance or implement Redis pub/sub invalidation (tracked in issue #XXX).

---

### Decision: Hash Computation Canonicalization (RFC 8785 / JCS)

**Choice**: Use `github.com/gowebpki/jcs` for RFC 8785 (JCS) canonical JSON serialization.

**Alternatives considered**:
1. **`github.com/alecthomas/go-json-canonical`**: Originally proposed
   - *Rejected*: Package does not exist on GitHub; not a valid import
2. **`github.com/gibson042/canonicaljson-go`**: Implements JCS but listed by RFC 8785 as a distinct effort, not a reference implementation
   - *Rejected*: RFC 8785 lists it as a separate canonicalization effort; potential differences in number formatting (e.g., `1.0` vs `1`, `1e3` vs `1000`) and escape handling
3. **Manual canonicalization**: Sort keys recursively before `json.Marshal`
   - *Rejected*: Error-prone; edge cases with nested objects, arrays, numbers

**Rationale**: The proposal requires "canonical JSON: keys sorted lexicographically, no whitespace, deterministic serialization." `gowebpki/jcs` is the Go reference implementation for RFC 8785 (JSON Canonicalization Scheme), ensuring exact compliance with the standard. This is critical for Front B MCP client hash verification where both sides must compute identical hashes.

**Verification**: Added **golden tests** (`TestComputeHash_Golden`) with hardcoded expected hashes for:
- Floating-point numbers (`18.0` vs `18`)
- Scientific notation (`1e3`)
- Unicode (emoji `👋🌍`, Arabic `مرحبا`)
- Trailing zeros in decimals (`0.01`)

Added **RFC 8785 reference vector test** (`TestComputeHash_RFC8785Reference`) verifying the canonicalization library implements the standard correctly:
- Number serialization: `[333333333.33333329, 1E30, 4.50, 2e-3, 0.000000000000000000000000001]` → `[333333333.3333333,1e+30,4.5,0.002,1e-27]` (per RFC 8785 Section 3.2.2)
- Equivalent number representations produce identical hashes
- Non-equivalent numbers produce different hashes
- Negative zero (`-0.0`) canonicalizes to same as positive zero (`0.0`)

This test is independent of our golden hashes — it validates the library against the RFC itself.

These tests act as a canary: if the canonicalization library or its behavior changes, CI fails immediately instead of silently invalidating all persisted tool definition hashes.

**Deviation from original design**: Original design specified `alecthomas/go-json-canonical` (non-existent package). This design documents the actual choice (`gowebpki/jcs`) and the verification strategy (golden tests).

---

### Decision: Tool Definition Entity and Repository Interface

**Choice**: Define `ToolDefinition` in `internal/domain/tool/registry.go` with repository interface in same package. SQLC-generated implementation in `internal/adapter/postgres/tool_repository.go`.

**Rationale**: Follows existing domain/adapter separation (see `internal/domain/user.go` + `internal/adapter/postgres/user_repository.go`). The domain owns the entity and interface; the adapter owns the SQLC implementation.

---
### Decision: Tool Definitions are Tenanted (Multi-Tenant Registry)

**Choice**: `tool_definitions` table includes `tenant_id` in the primary key and unique index `(tenant_id, name) WHERE is_active = true`. All repository methods require a `tenantID` parameter. RLS FORCE is enabled with policy `tenant_id = current_setting('app.current_tenant')`.

**Alternatives considered**:
1. **Global tools (no tenant_id)**: Single registry shared by all tenants
   - *Rejected*: Inconsistent with project's multi-tenant architecture; all other domain tables are tenanted. Super-admin would need a separate "global" tenant concept.
2. **Global tools with separate super-admin namespace**: Tools live in a special "system" tenant
   - *Rejected*: Adds complexity; super-admin can already manage any tenant's tools by specifying `tenant_id` in admin API requests.

**Rationale**:
- Consistent with project's existing RLS pattern (all domain tables are tenanted)
- Super-admin tokens have no `tenant_id`; admin handlers accept optional `tenant_id` in request body for super-admin to target any tenant
- Chat handler extracts `tenant_id` from user's JWT token and passes it to `ToolRepository.GetByName`
- Boot seed (`InitFromConfig`) requires a target `tenant_id` — seeded per-tenant at creation time or via admin API
- Unique index `(tenant_id, name)` is case-sensitive (no `lower()`) to match strict case-sensitive lookup requirement

**Implementation impact**:
- `ToolDefinition` entity includes `TenantID domain.UUID`
- `ToolRepository` interface methods all take `tenantID domain.UUID` parameter
- SQLC queries include `tenant_id` in WHERE clauses
- Admin API: super-admin specifies `tenant_id` in request body; regular admin (if any) uses token's tenant
- Chat handler: gets `tenant_id` from middleware context

---

### Decision: Admin API Endpoint Structure

**Choice**: New handler `AdminToolsHandler` with routes under `/admin/tools`:
- `POST /admin/tools` — Create (requires super-admin)
- `PATCH /admin/tools/{name}` — Update grants, limits, or definition
- `DELETE /admin/tools/{name}` — Deactivate (soft delete)

**Rationale**: Consistent with existing admin handlers (`AdminAuditHandlers`, `AdminTenantsHandler`, etc.). Uses existing `middleware.NewAuth` with super-admin role check.

---
### Decision: Registry-to-Module Binding

**Choice**: The tool registry acts as an **allowlist with limits**. A registry entry for tool name `X` enables execution of the WASM module named `X` (loaded at boot from `ToolConfig.Tools`). The registry does **not** store the module binary — it only stores the definition, grants, and resource limits. The module binary is loaded once at startup from `ToolConfig.Tools[]` by matching `ToolModuleConfig.Name` to the registry entry name.

**Rationale**:
- Consistent with existing boot seed pattern: `ToolConfig.Tools` defines available modules; registry adds persistence, hash validation, and per-tenant limits
- Simplifies MCP integration: future MCP client registers tool definition in registry; the module binary is already loaded if the name matches
- Separation of concerns: registry = governance (what is allowed, with what limits); config = module loading (what code runs)

**Implications**:
- If a tool is registered in the registry but no module with that name exists in `ToolConfig.Tools`, execution will fail with "module not found"
- If a module exists in config but is not in the registry (or inactive), it cannot be invoked via chat completions
- This is intentional: registry is the source of truth for "what may execute"; config is the source of truth for "what code exists"

---

## Data Flow

### Chat Completion Request Flow (Tool Validation)

```
HTTP Request
    │
    ▼
┌─────────────────────────────────────┐
│ ChatHandlers.ChatCompletions        │
│ 1. Parse req.Tools (json.RawMessage)│
│ 2. For each tool:                   │
│    a. Lookup in ToolRegistry        │
│       ├─ Cache hit → return cached  │
│       └─ Cache miss → DB query      │
│    b. If not found → 400 tool_not_found
│    c. Compute hash of request tool  │
│    d. Compare with registry hash    │
│       └─ Mismatch → 400 tool_definition_mismatch
│ 3. Build validated tool refs        │
└─────────────────────────────────────┘
    │
    ▼
┌─────────────────────────────────────┐
│ ChatUsecase.Complete                │
│ Pass validated tool refs to router  │
└─────────────────────────────────────┘
    │
    ▼
┌─────────────────────────────────────┐
│ ToolExecutor.Execute                │
│ 1. Get tool config (grants, limits) │
│ 2. Apply WithFuel/WithMemoryPages   │
│ 3. Execute WASM module              │
└─────────────────────────────────────┘
```

### Admin Tool Registration Flow

```
HTTP Request (super-admin)
    │
    ▼
┌─────────────────────────────────────┐
│ AdminToolsHandler                   │
│ 1. Parse request body               │
│ 2. Validate (name, description,     │
│    parameters, grants, limits)      │
│ 3. Compute hash from definition     │
└─────────────────────────────────────┘
    │
    ▼
┌─────────────────────────────────────┐
│ ToolRepository.Create/Update/Delete │
│ 1. Execute DB write (upsert/update) │
│ 2. Invalidate local cache entry     │
│ 3. Emit AuditEvent via AuditRepo    │
└─────────────────────────────────────┘
    │
    ▼
HTTP 201/200/204
```

### Boot Seed Flow (Startup)

```
main.go startup
    │
    ▼
┌─────────────────────────────────────┐
│ config.Load() → ToolConfig populated│
└─────────────────────────────────────┘
    │
    ▼
┌─────────────────────────────────────┐
│ ToolRegistry.InitFromConfig(cfg)    │
│ For each ToolModuleConfig:          │
│  1. Compute hash from FunctionDef   │
│  2. Upsert into tool_definitions    │
│  3. Clear local cache after all     │
└─────────────────────────────────────┘
    │
    ▼
Handler initialization continues
```

---

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `migrations/0018_tool_definitions.up.sql` | Create | PostgreSQL table with columns: id, name, description, input_schema (JSONB), grants (JSONB), fuel_limit (BIGINT), memory_pages (INT), hash (CHAR(64)), is_active (BOOL), created_at, updated_at. Unique index on name. RLS enabled. |
| `migrations/0018_tool_definitions.down.sql` | Create | DROP TABLE tool_definitions |
| `internal/domain/tool/registry.go` | Create | Domain entity `ToolDefinition`, repository interface `ToolRepository`, sentinel errors `ErrToolNotFound`, `ErrToolDefinitionMismatch` |
| `internal/domain/tool/errors.go` | Modify | Add `ErrToolDefinitionMismatch` (already added in spec) |
| `internal/adapter/postgres/tool_repository.go` | Create | SQLC-generated repository implementation with LRU cache, audit event emission on writes |
| `internal/adapter/postgres/sqlc/tool.sql` | Create | SQLC queries for tool_definitions CRUD |
| `internal/api/handlers/chat.go` | Modify | Add tool validation in `convertToUsecaseRequest`: lookup each tool, compute hash, compare, reject on mismatch |
| `internal/api/handlers/admin_tools.go` | Create | Admin handlers for tool registry: POST/PATCH/DELETE /admin/tools |
| `internal/usecase/chat/router.go` | Modify | Inject `ToolRepository` into chat usecase; pass validated tool definitions (with grants/limits) to executor |
| `internal/usecase/chat/orchestrator.go` | Modify | Accept validated tools from router; pass to executor |
| `internal/adapter/tool/wazero/executor.go` | Modify | Read `fuel_limit` and `memory_pages` from validated tool; call `WithFuel()`/`WithMemoryPages()`; fail closed if both zero |
| `internal/domain/tool/config.go` | Modify | Add `CacheTTL` and `CacheMaxEntries` fields; ensure `EffectiveFuel`/`EffectiveMemoryPages` used as fallback |
| `internal/config/config.go` | Modify | Add `InitToolRegistry(dbPool, auditRepo, cfg)` call after DB pool creation |
| `cmd/gateway/main.go` | Modify | Initialize tool repository after DB pool, before handlers; pass to chat usecase builder |
| `test/mettle/tool_registry/` | Create | Mettle corpus with 4 scenarios per spec |
| `docs/limitations.md` | Modify | Add cache staleness limitation entry |

---

## Interfaces / Contracts

### Domain Entity: ToolDefinition

```go
// internal/domain/tool/registry.go
package tool

import (
    "time"
    "github.com/ezequielranieri/agent-gateway/internal/domain"
)

type ToolDefinition struct {
    ID           domain.UUID
    Name         string
    Description  string
    InputSchema  json.RawMessage  // JSON Schema object
    Grants       json.RawMessage  // Array of grant strings
    FuelLimit    uint64           // WASM fuel units
    MemoryPages  uint32           // WASM memory pages (64KB each)
    Hash         string           // SHA-256 hex (64 chars)
    IsActive     bool
    CreatedAt    time.Time
    UpdatedAt    time.Time
}

// ComputeHash computes SHA-256 over canonical JSON of name, description, parameters
func ComputeHash(name, description string, parameters json.RawMessage) string
```

### Repository Interface

```go
// internal/domain/tool/registry.go
type ToolRepository interface {
    // GetByName returns tool definition by name (only active tools)
    GetByName(ctx context.Context, name string) (*ToolDefinition, error)

    // CreateToolDefinition inserts a new tool definition
    CreateToolDefinition(ctx context.Context, def *ToolDefinition) error

    // UpdateToolDefinition updates grants, limits, or definition fields
    // Returns true if hash was recomputed (definition fields changed)
    UpdateToolDefinition(ctx context.Context, def *ToolDefinition) (hashChanged bool, err error)

    // DeactivateToolDefinition soft-deletes a tool (is_active = false)
    DeactivateToolDefinition(ctx context.Context, name string) error

    // UpsertToolDefinition inserts or updates (used by boot seed)
    UpsertToolDefinition(ctx context.Context, def *ToolDefinition) error

    // InitFromConfig boot-seeds from ToolConfig.Tools
    InitFromConfig(ctx context.Context, cfg *ToolConfig) error
}
```

### Admin API Request/Response

```go
// internal/api/handlers/admin_tools.go

// CreateToolRequest
type CreateToolRequest struct {
    Name        string          `json:"name" validate:"required"`
    Description string          `json:"description"`
    Parameters  json.RawMessage `json:"parameters" validate:"required"`
    Grants      json.RawMessage `json:"grants,omitempty"`      // array of strings
    FuelLimit   *uint64         `json:"fuel_limit,omitempty"`
    MemoryPages *uint32         `json:"memory_pages,omitempty"`
}

// UpdateToolRequest (all fields optional)
type UpdateToolRequest struct {
    Description *string          `json:"description,omitempty"`
    Parameters  *json.RawMessage `json:"parameters,omitempty"`
    Grants      *json.RawMessage `json:"grants,omitempty"`
    FuelLimit   *uint64          `json:"fuel_limit,omitempty"`
    MemoryPages *uint32          `json:"memory_pages,omitempty"`
    IsActive    *bool            `json:"is_active,omitempty"`
}

// ToolResponse (returned by all endpoints)
type ToolResponse struct {
    ID           string          `json:"id"`
    Name         string          `json:"name"`
    Description  string          `json:"description"`
    Parameters   json.RawMessage `json:"parameters"`
    Grants       json.RawMessage `json:"grants"`
    FuelLimit    uint64          `json:"fuel_limit"`
    MemoryPages  uint32          `json:"memory_pages"`
    Hash         string          `json:"hash"`
    IsActive     bool            `json:"is_active"`
    CreatedAt    time.Time       `json:"created_at"`
    UpdatedAt    time.Time       `json:"updated_at"`
}
```

### Audit Event Payload for Tool Definitions

```go
// internal/adapter/postgres/tool_repository.go (internal to repo)

type toolDefinitionAuditPayload struct {
    Operation      string          `json:"operation"`       // CREATE, UPDATE, DELETE
    ToolName       string          `json:"tool_name"`
    OldHash        *string         `json:"old_hash,omitempty"`
    NewHash        *string         `json:"new_hash,omitempty"`
    ChangedFields  []string        `json:"changed_fields,omitempty"`
    FullDefinition json.RawMessage `json:"full_definition,omitempty"`
}
```

---

## Testing Strategy

| Layer | What to Test | Approach |
|-------|-------------|----------|
| **Unit** | `ComputeHash` canonicalization | Table-driven tests: identical defs → same hash; field changes → different hash; key reordering → same hash |
| **Unit** | ToolRepository cache behavior | Mock DB; verify cache hit/miss, TTL expiry, invalidation on write |
| **Unit** | AdminToolsHandler validation | Test request parsing, validation rules, error codes |
| **Unit** | WasmExecutor fuel/memory config | Mock wazero; verify `WithFuel`/`WithMemoryPages` called with correct values |
| **Integration** | Tool validation in chat handler | Spin up test DB; register tool; send chat request with valid/invalid/tampered tools; assert HTTP codes |
| **Integration** | Admin API CRUD | Register tool via POST; verify in DB; update via PATCH; verify hash recomputed; deactivate via DELETE; verify lookup fails |
| **Integration** | Boot seed idempotency | Start app with config → verify tools in DB; restart → verify no duplicates, updated_at refreshed |
| **Integration** | Audit events on writes | After each admin write, query audit log; verify event exists with correct payload (operation, old/new hash) |
| **E2E (Mettle)** | Scenario 1: Tool injection | Request with unknown tool → 400 tool_not_found |
| **E2E (Mettle)** | Scenario 2: Hash mismatch | Request with known tool but modified def → 400 tool_definition_mismatch |
| **E2E (Mettle)** | Scenario 3: Resource exhaustion | Tool with low fuel → ErrToolResourceExhausted |
| **E2E (Mettle)** | Scenario 4: Valid flow | Registered tool, correct hash, adequate limits → 200 OK |

---

## Threat Matrix

N/A — no routing, shell, subprocess, VCS/PR automation, executable-file classification, or process-integration boundary.

---

## Migration / Rollout

### Database Migration

1. Apply `0018_tool_definitions.up.sql` (creates table, indexes, RLS policies)
2. Application startup runs boot seed automatically — no manual data migration needed
3. Builtin tools from `ToolConfig.Tools` are upserted on first startup

### Rollback Plan

1. **Code rollback**: Revert `chat.go`, `executor.go`, `main.go`, `config.go`, `router.go`, `orchestrator.go` changes
2. **Database rollback**: Run `0018_tool_definitions.down.sql` (DROP TABLE)
3. **Config rollback**: No config changes required (existing `ToolConfig` unchanged)
4. **Verification**: Run integration tests — chat completions with arbitrary tools should work (pre-hardening behavior)

### Feature Flags

None required. The hardening is always-on once deployed. Boot seed ensures builtin tools are immediately available.

---

## Open Questions

- [ ] **Cache TTL configuration**: Should `CacheTTL` and `CacheMaxEntries` be in `ToolConfig` or a separate `ToolRegistryConfig`? (Currently in `ToolConfig` per spec)
- [ ] **Admin API response on hash change**: Should `PATCH /admin/tools/{name}` return the new hash in response body when definition fields change? (Design assumes yes — included in `ToolResponse`)
- [ ] **Boot seed transaction**: Should boot seed run in a single transaction? (Current design: individual upserts with `ON CONFLICT`; acceptable since startup is single-threaded)
- [ ] **Tool name case sensitivity**: Registry uses exact match (case-sensitive). Should we normalize to lowercase? (Current design: exact match; config tools must match request tool names exactly)

---

## Next Step

Ready for tasks (sdd-tasks).