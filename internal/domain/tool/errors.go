package tool

import "errors"

// Sentinel errors for tool execution
var (
	// ErrToolNotFound is returned when a tool is not registered
	ErrToolNotFound = errors.New("tool not found")

	// ErrToolTimeout is returned when tool execution exceeds time limit
	ErrToolTimeout = errors.New("tool execution timeout")

	// ErrToolResourceExhausted is returned when tool exceeds fuel/memory limits
	ErrToolResourceExhausted = errors.New("tool resource exhausted (fuel/memory)")

	// ErrToolExecutionFailed is returned when tool execution fails
	ErrToolExecutionFailed = errors.New("tool execution failed")

	// ErrToolNotAllowed is returned when tool execution is rejected (e.g., HITL)
	ErrToolNotAllowed = errors.New("tool not allowed (HITL rejected)")

	// ErrMaxIterationsExceeded is returned when max tool iterations exceeded
	ErrMaxIterationsExceeded = errors.New("max iterations exceeded")
)