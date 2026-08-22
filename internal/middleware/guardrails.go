package middleware

import (
	"context"
	"net/http"

	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// GuardrailChecker is the interface for guardrail checking
type GuardrailChecker interface {
	// CheckInput validates input before sending to model
	CheckInput(ctx context.Context, tenantID domain.UUID, input string) (*domain.GuardrailViolation, error)
	// CheckOutput validates output from model
	CheckOutput(ctx context.Context, tenantID domain.UUID, output string) (*domain.GuardrailViolation, error)
}

// GuardrailsConfig holds configuration for the guardrails middleware
type GuardrailsConfig struct {
	Checker GuardrailChecker
	Logger  zerolog.Logger
}

// NewGuardrails creates a new guardrails middleware
func NewGuardrails(cfg GuardrailsConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = r.Context()
			logger := cfg.Logger.With().Str("middleware", "guardrails").Logger()

			// Only apply to chat/completions endpoint
			if r.URL.Path != "/v1/chat/completions" {
				next.ServeHTTP(w, r)
				return
			}

			// Get tenant ID from context
			_, ok := GetTenantID(r)
			if !ok {
				logger.Debug().Msg("No tenant_id in context")
				next.ServeHTTP(w, r)
				return
			}

			// For now, skip actual guardrail checking (skeleton)
			// In implementation, we would:
			// 1. Read request body
			// 2. Call cfg.Checker.CheckInput(ctx, tenantID, body)
			// 3. If violation, return 400 with violation details
			// 4. For output, wrap response writer and check on write

			next.ServeHTTP(w, r)
		})
	}
}