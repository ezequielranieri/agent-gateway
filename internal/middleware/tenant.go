package middleware

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// TenantConfig holds configuration for the tenant middleware
type TenantConfig struct {
	Pool  *pgxpool.Pool
	Logger zerolog.Logger
}

// NewTenant creates a new tenant resolution middleware
// It validates the tenant exists and is active, then sets the tenant GUC for RLS
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

			// Validate tenant exists and is active
			var status string
			err := cfg.Pool.QueryRow(ctx, `
				SELECT status FROM public.tenants WHERE id = $1
			`, tenantID).Scan(&status)

			if err != nil {
				logger.Debug().Err(err).Msg("Tenant lookup failed")
				WriteError(w, r, http.StatusNotFound, domain.ErrNotFound)
				return
			}

			if status != "active" {
				logger.Debug().Str("status", status).Msg("Tenant not active")
				WriteError(w, r, http.StatusForbidden, domain.ErrForbidden)
				return
			}

			// Set the tenant context GUC for RLS
			// Use WITH CHECK (true) to make it LOCAL to this transaction
			_, err = cfg.Pool.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenantID.String())
			if err != nil {
				logger.Error().Err(err).Msg("Failed to set tenant GUC")
				WriteError(w, r, http.StatusInternalServerError, domain.ErrRLSViolation)
				return
			}

			// Add tenant to context for downstream use
			ctx = context.WithValue(ctx, TenantIDKey, tenantID)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}