package mock

import (
	"context"
	"testing"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/domain/tool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMockExecutor_DefaultBehavior(t *testing.T) {
	m := NewMockExecutor(WithSupportedTools("echo_tool"))

	ctx := context.Background()
	call := tool.ToolCall{
		ID:        "call_1",
		Name:      "echo_tool",
		Arguments: map[string]any{"message": "hello"},
	}

	result, err := m.Execute(ctx, call)

	require.NoError(t, err)
	assert.Equal(t, "call_1", result.CallID)
	// Default behavior echoes arguments under "echo" key
	echoed := result.Output["echo"].(map[string]any)
	assert.Equal(t, "hello", echoed["message"])
	assert.Empty(t, result.Error)
	assert.Equal(t, 1, m.CallCount())
}

func TestMockExecutor_WithLatency(t *testing.T) {
	m := NewMockExecutor(
		WithSupportedTools("slow_tool"),
		WithLatency(50*time.Millisecond),
	)

	ctx := context.Background()
	call := tool.ToolCall{ID: "call_1", Name: "slow_tool"}

	start := time.Now()
	_, err := m.Execute(ctx, call)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.GreaterOrEqual(t, elapsed, 50*time.Millisecond)
	assert.Equal(t, 1, m.CallCount())
}

func TestMockExecutor_WithError(t *testing.T) {
	m := NewMockExecutor(
		WithSupportedTools("fail_tool"),
		WithError(tool.ErrToolTimeout),
	)

	ctx := context.Background()
	call := tool.ToolCall{ID: "call_1", Name: "fail_tool"}

	_, err := m.Execute(ctx, call)

	require.Error(t, err)
	assert.ErrorIs(t, err, tool.ErrToolTimeout)
}

func TestMockExecutor_WithFixedResult(t *testing.T) {
	fixed := &tool.ToolResult{
		CallID:   "fixed",
		Output:   map[string]any{"status": "ok"},
		Error:    "",
		Duration: 100,
	}

	m := NewMockExecutor(
		WithSupportedTools("fixed_tool"),
		WithFixedResult(fixed),
	)

	ctx := context.Background()
	call := tool.ToolCall{ID: "call_1", Name: "fixed_tool"}

	result, err := m.Execute(ctx, call)

	require.NoError(t, err)
	assert.Equal(t, "call_1", result.CallID) // Should use call's ID
	assert.Equal(t, "ok", result.Output["status"])
}

func TestMockExecutor_WithCustomFunc(t *testing.T) {
	m := NewMockExecutor(
		WithSupportedTools("custom_tool"),
		WithCustomFunc(func(ctx context.Context, call tool.ToolCall) (tool.ToolResult, error) {
			if call.Arguments["fail"] == true {
				return tool.ToolResult{}, tool.ErrToolExecutionFailed
			}
			return tool.ToolResult{
				CallID: call.ID,
				Output: map[string]any{"custom": true, "input": call.Arguments},
			}, nil
		}),
	)

	ctx := context.Background()

	// Success case
	call1 := tool.ToolCall{ID: "call_1", Name: "custom_tool", Arguments: map[string]any{}}
	result1, err1 := m.Execute(ctx, call1)
	require.NoError(t, err1)
	assert.True(t, result1.Output["custom"].(bool))

	// Fail case
	call2 := tool.ToolCall{ID: "call_2", Name: "custom_tool", Arguments: map[string]any{"fail": true}}
	_, err2 := m.Execute(ctx, call2)
	require.Error(t, err2)
	assert.ErrorIs(t, err2, tool.ErrToolExecutionFailed)
}

func TestMockExecutor_SupportsTool(t *testing.T) {
	m := NewMockExecutor(WithSupportedTools("tool_a", "tool_b"))

	assert.True(t, m.SupportsTool("tool_a"))
	assert.True(t, m.SupportsTool("tool_b"))
	assert.False(t, m.SupportsTool("tool_c"))
}

func TestMockExecutor_CallTracking(t *testing.T) {
	m := NewMockExecutor(WithSupportedTools("track_tool"))

	ctx := context.Background()

	// First call
	call1 := tool.ToolCall{ID: "call_1", Name: "track_tool"}
	m.Execute(ctx, call1)
	assert.Equal(t, 1, m.CallCount())
	last := m.LastCall()
	require.NotNil(t, last)
	assert.Equal(t, "call_1", last.ID)

	// Second call
	call2 := tool.ToolCall{ID: "call_2", Name: "track_tool"}
	m.Execute(ctx, call2)
	assert.Equal(t, 2, m.CallCount())
	last = m.LastCall()
	require.NotNil(t, last)
	assert.Equal(t, "call_2", last.ID)

	// Reset
	m.Reset()
	assert.Equal(t, 0, m.CallCount())
	assert.Nil(t, m.LastCall())
}

func TestMockExecutor_UnsupportedTool(t *testing.T) {
	m := NewMockExecutor(WithSupportedTools("allowed_tool"))

	ctx := context.Background()
	call := tool.ToolCall{ID: "call_1", Name: "disallowed_tool"}

	result, err := m.Execute(ctx, call)

	// Should still execute (mock doesn't enforce support check)
	// The check is meant for the caller to verify before calling
	require.NoError(t, err)
	// Default behavior echoes arguments under "echo" key
	echoed := result.Output["echo"].(map[string]any)
	assert.Empty(t, echoed)
}

func TestMockExecutor_Reset(t *testing.T) {
	m := NewMockExecutor(WithSupportedTools("reset_tool"))
	ctx := context.Background()

	m.Execute(ctx, tool.ToolCall{ID: "call_1", Name: "reset_tool"})
	assert.Equal(t, 1, m.CallCount())

	m.Reset()
	assert.Equal(t, 0, m.CallCount())
	assert.Nil(t, m.LastCall())
}