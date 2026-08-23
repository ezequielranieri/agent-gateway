package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/postgres"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// AuditStore is the interface for audit logging
type AuditStore interface {
	// Append adds an audit event
	Append(ctx context.Context, event *domain.AuditEvent) error
	// VerifyChain verifies the hash chain for a tenant
	VerifyChain(ctx context.Context, tenantID domain.UUID, fromSeq, toSeq int64) (*postgres.VerifyResult, error)
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

			start := time.Now()

			// Pre-request audit event
			preEvent := &domain.AuditEvent{
				TenantID:    tenantID,
				ActorUserID: &userID,
				Action:      r.Method + " " + r.URL.Path,
				EntityType:  "http_request",
				Severity:    domain.AuditSeverityInfo,
				CreatedAt:   start,
				Payload:     json.RawMessage(`{}`),
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

			// Determine severity based on status code
			severity := domain.AuditSeverityInfo
			if ww.statusCode >= 500 {
				severity = domain.AuditSeverityCritical
			} else if ww.statusCode >= 400 {
				severity = domain.AuditSeverityWarn
			}

			// Create response payload with metadata
			responsePayload := map[string]interface{}{
				"status_code": ww.statusCode,
				"duration_ms": duration.Milliseconds(),
			}
			payloadBytes, _ := json.Marshal(responsePayload)

			postEvent := &domain.AuditEvent{
				TenantID:    tenantID,
				ActorUserID: &userID,
				Action:      r.Method + " " + r.URL.Path,
				EntityType:  "http_response",
				Severity:    severity,
				CreatedAt:   time.Now(),
				Payload:     payloadBytes,
			}

			// Append post-event (fail-open)
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