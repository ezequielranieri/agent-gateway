package tool

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSentinelErrors(t *testing.T) {
	require.ErrorIs(t, ErrToolNotFound, ErrToolNotFound)
	require.ErrorIs(t, ErrToolTimeout, ErrToolTimeout)
	require.ErrorIs(t, ErrToolResourceExhausted, ErrToolResourceExhausted)
	require.ErrorIs(t, ErrToolExecutionFailed, ErrToolExecutionFailed)
	require.ErrorIs(t, ErrToolNotAllowed, ErrToolNotAllowed)
	require.ErrorIs(t, ErrMaxIterationsExceeded, ErrMaxIterationsExceeded)

	// Each error should be distinct
	assert.NotEqual(t, ErrToolNotFound, ErrToolTimeout)
	assert.NotEqual(t, ErrToolTimeout, ErrToolResourceExhausted)
	assert.NotEqual(t, ErrToolExecutionFailed, ErrToolNotAllowed)
}

func TestToolCallToolResult(t *testing.T) {
	call := ToolCall{
		ID:        "call_123",
		Name:      "test_tool",
		Arguments: map[string]any{"key": "value"},
	}

	result := ToolResult{
		CallID:   call.ID,
		Output:   map[string]any{"result": "ok"},
		Error:    "",
		Duration: 42,
	}

	assert.Equal(t, "call_123", call.ID)
	assert.Equal(t, "test_tool", call.Name)
	assert.Equal(t, "value", call.Arguments["key"])
	assert.Equal(t, "call_123", result.CallID)
	assert.Equal(t, "ok", result.Output["result"])
	assert.Empty(t, result.Error)
	assert.Equal(t, 42, int(result.Duration))
}

func TestToolCallJSON(t *testing.T) {
	call := ToolCall{
		ID:        "call_123",
		Name:      "test_tool",
		Arguments: map[string]any{"key": "value", "num": 42},
	}

	// Should be JSON marshalable
	data, err := json.Marshal(call)
	require.NoError(t, err)

	var decoded ToolCall
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, call.ID, decoded.ID)
	assert.Equal(t, call.Name, decoded.Name)
	assert.Equal(t, "value", decoded.Arguments["key"])
	assert.Equal(t, float64(42), decoded.Arguments["num"])
}

func TestToolResultJSON(t *testing.T) {
	result := ToolResult{
		CallID:   "call_123",
		Output:   map[string]any{"status": "ok", "data": 123},
		Error:    "",
		Duration: 100,
	}

	data, err := json.Marshal(result)
	require.NoError(t, err)

	var decoded ToolResult
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, result.CallID, decoded.CallID)
	assert.Equal(t, "ok", decoded.Output["status"])
	assert.Equal(t, float64(123), decoded.Output["data"])
	assert.Equal(t, result.Duration, decoded.Duration)
}