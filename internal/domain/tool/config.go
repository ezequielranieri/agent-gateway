package tool

import (
	"time"
)

// ToolConfig holds the configuration for tool execution.
type ToolConfig struct {
	// MaxIterations is the maximum number of tool execution iterations in a single request.
	// Default: 5
	MaxIterations int `koanf:"max_iterations"`

	// DefaultTimeout is the default wall-time timeout for tool execution.
	// Default: 30s
	DefaultTimeout time.Duration `koanf:"default_timeout"`

	// DefaultFuel is the default fuel limit (WebAssembly instructions) for tool execution.
	// Default: 10_000_000
	DefaultFuel uint64 `koanf:"default_fuel"`

	// DefaultMemoryPages is the default memory limit in 64KB pages for tool execution.
	// Default: 512 (32MB)
	DefaultMemoryPages uint32 `koanf:"default_memory_pages"`

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
	// Timeout is the wall-time timeout for this tool.
	Timeout time.Duration `koanf:"timeout,omitempty"`

	// Fuel is the instruction limit for this tool (WebAssembly fuel).
	Fuel uint64 `koanf:"fuel,omitempty"`

	// MemoryPages is the memory limit in 64KB pages for this tool.
	MemoryPages uint32 `koanf:"memory_pages,omitempty"`
}

// EffectiveTimeout returns the timeout for a tool, falling back to default.
func (tc *ToolConfig) EffectiveTimeout(tool *ToolModuleConfig) time.Duration {
	if tool.Limits.Timeout > 0 {
		return tool.Limits.Timeout
	}
	if tc.DefaultTimeout > 0 {
		return tc.DefaultTimeout
	}
	return 30 * time.Second
}

// EffectiveFuel returns the fuel limit for a tool, falling back to default.
func (tc *ToolConfig) EffectiveFuel(tool *ToolModuleConfig) uint64 {
	if tool.Limits.Fuel > 0 {
		return tool.Limits.Fuel
	}
	if tc.DefaultFuel > 0 {
		return tc.DefaultFuel
	}
	return 10_000_000
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