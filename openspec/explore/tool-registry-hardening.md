# Exploration: tool-registry-hardening

## Current State

The agent-gateway currently has several security and architectural gaps in its tool execution flow:

### 1. Unvalidated tool unmarshal in `chat.go:156-168`

The handler `convertToUsecaseRequest` unconditionally unmarshals `req.Tools` without any validation:

```go
var tools []model.Tool
if len(req.Tools) > 0 {
    if err := json.Unmarshal(req.Tools, &tools); err != nil {
        h.logger.Warn().Err(err).Msg("Failed to parse tools")
    }
}
```

**Risks:**
- Any JSON can be passed as tools — no sandboxing or validation
- No check that tools are registered/known to the system
- Potential for tool injection attacks where arbitrary functions are executed
- No audit trail of which tools were requested vs. which were allowed

### 2. WASM fuel/memory limits defined but not enforced

`internal/domain/tool/config.go` defines default and per-tool fuel/memory limits:

- `DefaultFuel`: 10_000_000 WebAssembly instructions
- `DefaultMemoryPages`: 512 (32MB)
- `ToolLimits.Fuel` and `ToolLimits.MemoryPages` for per-tool overrides

However, `internal/adapter/tool/wazero/executor.go` never applies these limits:

```go
// Current — no fuel or memory limits set
moduleConfig := wazero.NewModuleConfig().
    WithName(module.config.Name).
    WithSysNanotime().
    WithSysNanosleep()
```

**Risks:**
- Tools can run unbounded WebAssembly instructions
- No memory protection between tool executions
- Potential for resource exhaustion attacks
- The `EffectiveFuel()` and `EffectiveMemoryPages()` helper functions exist but are unused

### 3. No persistent tool registry

Tools are configured at startup only via `ToolConfig.Tools` slice. There is no:
- Database schema for tool definitions
- Repository layer for CRUD operations
- API to query/register tools dynamically

### 4. Domain model

Existing tool types:
- `internal/domain/model.Tool` — represents a tool function definition (type + FunctionDef)
- `internal/domain/tool.ToolConfig` — startup configuration with `[]ToolModuleConfig`
- `internal/domain/tool.ToolModuleConfig` — individual tool: name, module_path, grants, limits, requires_approval
- No tool registry entity or persistence layer

## Affected Areas

| File | Why Affected |
|------|-------------|
| `internal/api/handlers/chat.go` | Unvalidated tool unmarshal — needs registry check |
| `internal/domain/tool/config.go` | Fuel/memory limits — need to apply in executor |
| `internal/adapter/tool/wazero/executor.go` | WASM module config — need `WithFuel()`/`WithMemoryPages()` |
| `internal/domain/tool/config.go` | `EffectiveFuel()`/`EffectiveMemoryPages()` — already exist |
| `internal/usecase/chat/router.go` | Provider registry pattern — can extend for tools |
| `migrations/` | Schema changes for tool registry persistence |
| `internal/config/config.go` | Tool config koanf tags — may need extension |

## Approaches

### Approach A: In-Memory Registry + Validation (lowest effort)

- Add an in-memory tool registry to the usecase/handler
- Validate tools against registry before passing to usecase
- Apply fuel/memory limits in the WASM executor
- **Pros**: Quick to implement, no schema changes
- **Cons**: No persistence, tools must be re-configured on restart, limited audit capability

### Approach B: PostgreSQL Tool Registry (medium effort)

- Create migration for `tool_registry` table (name, function_def, fuel_limit, memory_pages, grants, is_active)
- Add repository layer for CRUD
- Handler validates tools against registry
- WASM executor reads limits from registry
- **Pros**: Persistent, queryable, audit-enabled, survives restarts
- **Cons**: Schema migration, repository code, API surface

### Approach C: Hybrid — Registry + Configuration (recommended)

- Persist core tool definitions in PostgreSQL for audit/survival
- Keep runtime configuration via `ToolConfig` for dynamic overrides
- Handler validates tools exist in registry; falls back to configured defaults
- WASM executor: first check registry limits, then fall back to `ToolConfig` defaults
- **Pros**: Best of both worlds, gradual migration path, backwards compatible
- **Cons**: More complex initial implementation

## Recommendation

**Approach C: Hybrid — Registry + Configuration**

Implement a persistent tool registry in PostgreSQL alongside the existing `ToolConfig`. The flow would be:

1. **Handler** (`chat.go`): Parse tools from request, validate each exists in the registry, reject unknown tools
2. **Usecase**: Pass validated tool references (names/IDs) to the tool executor
3. **Tool Executor** (`wazero/executor.go`): Read fuel/memory limits from registry, fall back to `ToolConfig` defaults
4. **Repository**: SQLC-generated queries for tool CRUD
5. **Migration**: `0018_tools.up.sql` creating the tool registry table

This approach provides:
- Security: Unknown tools are rejected at the boundary
- Persistence: Tool definitions survive restarts
- Flexibility: Runtime config still supports dynamic overrides
- Audit: Full history of registered tools in the database

## Risks

- **Backwards compatibility**: Requests with tools not in the registry will be rejected — need migration plan
- **Performance**: Additional DB query per request — can be cached
- **Test gaps**: No existing tests for tool registry or WASM limits enforcement
- **Operator experience**: Operators need way to add/remove tools without code changes

## Ready for Proposal

**Yes**. The exploration is complete and ready for the proposal phase. The recommended approach is Approach C (Hybrid Registry + Configuration), which balances security, persistence, and operational practicality.

Next step: Create the SDD proposal with detailed design, or proceed to the design phase if the orchestrator directs.