package mock

import (
	"context"
	"sync"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/domain/tool"
)

// MockExecutor implements tool.ToolExecutor for testing.
// It provides controllable behavior via functional options.
type MockExecutor struct {
	mu           sync.Mutex
	name         string
	supported    map[string]bool
	latency      time.Duration
	err          error
	response     *tool.ToolResult
	fixedResult  *tool.ToolResult
	callCount    int
	lastCall     *tool.ToolCall
	customFunc   func(context.Context, tool.ToolCall) (tool.ToolResult, error)
}

// MockOption configures a MockExecutor.
type MockOption func(*MockExecutor)

// WithName sets the executor name.
func WithName(name string) MockOption {
	return func(m *MockExecutor) { m.name = name }
}

// WithSupportedTools sets the list of supported tool names.
func WithSupportedTools(tools ...string) MockOption {
	return func(m *MockExecutor) {
		m.supported = make(map[string]bool, len(tools))
		for _, t := range tools {
			m.supported[t] = true
		}
	}
}

// WithLatency sets a fixed latency for all executions.
func WithLatency(d time.Duration) MockOption {
	return func(m *MockExecutor) { m.latency = d }
}

// WithError sets an error to return for all executions.
func WithError(err error) MockOption {
	return func(m *MockExecutor) { m.err = err }
}

// WithResponse sets a fixed response for all executions.
func WithResponse(resp *tool.ToolResult) MockOption {
	return func(m *MockExecutor) { m.response = resp }
}

// WithFixedResult sets a fixed result (same as WithResponse).
func WithFixedResult(resp *tool.ToolResult) MockOption {
	return func(m *MockExecutor) { m.fixedResult = resp }
}

// WithCustomFunc sets a custom execution function.
func WithCustomFunc(fn func(context.Context, tool.ToolCall) (tool.ToolResult, error)) MockOption {
	return func(m *MockExecutor) { m.customFunc = fn }
}

// NewMockExecutor creates a new mock executor with the given options.
func NewMockExecutor(opts ...MockOption) *MockExecutor {
	m := &MockExecutor{
		name:      "mock",
		supported: make(map[string]bool),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Name returns the executor identifier.
func (m *MockExecutor) Name() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.name
}

// SupportsTool returns true if this executor can execute the given tool.
func (m *MockExecutor) SupportsTool(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.supported[name]
}

// Execute runs a tool call and returns the result.
// Behavior is controlled by the configured options.
func (m *MockExecutor) Execute(ctx context.Context, call tool.ToolCall) (tool.ToolResult, error) {
	m.mu.Lock()
	m.callCount++
	m.lastCall = &call
	m.mu.Unlock()

	// Simulate latency
	if m.latency > 0 {
		select {
		case <-ctx.Done():
			return tool.ToolResult{}, ctx.Err()
		case <-time.After(m.latency):
		}
	}

	// Custom function takes precedence
	if m.customFunc != nil {
		return m.customFunc(ctx, call)
	}

	// Fixed error
	if m.err != nil {
		return tool.ToolResult{}, m.err
	}

	// Fixed result
	if m.fixedResult != nil {
		result := *m.fixedResult
		result.CallID = call.ID
		return result, nil
	}

	// Fixed response
	if m.response != nil {
		result := *m.response
		result.CallID = call.ID
		return result, nil
	}

	// Default: echo the arguments
	return tool.ToolResult{
		CallID:   call.ID,
		Output:   map[string]any{"echo": call.Arguments},
		Duration: m.latency,
	}, nil
}

// CallCount returns the number of Execute calls.
func (m *MockExecutor) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

// LastCall returns the last tool call executed.
func (m *MockExecutor) LastCall() *tool.ToolCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastCall == nil {
		return nil
	}
	c := *m.lastCall
	return &c
}

// Reset clears call tracking.
func (m *MockExecutor) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount = 0
	m.lastCall = nil
}