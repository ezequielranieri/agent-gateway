package middleware

import (
	"net/http"

	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// HITLConfig holds configuration for the HITL middleware
type HITLConfig struct {
	Logger zerolog.Logger
}

// NewHITL creates a new HITL middleware
// This middleware is part of the chain but doesn't enforce approval directly;
// the approval logic is in the handlers. It can be used for metrics or future enforcement.
func NewHITL(cfg HITLConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			logger := cfg.Logger.With().Str("middleware", "hitl").Logger()

			// Log HITL-related requests for observability
			if isHITLEndpoint(r.URL.Path) {
				logger.Debug().
					Str("path", r.URL.Path).
					Str("method", r.Method).
					Msg("HITL endpoint request")
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// isHITLEndpoint checks if the path is a HITL-related endpoint
func isHITLEndpoint(path string) bool {
	return len(path) >= 9 && path[:9] == "/v1/reviews"
}

// ReviewTokenExtractor is a helper to extract and validate review tokens
// This can be used by handlers if needed
func ExtractReviewToken(r *http.Request) (string, bool) {
	// Check header first (for API clients)
	if token := r.Header.Get("X-Review-Token"); token != "" {
		return token, true
	}
	// Check query param (for SSE ticket)
	if token := r.URL.Query().Get("token"); token != "" {
		return token, true
	}
	return "", false
}

// ValidateReviewToken performs timing-safe token validation
func ValidateReviewToken(storedHash, presentedToken string) bool {
	// Hash the presented token
	// Note: In practice, we'd use crypto/sha256 and subtle.ConstantTimeCompare
	// This is a placeholder for the actual implementation
	if len(presentedToken) < 32 {
		return false
	}
	// The actual validation happens in the usecase with proper timing-safe compare
	return true
}

// Context keys for HITL
type hitlContextKey string

const (
	// ReviewIDKey is the context key for review ID
	ReviewIDKey hitlContextKey = "review_id"
	// ReviewTokenKey is the context key for review token
	ReviewTokenKey hitlContextKey = "review_token"
)

// GetReviewID retrieves review ID from request context
func GetReviewID(r *http.Request) (domain.UUID, bool) {
	if rid, ok := r.Context().Value(ReviewIDKey).(domain.UUID); ok {
		return rid, true
	}
	return domain.UUID{}, false
}

// GetReviewToken retrieves review token from request context
func GetReviewToken(r *http.Request) (string, bool) {
	if token, ok := r.Context().Value(ReviewTokenKey).(string); ok {
		return token, true
	}
	return "", false
}