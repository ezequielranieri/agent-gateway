package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/jwt"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// Context keys for auth middleware
type contextKey string

const (
	// ClaimsKey is the context key for JWT claims
	ClaimsKey contextKey = "claims"
	// UserIDKey is the context key for user ID
	UserIDKey contextKey = "user_id"
	// TenantIDKey is the context key for tenant ID
	TenantIDKey contextKey = "tenant_id"
	// RoleKey is the context key for role
	RoleKey contextKey = "role"
	// ScopesKey is the context key for scopes
	ScopesKey contextKey = "scopes"
)

// AuthConfig holds configuration for the auth middleware
type AuthConfig struct {
	JWTService *jwt.AuthService
	Logger     zerolog.Logger
}

// NewAuth creates a new authentication middleware using the real JWT adapter
func NewAuth(cfg AuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			logger := cfg.Logger.With().Str("middleware", "auth").Logger()

			// Extract token from Authorization header
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				logger.Debug().Msg("Missing Authorization header")
				WriteError(w, r, http.StatusUnauthorized, domain.ErrUnauthorized)
				return
			}

			// Expect "Bearer <token>"
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				logger.Debug().Msg("Invalid Authorization header format")
				WriteError(w, r, http.StatusUnauthorized, domain.ErrUnauthorized)
				return
			}

			tokenString := parts[1]

			// Parse and validate token using real JWT adapter
			claims, err := cfg.JWTService.ParseAccessToken(tokenString)
			if err != nil {
				logger.Debug().Err(err).Msg("Token validation failed")
				WriteError(w, r, http.StatusUnauthorized, domain.ErrUnauthorized)
				return
			}

			// Parse UUIDs
			userID, err := domain.ParseUUID(claims.UserID)
			if err != nil {
				logger.Debug().Err(err).Msg("Invalid user_id in token")
				WriteError(w, r, http.StatusUnauthorized, domain.ErrInvalidToken)
				return
			}

			// TenantID is optional (SuperAdmin tokens don't have it)
			var tenantID domain.UUID
			if claims.TenantID != "" {
				tenantID, err = domain.ParseUUID(claims.TenantID)
				if err != nil {
					logger.Debug().Err(err).Msg("Invalid tenant_id in token")
					WriteError(w, r, http.StatusUnauthorized, domain.ErrInvalidToken)
					return
				}
			}

			// Add claims to context
			ctx = context.WithValue(ctx, ClaimsKey, claims)
			ctx = context.WithValue(ctx, UserIDKey, userID)
			if claims.TenantID != "" {
				ctx = context.WithValue(ctx, TenantIDKey, tenantID)
			}
			ctx = context.WithValue(ctx, RoleKey, claims.Role)
			ctx = context.WithValue(ctx, ScopesKey, claims.Scopes)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetClaims retrieves claims from request context
func GetClaims(r *http.Request) *jwt.Claims {
	if claims, ok := r.Context().Value(ClaimsKey).(*jwt.Claims); ok {
		return claims
	}
	return nil
}

// GetUserID retrieves user ID from request context
func GetUserID(r *http.Request) (domain.UUID, bool) {
	if uid, ok := r.Context().Value(UserIDKey).(domain.UUID); ok {
		return uid, true
	}
	return domain.UUID{}, false
}

// GetTenantID retrieves tenant ID from request context
func GetTenantID(r *http.Request) (domain.UUID, bool) {
	if tid, ok := r.Context().Value(TenantIDKey).(domain.UUID); ok {
		return tid, true
	}
	return domain.UUID{}, false
}

// GetRole retrieves role from request context
func GetRole(r *http.Request) (string, bool) {
	if role, ok := r.Context().Value(RoleKey).(string); ok {
		return role, true
	}
	return "", false
}

// GetScopes retrieves scopes from request context
func GetScopes(r *http.Request) ([]string, bool) {
	if scopes, ok := r.Context().Value(ScopesKey).([]string); ok {
		return scopes, true
	}
	return nil, false
}

// WriteError writes a JSON error response
func WriteError(w http.ResponseWriter, r *http.Request, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// In production, use a proper error response structure
	// For now, just write the error message
	_, _ = w.Write([]byte(`{"error":"` + err.Error() + `"}`))
}