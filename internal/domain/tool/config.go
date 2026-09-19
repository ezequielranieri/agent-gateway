package tool

import (
	"time"
)

// ToolConfig holds the configuration for tool execution.
type ToolConfig struct {
	// MaxIterations is the maximum number of tool execution iterations in a single request.
	// Default: 5
	MaxIterations int `koanf:"max_iterations"`

	// DefaultTimeoutMs is the default execution timeout in milliseconds for tool execution.
	// Default: 30000 (30 seconds)
	DefaultTimeoutMs int64 `koanf:"default_timeout_ms"`

	// DefaultMemoryPages is the default memory limit in 64KB pages for tool execution.
	// Default: 512 (32MB)
	DefaultMemoryPages uint32 `koanf:"default_memory_pages"`

	// CacheTTL is the time-to-live for the in-memory tool registry cache.
	// Default: 5m (can be overridden via TOOL_CACHE_TTL env var)
	CacheTTL time.Duration `koanf:"cache_ttl"`

	// CacheMaxEntries is the maximum number of entries in the in-memory tool registry cache.
	// Default: 1000 (can be overridden via TOOL_CACHE_MAX_ENTRIES env var)
	CacheMaxEntries int `koanf:"cache_max_entries"`

	// Tools is the list of configured tool modules.
	Tools []ToolModuleConfig `koanf:"tools"`
}

// ToolModuleConfig defines a WASM module for a tool.
type ToolModuleConfig struct {
	// Name is the tool identifier (must match ToolCall.Name).
	Name string `koanf:"name"`

	// ModulePath is the path to the .wasm file.
	ModulePath string `koanf:"module_path"`

	// Grants defines what the tool can access.
	Grants ToolGrants `koanf:"grants"`

	// Limits overrides default execution limits for this specific tool.
	Limits ToolLimits `koanf:"limits,omitempty"`

	// RequiresApproval indicates if this tool needs HITL approval before execution.
	RequiresApproval bool `koanf:"requires_approval"`
}

// ToolGrants defines what the tool can access.
type ToolGrants struct {
	// FSReadOnlyMounts is the list of read-only filesystem mounts.
	FSReadOnlyMounts []FSMount `koanf:"fs_read_only_mounts"`

	// AllowNetwork enables network access for the tool (default: false).
	AllowNetwork bool `koanf:"allow_network"`
}

// FSMount defines a read-only filesystem mount.
type FSMount struct {
	// GuestPath is the path inside the WebAssembly module.
	GuestPath string `koanf:"guest_path"`

	// HostPath is the host directory to mount.
	HostPath string `koanf:"host_path"`
}

// ToolLimits overrides default execution limits.
type ToolLimits struct {
	// TimeoutMs is the execution timeout in milliseconds for this tool.
	// Valid range: 1-300000 (1ms to 5 minutes)
	TimeoutMs int64 `koanf:"timeout_ms,omitempty"`

	// MemoryPages is the memory limit in 64KB pages for this tool.
	MemoryPages uint32 `koanf:"memory_pages,omitempty"`
}

// EffectiveTimeoutMs returns the timeout for a tool in milliseconds, falling back to default.
func (tc *ToolConfig) EffectiveTimeoutMs(tool *ToolModuleConfig) int64 {
	if tool.Limits.TimeoutMs > 0 {
		return tool.Limits.TimeoutMs
	}
	if tc.DefaultTimeoutMs > 0 {
		return tc.DefaultTimeoutMs
	}
	return 30000 // 30 seconds default
}

// EffectiveMemoryPages returns the memory limit for a tool, falling back to default.
func (tc *ToolConfig) EffectiveMemoryPages(tool *ToolModuleConfig) uint32 {
	if tool.Limits.MemoryPages > 0 {
		return tool.Limits.MemoryPages
	}
	if tc.DefaultMemoryPages > 0 {
		return tc.DefaultMemoryPages
	}
	return 512
}