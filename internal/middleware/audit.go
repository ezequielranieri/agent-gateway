package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// AuditStore is the interface for audit logging
type AuditStore interface {
	// Append adds an audit event
	Append(ctx context.Context, event *domain.AuditEvent) error
	// VerifyChain verifies the hash chain for a tenant
	VerifyChain(ctx context.Context, tenantID domain.UUID) (int64, error)
}

// AuditConfig holds configuration for the audit middleware
type AuditConfig struct {
	Store  AuditStore
	Logger zerolog.Logger
}

// NewAudit creates a new audit logging middleware
func NewAudit(cfg AuditConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			logger := cfg.Logger.With().Str("middleware", "audit").Logger()

			// Get context values
			tenantID, _ := GetTenantID(r)
			userID, _ := GetUserID(r)
			role, _ := GetRole(r)

			start := time.Now()

			// Pre-request audit event
			preEvent := &domain.AuditEvent{
				TenantID:    tenantID,
				ActorUserID: &userID,
				Action:      r.Method + " " + r.URL.Path,
				EntityType:  "http_request",
				Severity:    domain.AuditSeverityInfo,
				CreatedAt:   start,
			}

			// Append pre-event (fail-open)
			if cfg.Store != nil {
				if err := cfg.Store.Append(ctx, preEvent); err != nil {
					logger.Warn().Err(err).Msg("Failed to append pre-request audit event")
				}
			}

			// Wrap response writer to capture status code
			ww := &auditResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}

			next.ServeHTTP(ww, r)

			// Post-request audit event
			duration := time.Since(start)
			postEvent := &domain.AuditEvent{
				TenantID:    tenantID,
				ActorUserID: &userID,
				Action:      r.Method + " " + r.URL.Path,
				EntityType:  "http_response",
				Severity:    domain.AuditSeverityInfo,
				CreatedAt:   time.Now(),
			}

			// Add response metadata to payload
			// In production, serialize properly
			// For now, just append
			if cfg.Store != nil {
				if err := cfg.Store.Append(ctx, postEvent); err != nil {
					logger.Warn().Err(err).Msg("Failed to append post-request audit event")
				}
			}

			// Log request completion
			logger.Info().
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Int("status", ww.statusCode).
				Dur("duration", duration).
				Str("role", role).
				Msg("Request completed")
		})
	}
}

// auditResponseWriter wraps http.ResponseWriter to capture status code
type auditResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *auditResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}