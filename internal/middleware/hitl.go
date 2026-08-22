package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// ReviewStore is the interface for review requests
type ReviewStore interface {
	// GetByToken retrieves a review request by token hash
	GetByToken(ctx context.Context, tokenHash string) (*domain.ReviewRequest, error)
	// Update updates a review request
	Update(ctx context.Context, req *domain.ReviewRequest) error
}

// HITLConfig holds configuration for the HITL middleware
type HITLConfig struct {
	Store  ReviewStore
	Logger zerolog.Logger
}

// NewHITL creates a new HITL middleware for review token validation
func NewHITL(cfg HITLConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = r.Context()
			logger := cfg.Logger.With().Str("middleware", "hitl").Logger()

			// Only apply to write endpoints that require approval
			// Check if path matches review endpoints
			if !strings.HasPrefix(r.URL.Path, "/v1/reviews/") {
				next.ServeHTTP(w, r)
				return
			}

			// For approve/reject endpoints, validate review token
			// Path pattern: /v1/reviews/{id}/approve or /v1/reviews/{id}/reject
			if strings.HasSuffix(r.URL.Path, "/approve") || strings.HasSuffix(r.URL.Path, "/reject") {
				// Extract review token from header
				reviewToken := r.Header.Get("X-Review-Token")
				if reviewToken == "" {
					logger.Debug().Msg("Missing X-Review-Token header")
					WriteError(w, r, http.StatusBadRequest, domain.ErrValidation)
					return
				}

				// Hash the token (SHA-256) to compare with stored hash
				// In implementation: tokenHash := sha256.Sum256([]byte(reviewToken))
				// For skeleton, just validate format
				if len(reviewToken) < 32 {
					logger.Debug().Msg("Invalid review token format")
					WriteError(w, r, http.StatusBadRequest, domain.ErrValidation)
					return
				}

				// Look up review request
				// review, err := cfg.Store.GetByToken(ctx, tokenHash)
				// Validate: pending, not expired, correct tenant, correct action
				// On approve: re-validate payload, materialize action, mark EXECUTED
				// On reject: mark REJECTED

				// Skeleton: just continue
			}

			next.ServeHTTP(w, r)
		})
	}
}