package tool

import (
	"context"
	"time"
)

// ToolExecutor is the interface for executing tool calls.
// Implementations handle provider-specific execution and error mapping.
type ToolExecutor interface {
	// Execute runs a tool call and returns the result.
	// Errors should be mapped to domain sentinel errors where applicable.
	Execute(ctx context.Context, call ToolCall) (ToolResult, error)

	// Name returns the executor identifier (e.g., "mock", "wazero").
	Name() string

	// SupportsTool returns true if this executor can execute the given tool.
	SupportsTool(name string) bool
}

// ToolCall represents a single tool invocation request.
type ToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ToolResult represents the result of a tool execution.
type ToolResult struct {
	CallID   string         `json:"call_id"`
	Output   map[string]any `json:"output,omitempty"`
	Error    string         `json:"error,omitempty"`
	Duration time.Duration  `json:"duration_ms"`
}