package domain

import (
	"context"
	"time"
)

// GuardrailPhase represents when the guardrail check occurs
type GuardrailPhase string

const (
	GuardrailPhaseInput  GuardrailPhase = "input"
	GuardrailPhaseOutput GuardrailPhase = "output"
)

// GuardrailViolation represents a guardrail violation
type GuardrailViolation struct {
	ID         UUID            `json:"id"`
	TenantID   UUID            `json:"tenant_id"`
	RequestID  *UUID           `json:"request_id,omitempty"` // Reference to audit event or review request
	Phase      GuardrailPhase  `json:"phase"`
	Rule       string          `json:"rule"`         // Rule name that triggered
	Severity   string          `json:"severity"`     // info/warn/critical
	Message    string          `json:"message"`      // Human-readable description
	Context    string          `json:"context"`      // JSON context of the violation
	CreatedAt  time.Time       `json:"created_at"`
}

// Guardrail interface for input/output validation
type Guardrail interface {
	// CheckInput validates input before sending to model
	CheckInput(ctx context.Context, tenantID UUID, input string) (*GuardrailViolation, error)
	// CheckOutput validates output from model
	CheckOutput(ctx context.Context, tenantID UUID, output string) (*GuardrailViolation, error)
	// SanitizeOutput removes/masks sensitive data from output
	SanitizeOutput(output string) string
}

// Violation represents a guardrail violation (alias for consistency)
type Violation = GuardrailViolation

// Now returns the current time (for testability, can be overridden in tests)
var Now = time.Now