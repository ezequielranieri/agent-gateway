# Proposal: Tool Registry Hardening

## Intent

The agent-gateway currently accepts arbitrary tool definitions in chat completion requests without validation (chat.go:156-168), creating a tool injection vulnerability. Additionally, WebAssembly fuel and memory limits are defined in configuration but never enforced in the WASM executor, enabling resource exhaustion attacks. This change introduces a persistent tool registry with validation, enforces real WASM resource limits, and creates a mettle test corpus to verify both security properties.

## Scope

### In Scope
- PostgreSQL `tool_definitions` table with schema for name, description, input_schema, grants, fuel_limit, memory_pages, hash, is_active
- Tool registry repository with CRUD operations (SQLC-generated)
- Handler validation: reject requests with unknown tool IDs (not in registry)
- Handler validation: reject requests where tool definition hash mismatches registry (tampered definition)
- WASM executor: apply `WithFuel()` and `WithMemoryPages()` from registry (fallback to ToolConfig defaults)
- Mettle corpus with 4 explicit scenarios (injection, hash mismatch, resource exhaustion, baseline)
- Migration `0018_tool_definitions.up.sql` for schema creation
- Boot-time seed from existing `ToolConfig.Tools` (auto-upsert builtin tools)
- Optional admin API endpoint for manual tool registration (future MCP client integration)

### Out of Scope
- MCP client implementation (Front B) — registry designed to support it
- Dynamic tool loading/unloading at runtime without restart
- Tool versioning/history beyond hash-based tamper detection
- Full admin UI for tool management
- Rate limiting per tool (separate concern)

## Capabilities

### New Capabilities
- `tool-registry`: Persistent tool definitions with hash-based integrity validation
- `wasm-resource-limits`: Enforced WebAssembly fuel and memory limits per tool execution

### Modified Capabilities
- `chat-completions`: Tool validation now enforced at handler boundary
- `tool-execution`: WASM resource limits now enforced at executor boundary

## Approach

**Hybrid Registry + Configuration (Approach C from exploration)**

The solution combines a persistent PostgreSQL registry with the existing file-based `ToolConfig`:

1. **Registry Table** (`tool_definitions`): Canonical source of truth for tool definitions, grants, and resource limits. Includes a `hash` column computed over the complete tool definition (name + description + input_schema JSON) for tamper detection.

2. **Boot Seed**: On application startup, iterate `ToolConfig.Tools` and upsert into registry. Builtin tools are auto-registered; config overrides registry for dynamic limits.

3. **Handler Validation** (`chat.go`): Parse request tools → for each tool, lookup in registry by name → if not found, reject with `ErrToolNotFound`. If found, compute hash of request's tool definition (name + description + parameters) and compare with registry hash → if mismatch, reject with new error `ErrToolDefinitionMismatch`.

4. **Executor Enforcement** (`wazero/executor.go`): Read `fuel_limit` and `memory_pages` from registry entry; call `moduleConfig.WithFuel(fuel_limit).WithMemoryPages(memory_pages)`. Fall back to `ToolConfig.EffectiveFuel()` / `EffectiveMemoryPages()` if registry entry missing or zero.

5. **Admin Flow** (optional, for future MCP): `POST /admin/tools` to register new tool definitions; `PATCH /admin/tools/{name}` to update grants/limits; `DELETE /admin/tools/{name}` to deactivate (`is_active = false`).

### Explicit Hash Definition

The `hash` column in `tool_definitions` is computed as **SHA-256 over canonical JSON** of the complete tool definition:

```
hash = SHA256(canonical_json({
  "name": <string>,
  "description": <string>,
  "parameters": <JSON Schema object>
}))
```

- **Canonical JSON**: Keys sorted lexicographically, no whitespace, deterministic serialization (e.g., `json.Marshal` with sorted keys or `go-json-canonical`).
- **Fields included**: `name` (string), `description` (string, empty string if absent), `parameters` (object, empty object if absent).
- **Algorithm**: SHA-256, encoded as lowercase hex string (64 chars).
- **Reuse in Front B**: This exact hash computation will be reused by the MCP client to detect rug-pulls — if a remote MCP server returns a tool definition whose hash doesn't match the registry, the client rejects it.

### Explicit Registry Write Path

| Path | Mechanism | Trigger | Notes |
|------|-----------|---------|-------|
| **Boot seed** | Auto-upsert from `ToolConfig.Tools[]` | Application startup | For each `ToolModuleConfig`: upsert by `name`; `hash` computed from `FunctionDef` (name, description, parameters). `grants`, `fuel_limit`, `memory_pages` from config. `is_active = true`. |
| **Admin API** | `POST /admin/tools` (create), `PATCH /admin/tools/{name}` (update), `DELETE /admin/tools/{name}` (deactivate) | Manual operator action / future MCP client | Requires admin role. Write recomputes hash from provided definition. Update preserves hash unless definition fields change. |

**Mettle corpus relevance**: The "valid tool ID but tampered definition (hash mismatch)" scenario requires the registry to contain a tool with a known hash. The boot seed ensures builtin tools have hashes; the admin API allows adding tools with hashes. Without a write path, the registry would be empty and the hash-mismatch scenario couldn't be tested.

### Explicit Mettle Corpus Scenarios

The mettle corpus (`test/mettle/tool_registry/`) will contain these 4 scenarios:

| # | Scenario | Input | Expected Result | Purpose |
|---|----------|-------|-----------------|---------|
| 1 | **Tool injection without registry** | Request with tool `{"name": "evil_tool", "description": "...", "parameters": {...}}` where `evil_tool` not in registry | HTTP 400, error `tool not found` | Validates registry boundary enforcement |
| 2 | **Tool ID exists but hash mismatch** | Request with tool `{"name": "legit_tool", "description": "modified", "parameters": {...}}` where `legit_tool` exists in registry but hash differs | HTTP 400, error `tool definition mismatch` | Validates tamper detection (rug-pull protection) |
| 3 | **WASM execution without fuel/memory limits** | Valid tool + valid hash, but executor called without `WithFuel`/`WithMemoryPages` (simulated via mock or config override) | Execution fails with `ErrToolResourceExhausted` or panics (detected by test) | Validates real limits enforcement |
| 4 | **Valid tool + valid hash + enforced limits** | Request with registered tool, correct hash, executor configured with limits | HTTP 200, successful execution within limits | Baseline — confirms legitimate flow works |

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `migrations/0018_tool_definitions.up.sql` | New | Create `tool_definitions` table with columns: `id`, `name`, `description`, `input_schema` (JSONB), `grants` (JSONB), `fuel_limit` (BIGINT), `memory_pages` (INT), `hash` (CHAR(64)), `is_active` (BOOL), `created_at`, `updated_at` |
| `internal/domain/tool/registry.go` | New | Domain entity `ToolDefinition` + repository interface |
| `internal/adapter/postgres/tool_repository.go` | New | SQLC-generated repository implementation |
| `internal/api/handlers/chat.go` | Modified | Add tool validation logic in `convertToUsecaseRequest`; compute hash, compare with registry |
| `internal/usecase/chat/router.go` | Modified | Inject tool registry; pass validated tool refs to executor |
| `internal/adapter/tool/wazero/executor.go` | Modified | Apply `WithFuel()` and `WithMemoryPages()` from registry entry |
| `internal/domain/tool/errors.go` | Modified | Add `ErrToolDefinitionMismatch` sentinel error |
| `internal/config/config.go` | Modified | Boot seed logic in `Load()` or separate `InitToolRegistry()` function |
| `cmd/gateway/main.go` | Modified | Initialize tool registry after DB pool, before handlers |
| `test/mettle/tool_registry/` | New | Mettle corpus with 4 scenarios above |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| Migration breaks existing deployments | Medium | Run migration in transaction; seed from config ensures builtin tools present immediately |
| Performance: DB query per tool validation | Low | In-memory cache (LRU, TTL 5m) in registry; invalidate on admin writes |
| Hash collision (SHA-256) | Negligible | SHA-256 collision resistance is cryptographically sound; not a practical concern |
| Backwards compatibility: unknown tools rejected | High | Document breaking change; provide migration guide; seed builtin tools at boot |
| Admin API auth bypass | Low | Reuse existing admin middleware; require super-admin role |
| Fuel/memory limits too restrictive | Medium | Configurable per-tool via registry; default to generous values (10M fuel, 512 pages) |
| Registry write race at boot | Low | Single-threaded startup; upsert with `ON CONFLICT DO UPDATE` |

## Rollback Plan

1. **Code rollback**: Revert `chat.go`, `executor.go`, `main.go`, `config.go` changes — handler returns to unvalidated unmarshal, executor removes fuel/memory config.
2. **Database rollback**: Run `0018_tool_definitions.down.sql` (DROP TABLE `tool_definitions`).
3. **Config rollback**: No config changes required (existing `ToolConfig` unchanged).
4. **Verification**: Run integration tests — chat completions with arbitrary tools should work (pre-hardening behavior).

## Dependencies

- `github.com/jackc/pgx/v5` (already used)
- `sqlc` for repository generation (already in project)
- `golang.org/x/crypto/sha256` (stdlib) for hash computation
- No new external dependencies

## Success Criteria

- [ ] Request with unknown tool ID returns 400 "tool not found"
- [ ] Request with known tool ID but modified definition returns 400 "tool definition mismatch"
- [ ] WASM execution respects fuel limit (exceeding fuel returns `ErrToolResourceExhausted`)
- [ ] WASM execution respects memory limit (exceeding memory returns `ErrToolResourceExhausted`)
- [ ] Valid request with registered tool, correct hash, and limits executes successfully (HTTP 200)
- [ ] Boot seed populates registry from `ToolConfig.Tools` on startup
- [ ] Admin API can register/update/deactivate tools (manual test)
- [ ] Mettle corpus passes all 4 scenarios
- [ ] No regression in existing chat completion flows (integration tests pass)

## Open Questions

None — all decisions resolved in exploration. Approach C (Hybrid) is confirmed.

---

**Next Step**: Proceed to `sdd-spec` phase to write delta specs for `tool-registry` and `wasm-resource-limits` capabilities.