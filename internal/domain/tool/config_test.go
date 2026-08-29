package tool

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestToolConfigDefaults(t *testing.T) {
	cfg := ToolConfig{}

	assert.Equal(t, 0, cfg.MaxIterations)
	assert.Equal(t, time.Duration(0), cfg.DefaultTimeout)
	assert.Equal(t, uint64(0), cfg.DefaultFuel)
	assert.Equal(t, uint32(0), cfg.DefaultMemoryPages)
	assert.Nil(t, cfg.Tools)
}

func TestEffectiveTimeout(t *testing.T) {
	cfg := ToolConfig{
		DefaultTimeout: 30 * time.Second,
	}

	// No tool limits -> default
	tool := &ToolModuleConfig{}
	assert.Equal(t, 30*time.Second, cfg.EffectiveTimeout(tool))

	// Tool has limit -> use tool limit
	tool.Limits.Timeout = 10 * time.Second
	assert.Equal(t, 10*time.Second, cfg.EffectiveTimeout(tool))
}

func TestEffectiveFuel(t *testing.T) {
	cfg := ToolConfig{
		DefaultFuel: 10_000_000,
	}

	tool := &ToolModuleConfig{}
	assert.Equal(t, uint64(10_000_000), cfg.EffectiveFuel(tool))

	tool.Limits.Fuel = 5_000_000
	assert.Equal(t, uint64(5_000_000), cfg.EffectiveFuel(tool))
}

func TestEffectiveMemoryPages(t *testing.T) {
	cfg := ToolConfig{
		DefaultMemoryPages: 512,
	}

	tool := &ToolModuleConfig{}
	assert.Equal(t, uint32(512), cfg.EffectiveMemoryPages(tool))

	tool.Limits.MemoryPages = 256
	assert.Equal(t, uint32(256), cfg.EffectiveMemoryPages(tool))
}

func TestToolModuleConfig(t *testing.T) {
	cfg := ToolModuleConfig{
		Name:        "test_tool",
		ModulePath:  "./test.wasm",
		RequiresApproval: true,
		Grants: ToolGrants{
			AllowNetwork: false,
			FSReadOnlyMounts: []FSMount{
				{GuestPath: "/data", HostPath: "/host/data"},
			},
		},
		Limits: ToolLimits{
			Timeout:     10 * time.Second,
			Fuel:        5_000_000,
			MemoryPages: 256,
		},
	}

	assert.Equal(t, "test_tool", cfg.Name)
	assert.Equal(t, "./test.wasm", cfg.ModulePath)
	assert.True(t, cfg.RequiresApproval)
	assert.False(t, cfg.Grants.AllowNetwork)
	assert.Len(t, cfg.Grants.FSReadOnlyMounts, 1)
	assert.Equal(t, "/data", cfg.Grants.FSReadOnlyMounts[0].GuestPath)
	assert.Equal(t, "/host/data", cfg.Grants.FSReadOnlyMounts[0].HostPath)
	assert.Equal(t, 10*time.Second, cfg.Limits.Timeout)
	assert.Equal(t, uint64(5_000_000), cfg.Limits.Fuel)
	assert.Equal(t, uint32(256), cfg.Limits.MemoryPages)
}

func TestToolConfigWithMultipleTools(t *testing.T) {
	cfg := ToolConfig{
		MaxIterations:      5,
		DefaultTimeout:     30 * time.Second,
		DefaultFuel:        10_000_000,
		DefaultMemoryPages: 512,
		Tools: []ToolModuleConfig{
			{
				Name:       "tool_a",
				ModulePath: "./a.wasm",
			},
			{
				Name:       "tool_b",
				ModulePath: "./b.wasm",
				RequiresApproval: true,
			},
		},
	}

	assert.Equal(t, 5, cfg.MaxIterations)
	assert.Len(t, cfg.Tools, 2)
	assert.Equal(t, "tool_a", cfg.Tools[0].Name)
	assert.Equal(t, "tool_b", cfg.Tools[1].Name)
	assert.True(t, cfg.Tools[1].RequiresApproval)
}