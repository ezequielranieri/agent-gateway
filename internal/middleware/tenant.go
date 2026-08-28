package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// TenantConfig holds configuration for the tenant middleware
type TenantConfig struct {
	Pool  *pgxpool.Pool
	Logger zerolog.Logger
}

// NewTenant creates a new tenant validation middleware
// It validates the tenant exists and is active. GUC is set in WithTenant/handlers.
func NewTenant(cfg TenantConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			logger := cfg.Logger.With().Str("middleware", "tenant").Logger()

			// Get tenant ID from auth claims
			tenantID, ok := GetTenantID(r)
			if !ok {
				logger.Debug().Msg("No tenant_id in context")
				WriteError(w, r, http.StatusUnauthorized, domain.ErrUnauthorized)
				return
			}

			// DIAGNOSTIC: Log context deadline and pool stats
			deadline, hasDeadline := ctx.Deadline()
			poolStats := cfg.Pool.Stat()
			logger.Debug().
				Str("tenant_id", tenantID.String()).
				Bool("ctx_has_deadline", hasDeadline).
				Time("ctx_deadline", deadline).
				Int("pool_acquired", int(poolStats.AcquiredConns())).
				Int("pool_idle", int(poolStats.IdleConns())).
				Int("pool_total", int(poolStats.TotalConns())).
				Msg("Tenant middleware start")

			start := time.Now()

			// Validate tenant exists and is active
			// Use background context for metadata lookup - shouldn't fail due to client request timeout
			var status string
			dbCtx := context.Background()
			err := cfg.Pool.QueryRow(dbCtx, `
				SELECT status FROM public.tenants WHERE id = $1
			`, tenantID).Scan(&status)

			queryDuration := time.Since(start)
			logger.Debug().
				Str("tenant_id", tenantID.String()).
				Dur("query_duration", queryDuration).
				Msg("Tenant lookup query completed")

			if err != nil {
				logger.Error().
					Err(err).
					Str("tenant_id", tenantID.String()).
					Dur("query_duration", queryDuration).
					Msg("Tenant lookup failed")
				WriteError(w, r, http.StatusNotFound, domain.ErrNotFound)
				return
			}

			if status != "active" {
				logger.Warn().
					Str("tenant_id", tenantID.String()).
					Str("status", status).
					Msg("Tenant not active")
				WriteError(w, r, http.StatusForbidden, domain.ErrForbidden)
				return
			}

			// NOTE: Do NOT set tenant GUC here.
			// The GUC must be set LOCAL to the transaction (in WithTenant / handlers)
			// so RLS policies work correctly for the actual request transaction.
			// This middleware only validates tenant exists and is active.

			totalDuration := time.Since(start)
			logger.Debug().
				Str("tenant_id", tenantID.String()).
				Dur("total_duration", totalDuration).
				Msg("Tenant middleware completed (validation only)")

			// Add tenant to context for downstream use
			ctx = context.WithValue(ctx, TenantIDKey, tenantID)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}