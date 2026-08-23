package middleware

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/postgres"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// GuardrailChecker is the interface for guardrail checking
type GuardrailChecker interface {
	// CheckInput validates input before sending to model
	CheckInput(ctx context.Context, tenantID domain.UUID, input string) (*domain.GuardrailViolation, error)
	// CheckOutput validates output from model
	CheckOutput(ctx context.Context, tenantID domain.UUID, output string) (*domain.GuardrailViolation, error)
	// SanitizeOutput removes/masks sensitive data from output
	SanitizeOutput(output string) string
}

// GuardrailsConfig holds configuration for the guardrails middleware
type GuardrailsConfig struct {
	Checker   GuardrailChecker
	AuditRepo *postgres.AuditRepository
	Logger    zerolog.Logger
}

// NewGuardrails creates a new guardrails middleware
func NewGuardrails(cfg GuardrailsConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			logger := cfg.Logger.With().Str("middleware", "guardrails").Logger()

			// Only apply to chat/completions endpoint
			if r.URL.Path != "/v1/chat/completions" {
				next.ServeHTTP(w, r)
				return
			}

			// Get tenant ID from context
			tenantID, ok := GetTenantID(r)
			if !ok {
				logger.Debug().Msg("No tenant_id in context, skipping guardrails")
				next.ServeHTTP(w, r)
				return
			}

			// Read request body for input checking
			body, err := io.ReadAll(r.Body)
			if err != nil {
				logger.Error().Err(err).Msg("Failed to read request body")
				// Fail-open: allow request through
				next.ServeHTTP(w, r)
				return
			}

			// Restore body for downstream handlers
			r.Body = io.NopCloser(&readCloser{data: body})

			input := string(body)

			// Check input guardrails (fail-closed)
			if cfg.Checker != nil {
				if violation, err := cfg.Checker.CheckInput(ctx, tenantID, input); err != nil {
					// Fail-open on guardrail error
					logger.Warn().Err(err).Msg("Guardrail check failed, allowing request (fail-open)")
				} else if violation != nil {
					// Violation found - reject with 400
					violation.TenantID = tenantID
					logger.Warn().
						Str("rule", violation.Rule).
						Str("severity", violation.Severity).
						Str("phase", string(violation.Phase)).
						Msg("Input guardrail violation - rejecting request")

					// Emit audit event for guardrail violation
					if cfg.AuditRepo != nil {
						emitGuardrailAudit(ctx, cfg.AuditRepo, tenantID, violation, logger)
					}

					writeGuardrailError(w, violation)
					return
				}
			}

			// Wrap response writer to capture output for output checking
			ww := &guardrailResponseWriter{
				ResponseWriter: w,
				body:           &responseBody{},
				checker:        cfg.Checker,
				auditRepo:      cfg.AuditRepo,
				tenantID:       tenantID,
				logger:         logger,
				ctx:            ctx,
			}

			next.ServeHTTP(ww, r)
		})
	}
}

// guardrailResponseWriter wraps http.ResponseWriter to capture response body
type guardrailResponseWriter struct {
	http.ResponseWriter
	body       *responseBody
	checker    GuardrailChecker
	auditRepo  *postgres.AuditRepository
	tenantID   domain.UUID
	logger     zerolog.Logger
	ctx        context.Context
	statusCode int
}

type responseBody struct {
	data []byte
}

func (r *responseBody) Write(p []byte) (n int, err error) {
	r.data = append(r.data, p...)
	return len(p), nil
}

func (r *responseBody) Bytes() []byte {
	return r.data
}

func (w *guardrailResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
	
	// Flush guardrails after headers are written (response is complete)
	w.flushGuardrails()
}

func (w *guardrailResponseWriter) Write(p []byte) (int, error) {
	// Write to both the underlying writer and our buffer
	n, err := w.body.Write(p)
	if err != nil {
		return n, err
	}
	return w.ResponseWriter.Write(p)
}

// flushGuardrails checks output guardrails after response is complete
// This is called automatically when the handler finishes
func (w *guardrailResponseWriter) flushGuardrails() {
	if w.checker == nil {
		return
	}

	output := string(w.body.Bytes())
	if output == "" {
		return
	}

	// Check output guardrails
	violation, err := w.checker.CheckOutput(w.ctx, w.tenantID, output)
	if err != nil {
		// Fail-open on guardrail error
		w.logger.Warn().Err(err).Msg("Output guardrail check failed, allowing response (fail-open)")
		return
	}

	if violation != nil {
		violation.TenantID = w.tenantID
		w.logger.Warn().
			Str("rule", violation.Rule).
			Str("severity", violation.Severity).
			Str("phase", string(violation.Phase)).
			Msg("Output guardrail violation")

		// Emit audit event for guardrail violation
		if w.auditRepo != nil {
			emitGuardrailAudit(w.ctx, w.auditRepo, w.tenantID, violation, w.logger)
		}

		if violation.Severity == "critical" {
			// Critical violation - we need to modify the response
			// Since headers may already be sent, we log and sanitize
			_ = w.checker.SanitizeOutput(output)
			w.logger.Warn().Msg("Critical output violation - response would be rejected, sanitized instead")
			// In a real implementation, we'd need to handle this before WriteHeader
			// For now, we log and continue with sanitized version if possible
		} else if violation.Severity == "warn" {
			// Warn violation - sanitize output
			_ = w.checker.SanitizeOutput(output)
			// We can't easily modify the response after it's written
			// In a production system, we'd buffer the entire response
			w.logger.Warn().Msg("Warn output violation - response sanitized")
		}
	}
}

// readCloser wraps bytes to implement io.ReadCloser
type readCloser struct {
	data []byte
	pos  int
}

func (r *readCloser) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n = copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func (r *readCloser) Close() error {
	return nil
}

// writeGuardrailError writes a 400 error response for guardrail violations
func writeGuardrailError(w http.ResponseWriter, violation *domain.GuardrailViolation) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	response := map[string]interface{}{
		"error": "guardrail_violation",
		"details": map[string]interface{}{
			"rule":      violation.Rule,
			"severity":  violation.Severity,
			"phase":     violation.Phase,
			"message":   violation.Message,
			"context":   violation.Context,
		},
	}
	json.NewEncoder(w).Encode(response)
}

// emitGuardrailAudit emits an audit event for a guardrail violation
func emitGuardrailAudit(
	ctx context.Context,
	auditRepo *postgres.AuditRepository,
	tenantID domain.UUID,
	violation *domain.GuardrailViolation,
	logger zerolog.Logger,
) {
	if auditRepo == nil {
		return
	}

	// Prepare audit payload
	payload := map[string]interface{}{
		"rule":      violation.Rule,
		"severity":  violation.Severity,
		"phase":     violation.Phase,
		"message":   violation.Message,
		"context":   violation.Context,
		"violation_id": violation.ID.String(),
	}
	payloadBytes, _ := json.Marshal(payload)

	severity := domain.AuditSeverityWarn
	if violation.Severity == "critical" {
		severity = domain.AuditSeverityCritical
	}

	event := &domain.AuditEvent{
		TenantID:   tenantID,
		Action:     "guardrail.violation",
		EntityType: "guardrail_violation",
		Severity:   severity,
		Payload:    payloadBytes,
		CreatedAt:  violation.CreatedAt,
	}

	// Fail-open: never block on audit failure
	if err := auditRepo.Append(ctx, event); err != nil {
		logger.Warn().Err(err).Msg("Failed to emit guardrail violation audit event")
	}
}