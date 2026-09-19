//go:build integration
// +build integration

package wazero

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ezequielranieri/agent-gateway/internal/domain/tool"
)

func TestExecutor_LimitsEnforcement(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()

	logger := zerolog.New(zerolog.NewTestWriter(t)).Level(zerolog.DebugLevel)

	// Setup tool config with execution_timeout_ms and memory_pages
	toolConfig := tool.ToolConfig{
		DefaultTimeoutMs:     30000, // 30 seconds in ms
		DefaultMemoryPages:   512,
		CacheTTL:             5 * time.Minute,
		CacheMaxEntries:      1000,
		Tools: []tool.ToolModuleConfig{
			{
				Name:        "echo_tool",
				ModulePath:  getTestWASMPath(t, "echo.wasm"),
				Grants:      tool.ToolGrants{FSReadOnlyMounts: nil, AllowNetwork: false},
				Limits:      tool.ToolLimits{TimeoutMs: 10000, MemoryPages: 256}, // 10 seconds
				RequiresApproval: false,
			},
			{
				Name:        "memory_grow_tool",
				ModulePath:  getTestWASMPath(t, "memory_grow.wasm"),
				Grants:      tool.ToolGrants{FSReadOnlyMounts: nil, AllowNetwork: false},
				Limits:      tool.ToolLimits{TimeoutMs: 10000, MemoryPages: 256},
				RequiresApproval: false,
			},
			{
				Name:        "infinite_loop_tool",
				ModulePath:  getTestWASMPath(t, "infinite_loop.wasm"),
				Grants:      tool.ToolGrants{FSReadOnlyMounts: nil, AllowNetwork: false},
				Limits:      tool.ToolLimits{TimeoutMs: 2000, MemoryPages: 256}, // 2 seconds
				RequiresApproval: false,
			},
			{
				Name:        "min_memory_exceed_tool",
				ModulePath:  getTestWASMPath(t, "min_memory_exceed.wasm"),
				Grants:      tool.ToolGrants{FSReadOnlyMounts: nil, AllowNetwork: false},
				Limits:      tool.ToolLimits{TimeoutMs: 10000, MemoryPages: 256},
				RequiresApproval: false,
			},
		},
	}

	// Create executor with tool config
	executor, err := NewWasmExecutor(toolConfig, logger)
	require.NoError(t, err)
	defer executor.Close(context.Background())

t.Run("Memory grow exceeding limit fails with resource exhausted", func(t *testing.T) {
		// This test requires a WASM module that:
		// 1. Calls memory.grow
		// 2. Checks if result is -1 (failed)
		// 3. If -1, executes unreachable (trap)
		
		call := tool.ToolCall{
			ID:        "test-1",
			Name:      "memory_grow_tool",
			Arguments: map[string]any{},
		}

		result, err := executor.Execute(ctx, call)
		
		// Should fail with resource exhausted error (or trap from unreachable)
		assert.Error(t, err)
		// The error could be "resource exhausted" or "wasm error: unreachable" depending on wazero version
		assert.True(t, 
			strings.Contains(result.Error, "resource exhausted") || 
			strings.Contains(result.Error, "unreachable"),
			"result.Error should contain 'resource exhausted' or 'unreachable', got: %s", result.Error)
		assert.NotNil(t, result.Error)
	})

	t.Run("Min memory exceeding bucket limit fails at instantiation", func(t *testing.T) {
		// This test requires a WASM module with min_memory > bucket limit
		// The module should fail to instantiate
		
		call := tool.ToolCall{
			ID:   "test-2",
			Name: "min_memory_exceed_tool",
			Arguments: map[string]any{},
		}

		result, err := executor.Execute(ctx, call)
		
		// Should fail at instantiation (memory limit exceeded)
		assert.Error(t, err, "Expected error for min_memory_exceed_tool, got nil")
		assert.True(t, 
			strings.Contains(result.Error, "over limit") || 
			strings.Contains(result.Error, "over limit of") ||
			strings.Contains(result.Error, "memory limit"),
			"result.Error should contain memory limit error, got: %s", result.Error)
		assert.NotNil(t, result.Error)
	})

	t.Run("Infinite loop terminated by timeout", func(t *testing.T) {
		// This test requires a WASM module with infinite loop
		// Should be terminated by context timeout + WithCloseOnContextDone
		
		call := tool.ToolCall{
			ID:   "test-3",
			Name: "infinite_loop_tool",
			Arguments: map[string]any{},
		}

		start := time.Now()
		result, err := executor.Execute(ctx, call)
		duration := time.Since(start)
		
		// Should fail with timeout error (not success)
		assert.Error(t, err)
		assert.True(t,
			strings.Contains(result.Error, "deadline exceeded") ||
			strings.Contains(result.Error, "context deadline") ||
			strings.Contains(result.Error, "timeout"),
			"result.Error should contain timeout/deadline error, got: %s", result.Error)
		// Should terminate within reasonable time (timeout + margin)
		assert.Less(t, duration, 10*time.Second)
		assert.NotNil(t, result.Error)
	})

	t.Run("No limits configured - fail closed", func(t *testing.T) {
		// Create config with zero limits
		zeroConfig := tool.ToolConfig{
			DefaultTimeoutMs:     0,
			DefaultMemoryPages:   0,
			CacheTTL:             5 * time.Minute,
			CacheMaxEntries:      1000,
			Tools: []tool.ToolModuleConfig{
				{
					Name:        "echo_tool",
					ModulePath:  getTestWASMPath(t, "echo.wasm"),
					Grants:      tool.ToolGrants{FSReadOnlyMounts: nil, AllowNetwork: false},
					Limits:      tool.ToolLimits{TimeoutMs: 0, MemoryPages: 0},
					RequiresApproval: false,
				},
			},
		}

		zeroExecutor, err := NewWasmExecutor(zeroConfig, logger)
		require.NoError(t, err)
		defer zeroExecutor.Close(context.Background())

		call := tool.ToolCall{
			ID:   "test-4",
			Name: "echo_tool",
			Arguments: map[string]any{},
		}

		result, err := zeroExecutor.Execute(ctx, call)
		
		// Should fail closed - no limits means reject
		assert.Error(t, err)
		assert.True(t,
			strings.Contains(result.Error, "resource exhausted") ||
			strings.Contains(result.Error, "fail-closed") ||
			strings.Contains(result.Error, "no limits configured") ||
			strings.Contains(result.Error, "resource limits not configured"),
			"result.Error should contain fail-closed message, got: %s", result.Error)
		assert.NotNil(t, result.Error)
	})
}

// getTestWASMPath returns path to test WASM module
func getTestWASMPath(t *testing.T, name string) string {
	// Test WASM modules should be in test/wasm/ relative to project root
	// go test runs from module root by default
	cwd, _ := os.Getwd()
	t.Logf("CWD: %s", cwd)
	// From package directory (internal/adapter/tool/wazero/), need ../../../../
	path := "../../../../test/wasm/" + name
	if _, err := os.Stat(path); err == nil {
		return path
	}
	// Fallback: from project root
	path = "test/wasm/" + name
	if _, err := os.Stat(path); err == nil {
		return path
	}
	// Debug: print what we tried
	t.Logf("Tried paths: %s and %s (CWD: %s)", "../../../test/wasm/"+name, "test/wasm/"+name, cwd)
	t.Skipf("Test WASM module %s not found", name)
	return ""
}