# WASM Resource Limits Specification

## Purpose

This specification defines the enforcement of WebAssembly fuel and memory limits per tool execution. The executor must apply configured limits from the tool registry (with fallback to configuration defaults) and fail closed when limits are exceeded, preventing resource exhaustion attacks.

## Requirements

### Requirement: Executor Applies Fuel and Memory Limits

The system MUST configure the WASM module with `WithFuel()` and `WithMemoryPages()` from the validated tool's registry entry before execution.

The fuel limit MUST come from the tool's `fuel_limit` column (BIGINT, default 10,000,000).
The memory limit MUST come from the tool's `memory_pages` column (INT, default 512 pages = 32 MB).

If the registry entry has zero or missing limits, the system SHALL fall back to `ToolConfig.EffectiveFuel()` and `ToolConfig.EffectiveMemoryPages()` from configuration.

The system SHALL call `wazero.NewModuleConfig().WithFuel(fuel_limit).WithMemoryPages(memory_pages)` when creating the module config.

#### Scenario: Executor uses registry limits for valid tool

- GIVEN `read_file` in registry has `fuel_limit = 5000000` and `memory_pages = 256`
- GIVEN request with valid `read_file` tool (correct hash) is validated
- WHEN executor creates module config for this tool
- THEN `WithFuel(5000000)` is called
- AND `WithMemoryPages(256)` is called
- AND execution proceeds with these limits

#### Scenario: Executor falls back to config defaults when registry limits missing

- GIVEN `read_file` in registry has `fuel_limit = 0` and `memory_pages = 0`
- GIVEN `ToolConfig.EffectiveFuel() = 10000000` and `EffectiveMemoryPages() = 512`
- WHEN executor creates module config for this tool
- THEN `WithFuel(10000000)` is called (from config)
- AND `WithMemoryPages(512)` is called (from config)

#### Scenario: Executor uses registry limits over config when both present

- GIVEN `read_file` in registry has `fuel_limit = 5000000`
- GIVEN `ToolConfig.EffectiveFuel() = 10000000`
- WHEN executor creates module config
- THEN `WithFuel(5000000)` is called (registry takes precedence)
- AND config value is ignored for this tool

---

### Requirement: Fail Closed on Missing Limits

The system MUST fail with `ErrToolResourceExhausted` if the executor is invoked without fuel or memory limits configured (i.e., neither registry nor config provides limits).

This is a "fail closed" design: the absence of limits is treated as a configuration error, not an unlimited execution.

#### Scenario: WASM execution without any limits configured → fail closed

- GIVEN tool registry lookup returns a tool with `fuel_limit = 0` and `memory_pages = 0`
- GIVEN `ToolConfig.EffectiveFuel()` returns 0 and `EffectiveMemoryPages()` returns 0
- WHEN executor attempts to create module config
- THEN execution fails immediately with `ErrToolResourceExhausted`
- AND no WASM module is instantiated
- AND error message indicates "resource limits not configured"

---

### Requirement: Fuel Exhaustion During Execution

The system MUST return `ErrToolResourceExhausted` when a valid tool with valid hash exhausts its fuel limit DURING execution.

This is a critical boundary case: even when all validations pass (tool exists in registry, hash matches, limits are configured), the executor MUST enforce the fuel boundary and terminate execution if fuel is exhausted mid-execution.

The error MUST be `ErrToolResourceExhausted` (not a generic error, not a panic).

#### Scenario: Valid tool + valid hash + fuel exhausted mid-execution → ErrToolResourceExhausted

- GIVEN `compute_heavy` tool registered with `fuel_limit = 100000` (low for testing)
- GIVEN request contains `compute_heavy` with correct hash (validation passes)
- GIVEN the WASM module contains a loop that consumes more than 100,000 fuel units
- WHEN executor runs the module
- THEN execution terminates when fuel is exhausted
- AND `ErrToolResourceExhausted` is returned
- AND HTTP 500 (or appropriate error) is returned to client
- AND error message indicates "fuel exhausted" or "resource exhausted"

#### Scenario: Valid tool + valid hash + execution completes within fuel limit → success

- GIVEN `read_file` tool registered with `fuel_limit = 10000000`
- GIVEN request contains `read_file` with correct hash
- GIVEN the WASM module completes execution using only 500,000 fuel units
- WHEN executor runs the module
- THEN execution completes successfully
- AND HTTP 200 is returned with tool result
- AND no `ErrToolResourceExhausted` occurs

---

### Requirement: Memory Limit Enforcement

The system MUST return `ErrToolResourceExhausted` when a valid tool with valid hash exceeds its memory limit during execution.

The memory limit is enforced by `WithMemoryPages()` which limits the maximum linear memory pages the module can allocate (1 page = 64 KB).

#### Scenario: Valid tool + valid hash + memory exhausted mid-execution → ErrToolResourceExhausted

- GIVEN `memory_heavy` tool registered with `memory_pages = 10` (640 KB limit)
- GIVEN request contains `memory_heavy` with correct hash (validation passes)
- GIVEN the WASM module attempts to allocate more than 10 pages (e.g., `memory.grow` beyond limit)
- WHEN executor runs the module
- THEN execution terminates when memory limit is hit
- AND `ErrToolResourceExhausted` is returned
- AND HTTP 500 (or appropriate error) is returned to client
- AND error message indicates "memory exhausted" or "resource exhausted"

#### Scenario: Valid tool + valid hash + execution completes within memory limit → success

- GIVEN `read_file` tool registered with `memory_pages = 512`
- GIVEN request contains `read_file` with correct hash
- GIVEN the WASM module uses only 50 pages during execution
- WHEN executor runs the module
- THEN execution completes successfully
- AND HTTP 200 is returned with tool result
- AND no `ErrToolResourceExhausted` occurs

---

### Requirement: ErrToolResourceExhausted Error Type

The system MUST define a sentinel error `ErrToolResourceExhausted` in the tool domain errors package.

This error MUST be distinguishable from other errors (validation errors, tool not found, etc.) to allow proper error handling and HTTP status mapping.

#### Scenario: ErrToolResourceExhausted is returned for fuel exhaustion

- GIVEN any scenario where fuel is exhausted during execution
- WHEN the executor catches the wazero fuel exhaustion
- THEN it returns `ErrToolResourceExhausted` wrapping the underlying cause
- AND error type can be checked with `errors.Is(err, ErrToolResourceExhausted)`

#### Scenario: ErrToolResourceExhausted is returned for memory exhaustion

- GIVEN any scenario where memory limit is exceeded during execution
- WHEN the executor catches the wazero memory limit error
- THEN it returns `ErrToolResourceExhausted` wrapping the underlying cause

#### Scenario: ErrToolResourceExhausted is returned for missing limits configuration

- GIVEN executor invoked with no fuel/memory limits configured
- WHEN executor validates limits before module creation
- THEN it returns `ErrToolResourceExhausted` with message "resource limits not configured"

---

### Requirement: Error Mapping to HTTP Responses

The system SHOULD map `ErrToolResourceExhausted` to HTTP 500 (Internal Server Error) with a generic message to avoid leaking resource limit details to clients.

Validation errors (`tool_not_found`, `tool_definition_mismatch`) MUST map to HTTP 400.

#### Scenario: Resource exhaustion maps to HTTP 500

- GIVEN tool execution fails with `ErrToolResourceExhausted`
- WHEN handler maps error to HTTP response
- THEN HTTP 500 is returned
- AND response body contains generic error message (e.g., "tool execution failed")
- AND no internal limit values are exposed

#### Scenario: Tool validation errors map to HTTP 400

- GIVEN handler validation fails with `tool_not_found` or `tool_definition_mismatch`
- WHEN handler maps error to HTTP response
- THEN HTTP 400 is returned
- AND response body contains specific error code (`tool_not_found` or `tool_definition_mismatch`)

---

### Requirement: Default Limits Configuration

The system SHOULD provide sensible default limits in `ToolConfig`:
- `DefaultFuelLimit = 10_000_000` (10 million fuel units)
- `DefaultMemoryPages = 512` (32 MB)

These defaults SHALL be used as fallback when registry entry has zero limits.

#### Scenario: Config provides default limits

- GIVEN `ToolConfig` has `DefaultFuelLimit = 10000000` and `DefaultMemoryPages = 512`
- GIVEN tool in registry has zero limits
- WHEN executor resolves limits
- THEN config defaults are used
- AND execution proceeds with these limits

---

### Requirement: Per-Tool Limit Configuration via Registry

The system MUST allow per-tool limit configuration through the registry (set at boot seed or via admin API).

This enables fine-grained control: compute-heavy tools get higher fuel, memory-intensive tools get more pages, simple tools get restrictive limits.

#### Scenario: Different tools have different limits

- GIVEN `compute_heavy` has `fuel_limit = 50000000`, `memory_pages = 1024`
- GIVEN `read_file` has `fuel_limit = 1000000`, `memory_pages = 64`
- GIVEN `simple_echo` has `fuel_limit = 100000`, `memory_pages = 16`
- WHEN each tool is executed
- THEN each uses its own configured limits
- AND limits are enforced independently per tool execution

---

### Requirement: Limits Enforced Per Execution (Not Shared)

The system MUST enforce fuel and memory limits independently for each tool execution.

Fuel and memory MUST NOT be shared or pooled across multiple tool invocations in the same request or across requests.

#### Scenario: Multiple tool executions in one request each get fresh limits

- GIVEN request contains two tools: `read_file` (fuel=1M) and `write_file` (fuel=1M)
- WHEN executor runs `read_file` and consumes 800K fuel
- AND then executor runs `write_file`
- THEN `write_file` starts with full 1M fuel (not 200K remaining)
- AND each tool's memory limit is independent

#### Scenario: Concurrent requests each get fresh limits

- GIVEN two concurrent requests for `read_file` (fuel=1M each)
- WHEN both execute simultaneously
- THEN each gets its own 1M fuel budget
- AND one request's fuel consumption does not affect the other