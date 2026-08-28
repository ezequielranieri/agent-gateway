package middleware

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// RateLimiter is the domain rate limiter interface
type RateLimiter = domain.RateLimiter

// QuotaResolver resolves effective rate limits
type QuotaResolver = domain.QuotaResolver

// RateLimitConfig holds configuration for the rate limit middleware
type RateLimitConfig struct {
	Limiter      RateLimiter
	QuotaResolver QuotaResolver
	FailOpen     bool
	Logger       zerolog.Logger
}

// bucketConfig holds limit info for a bucket type
type bucketConfig struct {
	bucketType domain.BucketType
	limit      int
	window     time.Duration
	result     domain.RateLimitResult
}

// NewRateLimit creates a new rate limiting middleware
func NewRateLimit(cfg RateLimitConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = r.Context() // request context (not used for rate-limit metadata ops)
			logger := cfg.Logger.With().Str("middleware", "ratelimit").Logger()

			// Get tenant ID from context
			tenantID, ok := GetTenantID(r)
			if !ok {
				logger.Debug().Msg("No tenant_id in context")
				if cfg.FailOpen {
					next.ServeHTTP(w, r)
					return
				}
				WriteError(w, r, http.StatusUnauthorized, domain.ErrUnauthorized)
				return
			}

			// Get user ID from context (optional)
			userID, _ := GetUserID(r)

			// Get role from context (optional)
			roleStr, _ := GetRole(r)
			var roleID domain.UUID
			if roleStr != "" {
				// Role is a string name, we need to look up the role ID
				// For now, we'll skip role-based quota since we don't have role ID in context
				// TODO: Add role ID to auth claims or look it up
			}

			// Estimate token cost from request (for chat/completions)
			// This is a rough estimate; actual tokens counted after response
			estimatedTokens := estimateTokenCost(r)

			// Build cost for this request
			cost := domain.RateLimitCost{
				Requests:  1,
				Tokens:    estimatedTokens,
				ToolExecs: 0, // Will be updated if tools are used
			}

			// Use background context for quota/rate-limit metadata operations
			// to avoid test/framework deadline issues (rate limiting is a protection mechanism)
			bgCtx := context.Background()

			// Resolve effective limits for each bucket type (highest-limit-wins)
			buckets := []bucketConfig{
				{bucketType: domain.BucketTypeRequests},
				{bucketType: domain.BucketTypeTokens},
				{bucketType: domain.BucketTypeToolExecs},
			}

			for i := range buckets {
				limit, window, err := cfg.QuotaResolver.GetEffectiveLimit(bgCtx, tenantID, userID, roleID, buckets[i].bucketType)
				if err != nil {
					logger.Error().Err(err).Str("bucket", string(buckets[i].bucketType)).Msg("Failed to resolve quota")
					if !cfg.FailOpen {
						WriteError(w, r, http.StatusInternalServerError, domain.ErrRateLimited)
						return
					}
					// Fail-open: use a high default limit
					limit = 10000
					window = time.Minute
				}
				buckets[i].limit = limit
				buckets[i].window = window
			}

			// Check rate limit for each bucket
			for i := range buckets {
				key := domain.RateLimitKey{
					BucketType: buckets[i].bucketType,
					Scope:      domain.RateLimitScopeTenant,
					ID:         tenantID,
				}

				result, err := cfg.Limiter.Allow(bgCtx, key, cost, buckets[i].limit, buckets[i].window)
				if err != nil {
					logger.Error().Err(err).Str("bucket", string(buckets[i].bucketType)).Msg("Rate limiter error")
					if !cfg.FailOpen {
						WriteError(w, r, http.StatusInternalServerError, domain.ErrRateLimited)
						return
					}
					// Fail-open: allow
					result = domain.RateLimitResult{
						Allowed:   true,
						Remaining: buckets[i].limit,
						ResetAt:   time.Now().Add(buckets[i].window),
					}
				}
				buckets[i].result = result

				// If any bucket is exhausted, reject the request
				if !result.Allowed {
					logger.Debug().
						Str("bucket", string(buckets[i].bucketType)).
						Str("key", key.String()).
						Msg("Rate limit exceeded")

					// Set headers for all buckets before returning error
					setRateLimitHeaders(w, buckets)
					w.Header().Set("Retry-After", strconv.Itoa(int(result.RetryAfter.Seconds())))
					WriteError(w, r, http.StatusTooManyRequests, domain.ErrRateLimited)
					return
				}
			}

			// All buckets allowed - set headers and continue
			setRateLimitHeaders(w, buckets)
			next.ServeHTTP(w, r)
		})
	}
}

// setRateLimitHeaders sets the rate limit headers for all buckets
func setRateLimitHeaders(w http.ResponseWriter, buckets []bucketConfig) {
	for _, b := range buckets {
		prefix := bucketHeaderPrefix(b.bucketType)
		w.Header().Set(prefix+"Limit", strconv.Itoa(b.limit))
		w.Header().Set(prefix+"Remaining", strconv.Itoa(b.result.Remaining))
		w.Header().Set(prefix+"Reset", strconv.FormatInt(b.result.ResetAt.Unix(), 10))
	}
}

func bucketHeaderPrefix(bucketType domain.BucketType) string {
	switch bucketType {
	case domain.BucketTypeRequests:
		return "X-RateLimit-Limit-Requests"
	case domain.BucketTypeTokens:
		return "X-RateLimit-Limit-Tokens"
	case domain.BucketTypeToolExecs:
		return "X-RateLimit-Limit-ToolExecs"
	default:
		return "X-RateLimit-Limit"
	}
}

// estimateTokenCost estimates the token cost from the request
// This is a rough estimate; actual tokens will be counted after the response
func estimateTokenCost(r *http.Request) int {
	// For chat/completions, estimate based on max_tokens or a default
	// In practice, we'd parse the request body, but that consumes it
	// For now, return a conservative estimate
	return 1000 // Default estimate
}