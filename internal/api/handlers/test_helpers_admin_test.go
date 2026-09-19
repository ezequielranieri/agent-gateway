package handlers

import (
	"crypto/rand"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/jwt"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// getTestDSN gets the test database DSN from environment
func getTestDSN(t *testing.T) string {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") == "true" {
			t.Fatal("TEST_DATABASE_URL must be set when running in CI")
		}
		t.Skip("TEST_DATABASE_URL not set, skipping integration test")
	}
	return dsn
}

// newTestJWTService creates a test JWT service with a random symmetric key
func newTestJWTService(t *testing.T) *jwt.AuthService {
	signingKey := make([]byte, 32)
	_, err := rand.Read(signingKey)
	require.NoError(t, err)

	keyStore := jwt.NewKeyStore("v1", signingKey, map[string][]byte{
		"v1": signingKey,
	})
	return jwt.NewAuthService(keyStore, "test-issuer", "test-audience")
}

// createTestToken creates a test JWT token with specified claims
func createTestToken(t *testing.T, userID, tenantID domain.UUID, role string, scopes []string) string {
	svc := newTestJWTService(t)

	claims := jwt.Claims{
		UserID:   userID.String(),
		TenantID: tenantID.String(),
		Role:     role,
		Scopes:   scopes,
	}

	token, err := svc.IssueAccessToken(claims)
	require.NoError(t, err)
	return token
}