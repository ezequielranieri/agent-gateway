package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog"

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

// Claims represents the JWT claims
type Claims struct {
	UserID   string   `json:"sub"`
	TenantID string   `json:"tenant_id"`
	Role     string   `json:"role"`
	Scopes   []string `json:"scopes"`
	jwt.RegisteredClaims
}

// AuthConfig holds configuration for the auth middleware
type AuthConfig struct {
	Secret       string
	Issuer       string
	Audience     string
	KeyRotation  KeyRotationConfig
	Logger       zerolog.Logger
}

// KeyRotationConfig holds key rotation settings
type KeyRotationConfig struct {
	Enabled    bool
	CurrentKID string
	Keys       map[string]string
}

// NewAuth creates a new authentication middleware
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

			// Parse and validate token
			claims, err := parseAndValidateToken(tokenString, cfg)
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

			tenantID, err := domain.ParseUUID(claims.TenantID)
			if err != nil {
				logger.Debug().Err(err).Msg("Invalid tenant_id in token")
				WriteError(w, r, http.StatusUnauthorized, domain.ErrInvalidToken)
				return
			}

			// Add claims to context
			ctx = context.WithValue(ctx, ClaimsKey, claims)
			ctx = context.WithValue(ctx, UserIDKey, userID)
			ctx = context.WithValue(ctx, TenantIDKey, tenantID)
			ctx = context.WithValue(ctx, RoleKey, claims.Role)
			ctx = context.WithValue(ctx, ScopesKey, claims.Scopes)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// parseAndValidateToken parses and validates a JWT token with key rotation support
func parseAndValidateToken(tokenString string, cfg AuthConfig) (*Claims, error) {
	// Try current key first
	key := cfg.Secret
	if cfg.KeyRotation.Enabled && cfg.KeyRotation.CurrentKID != "" {
		if k, ok := cfg.KeyRotation.Keys[cfg.KeyRotation.CurrentKID]; ok {
			key = k
		}
	}

	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		// Verify algorithm is HS256
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, domain.ErrInvalidToken
		}
		return []byte(key), nil
	}, jwt.WithIssuer(cfg.Issuer), jwt.WithAudience(cfg.Audience))

	if err == nil && token.Valid {
		if claims, ok := token.Claims.(*Claims); ok {
			return claims, nil
		}
	}

	// If key rotation enabled, try other keys
	if cfg.KeyRotation.Enabled {
		for kid, k := range cfg.KeyRotation.Keys {
			if kid == cfg.KeyRotation.CurrentKID {
				continue // Already tried
			}
			token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
				if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, domain.ErrInvalidToken
				}
				return []byte(k), nil
			}, jwt.WithIssuer(cfg.Issuer), jwt.WithAudience(cfg.Audience))
			if err == nil && token.Valid {
				if claims, ok := token.Claims.(*Claims); ok {
					return claims, nil
				}
			}
		}
	}

	return nil, domain.ErrInvalidToken
}

// GetClaims retrieves claims from request context
func GetClaims(r *http.Request) *Claims {
	if claims, ok := r.Context().Value(ClaimsKey).(*Claims); ok {
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