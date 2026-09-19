# Tool Registry Specification

## Purpose

This specification defines the persistent tool registry with hash-based integrity validation. The registry serves as the canonical source of truth for tool definitions, grants, and resource limits, enabling detection of tool injection attacks and tampered tool definitions (rug-pull protection).

## Requirements

### Requirement: Tool Definition Persistence

The system MUST persist tool definitions in a PostgreSQL table `tool_definitions` with the following schema:

| Column | Type | Constraints | Description |
|--------|------|-------------|-------------|
| `id` | BIGSERIAL | NOT NULL | Surrogate key (part of composite PK) |
| `tenant_id` | UUID | NOT NULL, REFERENCES tenants(id) ON DELETE CASCADE | Tenant identifier (part of composite PK) |
| `name` | VARCHAR(255) | NOT NULL | Tool name (unique per tenant when active) |
| `description` | TEXT | NOT NULL DEFAULT '' | Human-readable description |
| `input_schema` | JSONB | NOT NULL DEFAULT '{}' | JSON Schema for tool parameters |
| `grants` | JSONB | NOT NULL DEFAULT '[]' | Array of grant strings (e.g., `["filesystem:read", "network:egress"]`) |
| `fuel_limit` | BIGINT | NOT NULL DEFAULT 10000000 | Maximum WASM fuel units per execution |
| `memory_pages` | INT | NOT NULL DEFAULT 512 | Maximum WASM memory pages (64KB each) |
| `hash` | CHAR(64) | NOT NULL | SHA-256 hash of canonical tool definition |
| `is_active` | BOOLEAN | NOT NULL DEFAULT true | Soft delete flag |
| `created_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() | Creation timestamp |
| `updated_at` | TIMESTAMPTZ | NOT NULL DEFAULT now() | Last update timestamp |

The system SHALL create a composite primary key on `(id, tenant_id)` and a unique index on `(tenant_id, name) WHERE is_active = true` to enforce tool name uniqueness per tenant. Row Level Security (RLS) FORCE SHALL be enabled with policy `tenant_id = current_setting('app.current_tenant')`.

#### Scenario: Table creation via migration

- GIVEN a fresh database
- WHEN migration `0018_tool_definitions.up.sql` is applied
- THEN the `tool_definitions` table exists with all columns, constraints, and indexes defined above (composite PK on `id, tenant_id`, unique index on `tenant_id, name` WHERE `is_active = true`, RLS FORCE enabled)
- AND the table is empty

---

### Requirement: Hash Computation

The system MUST compute a SHA-256 hash over the canonical JSON representation of the tool definition consisting of three fields: `name` (string), `description` (string, empty if absent), and `parameters` (object, empty object if absent).

The canonical JSON MUST:
- Sort keys lexicographically
- Omit whitespace
- Use deterministic serialization (e.g., `json.Marshal` with sorted keys or `go-json-canonical`)

The hash MUST be encoded as a lowercase hexadecimal string (64 characters).

The system SHALL store this hash in the `hash` column of `tool_definitions`.

#### Scenario: Hash computed from tool definition

- GIVEN a tool definition with `name: "read_file"`, `description: "Read a file"`, `parameters: {"type": "object", "properties": {"path": {"type": "string"}}}`
- WHEN the system computes the canonical JSON and SHA-256 hash
- THEN the hash is a 64-character lowercase hex string
- AND the same tool definition always produces the same hash
- AND any modification to name, description, or parameters produces a different hash

#### Scenario: Hash detects parameter reordering

- GIVEN two tool definitions with identical parameters but different key order in JSON Schema
- WHEN the system computes hashes for both
- THEN both hashes are identical (canonicalization sorts keys)

---

### Requirement: Boot Seed from Configuration

The system MUST, on application startup, iterate `ToolConfig.Tools[]` and upsert each tool into the `tool_definitions` table.

For each `ToolModuleConfig` in configuration:
- Upsert by `name` (ON CONFLICT DO UPDATE)
- Compute `hash` from the tool's `FunctionDef` (name, description, parameters)
- Set `grants`, `fuel_limit`, `memory_pages` from config values
- Set `is_active = true`
- Set `updated_at = now()`

The system SHALL perform this boot seed in a single-threaded startup phase before handlers are initialized.

#### Scenario: Boot seed populates registry on startup

- GIVEN `ToolConfig.Tools` contains two tools: `read_file` and `write_file`
- WHEN the application starts
- THEN both tools exist in `tool_definitions` with correct hashes, grants, and limits
- AND `is_active = true` for both
- AND no errors occur during startup

#### Scenario: Boot seed updates existing tools on config change

- GIVEN `read_file` already exists in registry with `fuel_limit = 5000000`
- GIVEN `ToolConfig.Tools` now has `read_file` with `fuel_limit = 10000000`
- WHEN the application restarts
- THEN `read_file` in registry has `fuel_limit = 10000000`
- AND `hash` is recomputed if definition fields changed
- AND `updated_at` is refreshed

#### Scenario: Boot seed is idempotent

- GIVEN registry already contains `read_file` with matching config
- WHEN application starts again
- THEN no duplicate rows are created
- AND existing row is updated with `updated_at = now()`

---

### Requirement: Tool Lookup by Name

The system MUST provide a repository method to look up a tool definition by tenant and name, returning the full `ToolDefinition` entity or a "not found" error.

The lookup MUST only return tools where `is_active = true` for the given tenant.

#### Scenario: Lookup finds registered tool

- GIVEN `read_file` exists in registry for tenant `T1` with `is_active = true`
- WHEN the system looks up tool by tenant `T1` and name `"read_file"`
- THEN the tool definition is returned with all fields populated

#### Scenario: Lookup rejects inactive tool

- GIVEN `read_file` exists in registry for tenant `T1` with `is_active = false`
- WHEN the system looks up tool by tenant `T1` and name `"read_file"`
- THEN the lookup returns "tool not found" (same as unknown tool)

#### Scenario: Lookup rejects unknown tool

- GIVEN no tool named `"unknown_tool"` exists in registry for tenant `T1`
- WHEN the system looks up tool by tenant `T1` and name `"unknown_tool"`
- THEN the lookup returns "tool not found"

#### Scenario: Lookup rejects cross-tenant access

- GIVEN `read_file` exists in registry for tenant `T1` with `is_active = true`
- GIVEN no tool named `"read_file"` exists for tenant `T2`
- WHEN the system looks up tool by tenant `T2` and name `"read_file"`
- THEN the lookup returns "tool not found" (RLS isolation)

---

### Requirement: Handler Tool Validation

The system MUST validate every tool in a chat completion request at the handler boundary before execution.

For each tool in the request:
1. Extract `tenant_id` from the authenticated request context (JWT token)
2. Lookup the tool by tenant and name in the registry
3. If not found (unknown or inactive), reject the entire request with HTTP 400 and error code `tool_not_found`
4. If found, compute the hash of the request's tool definition (name, description, parameters) using the same canonical algorithm
5. Compare computed hash with registry hash
6. If hashes differ, reject the entire request with HTTP 400 and error code `tool_definition_mismatch`
7. If hashes match, allow the request to proceed with the validated tool reference

The system SHALL reject the entire request (not individual tools) on any validation failure.

#### Scenario: Unknown tool ID rejected

- GIVEN request contains tool `{"name": "evil_tool", "description": "...", "parameters": {...}}`
- GIVEN `evil_tool` is not in registry for the request's tenant
- WHEN handler processes the request
- THEN HTTP 400 is returned
- AND error body contains `code: "tool_not_found"`
- AND no tool execution occurs

#### Scenario: Known tool ID but hash mismatch (tampered definition)

- GIVEN `legit_tool` exists in registry for the request's tenant with hash `H1`
- GIVEN request contains `legit_tool` but with modified `description` or `parameters`
- WHEN handler processes the request
- THEN computed hash `H2` differs from registry hash `H1`
- THEN HTTP 400 is returned
- AND error body contains `code: "tool_definition_mismatch"`
- AND no tool execution occurs

#### Scenario: Valid tool with correct hash passes validation

- GIVEN `read_file` exists in registry for the request's tenant with hash `H1`
- GIVEN request contains `read_file` with identical definition
- WHEN handler processes the request
- THEN computed hash matches registry hash `H1`
- THEN request proceeds to execution phase
- AND validated tool reference (with registry grants, limits) is passed downstream

#### Scenario: Multiple tools - one invalid rejects entire request

- GIVEN request contains two tools: `read_file` (valid) and `evil_tool` (unknown)
- WHEN handler processes the request
- THEN HTTP 400 is returned for `tool_not_found`
- AND neither tool is executed

---

### Requirement: Admin Tool Registration (Future MCP)

The system MAY provide admin API endpoints for manual tool registration to support future MCP client integration.

Endpoints:
- `POST /admin/tools` — Register new tool definition for a tenant (computes hash, sets `is_active = true`)
- `PATCH /admin/tools/{name}` — Update grants, limits, or definition for a tenant (recomputes hash if definition fields change)
- `DELETE /admin/tools/{name}` — Deactivate tool for a tenant (sets `is_active = false`, soft delete)

All endpoints MUST require admin authentication (super-admin role). Super-admin MUST specify target `tenant_id` in request body (or use their token's tenant if present).

#### Scenario: Admin creates new tool

- GIVEN admin authenticated with super-admin role
- WHEN `POST /admin/tools` with valid tool definition and `tenant_id: "T1"`
- THEN tool is inserted into registry for tenant `T1` with computed hash
- AND `is_active = true`
- AND HTTP 201 returned with created tool

#### Scenario: Admin updates tool grants and limits

- GIVEN `read_file` exists in registry for tenant `T1`
- WHEN `PATCH /admin/tools/read_file` with `tenant_id: "T1"` and new `fuel_limit` and `grants`
- THEN registry row for tenant `T1` is updated
- AND `hash` unchanged (definition fields not modified)
- AND `updated_at` refreshed
- AND HTTP 200 returned

#### Scenario: Admin updates tool definition (recomputes hash)

- GIVEN `read_file` exists in registry for tenant `T1`
- WHEN `PATCH /admin/tools/read_file` with `tenant_id: "T1"` and modified `description`
- THEN registry row for tenant `T1` is updated
- AND `hash` recomputed from new definition
- AND `updated_at` refreshed
- AND HTTP 200 returned

#### Scenario: Admin deactivates tool

- GIVEN `read_file` exists in registry for tenant `T1` with `is_active = true`
- WHEN `DELETE /admin/tools/read_file` with `tenant_id: "T1"`
- THEN `is_active` set to `false` for tenant `T1`
- AND HTTP 204 returned
- AND subsequent lookups for tenant `T1` return "tool not found"

#### Scenario: Non-admin access denied

- GIVEN user authenticated without super-admin role
- WHEN any admin tool endpoint is called
- THEN HTTP 403 returned

#### Scenario: Cross-tenant isolation in admin API

- GIVEN `read_file` exists in registry for tenant `T1`
- GIVEN super-admin calls `PATCH /admin/tools/read_file` with `tenant_id: "T2"`
- WHEN `read_file` does not exist for tenant `T2`
- THEN HTTP 404 or 400 returned (tool not found for that tenant)
- AND tenant `T1` tool is unaffected

---

### Requirement: In-Memory Cache for Performance

The system SHOULD implement an in-memory LRU cache with 5-minute TTL for tool lookups to avoid a database query per tool validation.

The cache key SHALL be `(tenant_id, tool_name)` to ensure tenant isolation. The cache MUST be invalidated on admin write operations (create, update, deactivate) for the affected tenant.

#### Scenario: Cache hit avoids database query

- GIVEN `read_file` for tenant `T1` was looked up 30 seconds ago
- WHEN handler validates `read_file` for tenant `T1` again
- THEN registry returns cached result
- AND no database query executed

#### Scenario: Cache invalidated on admin update

- GIVEN `read_file` for tenant `T1` is cached
- WHEN admin updates `read_file` for tenant `T1` via `PATCH /admin/tools/read_file`
- THEN cache entry for `(T1, read_file)` is invalidated
- AND next lookup for tenant `T1` queries database

#### Scenario: Cache tenant isolation

- GIVEN `read_file` for tenant `T1` is cached
- WHEN handler validates `read_file` for tenant `T2`
- THEN cache miss occurs (different tenant)
- AND database query executed for tenant `T2`

#### Scenario: Cache expires after TTL

- GIVEN `read_file` was looked up 6 minutes ago
- WHEN handler validates `read_file`
- THEN cache miss occurs
- AND database query executed
- AND new result cached with fresh TTL