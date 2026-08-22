package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// RateLimiter is the interface for rate limiting
type RateLimiter interface {
	// Allow checks if a request is allowed and returns the limit info
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, int, int, time.Duration, error)
	// AllowN checks if N requests are allowed (for token consumption)
	AllowN(ctx context.Context, key string, n int, limit int, window time.Duration) (bool, int, int, time.Duration, error)
}

// RateLimitConfig holds configuration for the rate limit middleware
type RateLimitConfig struct {
	Limiter      RateLimiter
	DefaultLimit int
	Window       time.Duration
	FailOpen     bool
	Logger       zerolog.Logger
}

// NewRateLimit creates a new rate limiting middleware
func NewRateLimit(cfg RateLimitConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			logger := cfg.Logger.With().Str("middleware", "ratelimit").Logger()

			// Get tenant ID from context
			tenantID, ok := GetTenantID(r)
			if !ok {
				logger.Debug().Msg("No tenant_id in context")
				// If fail open, allow; otherwise reject
				if cfg.FailOpen {
					next.ServeHTTP(w, r)
					return
				}
				WriteError(w, r, http.StatusUnauthorized, domain.ErrUnauthorized)
				return
			}

			// Get user ID from context (optional)
			userID, _ := GetUserID(r)

			// Build rate limit key (tenant:user)
			key := "ratelimit:" + tenantID.String()
			if !userID.IsZero() {
				key += ":" + userID.String()
			}

			// Check rate limit
			allowed, remaining, reset, retryAfter, err := cfg.Limiter.Allow(ctx, key, cfg.DefaultLimit, cfg.Window)
			if err != nil {
				logger.Error().Err(err).Msg("Rate limiter error")
				if cfg.FailOpen {
					next.ServeHTTP(w, r)
					return
				}
				WriteError(w, r, http.StatusInternalServerError, domain.ErrRateLimited)
				return
			}

			// Set rate limit headers
			w.Header().Set("X-RateLimit-Limit", string(rune(cfg.DefaultLimit)))
			w.Header().Set("X-RateLimit-Remaining", string(rune(remaining)))
			w.Header().Set("X-RateLimit-Reset", string(rune(reset)))

			if !allowed {
				logger.Debug().Str("key", key).Msg("Rate limit exceeded")
				w.Header().Set("Retry-After", string(rune(int(retryAfter.Seconds()))))
				WriteError(w, r, http.StatusTooManyRequests, domain.ErrRateLimited)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}