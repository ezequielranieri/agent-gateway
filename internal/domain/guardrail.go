package domain

import (
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
	ID          UUID            `json:"id"`
	TenantID    UUID            `json:"tenant_id"`
	Phase       GuardrailPhase  `json:"phase"`
	Rule        string          `json:"rule"`         // Rule name that triggered
	Severity    string          `json:"severity"`     // info/warn/critical
	Message     string          `json:"message"`      // Human-readable description
	Context     string          `json:"context"`      // JSON context of the violation
	CreatedAt   time.Time       `json:"created_at"`
}

// Guardrail interface for input/output validation
type Guardrail interface {
	// CheckInput validates input before sending to model
	CheckInput(tenantID UUID, input string) (*GuardrailViolation, error)
	// CheckOutput validates output from model
	CheckOutput(tenantID UUID, output string) (*GuardrailViolation, error)
}

// Violation represents a guardrail violation (alias for consistency)
type Violation = GuardrailViolation