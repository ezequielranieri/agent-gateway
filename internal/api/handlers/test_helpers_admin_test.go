package handlers

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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

// newTestJWTServiceWithKey creates a test JWT service with a given key
func newTestJWTServiceWithKey(t *testing.T, signingKey []byte) *jwt.AuthService {
	keyStore := jwt.NewKeyStore("v1", signingKey, map[string][]byte{
		"v1": signingKey,
	})
	return jwt.NewAuthService(keyStore, "test-issuer", "test-audience")
}

// createTestToken creates a test JWT token with specified claims
func createTestToken(t *testing.T, userID, tenantID domain.UUID, role string, scopes []string) string {
	signingKey := make([]byte, 32)
	_, err := rand.Read(signingKey)
	require.NoError(t, err)

	svc := newTestJWTServiceWithKey(t, signingKey)

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

// createTestTokenWithService creates a test JWT token using a shared JWT service
func createTestTokenWithService(t *testing.T, svc *jwt.AuthService, userID, tenantID domain.UUID, role string, scopes []string) string {
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

// createTestTool inserts a tool definition into the database for testing
func createTestTool(ctx context.Context, pool *pgxpool.Pool, tenantID domain.UUID, name, description string, parameters, grants json.RawMessage, timeoutMs uint64, memoryPages uint32, hash string) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO public.tool_definitions (tenant_id, name, description, input_schema, grants, execution_timeout_ms, memory_pages, hash, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, true)
		ON CONFLICT (tenant_id, name) WHERE is_active = true DO UPDATE SET
			description = EXCLUDED.description,
			input_schema = EXCLUDED.input_schema,
			grants = EXCLUDED.grants,
			execution_timeout_ms = EXCLUDED.execution_timeout_ms,
			memory_pages = EXCLUDED.memory_pages,
			hash = EXCLUDED.hash,
			is_active = EXCLUDED.is_active,
			updated_at = now()
	`, tenantID, name, description, parameters, grants, timeoutMs, memoryPages, hash)
	return err
}

// getTestWASMPath returns the path to a test WASM module
func getTestWASMPath(t *testing.T, name string) string {
	// From package directory (internal/api/handlers/), need ../../../
	path := "../../../test/wasm/" + name
	if _, err := os.Stat(path); err == nil {
		return path
	}
	// Fallback: from project root
	path = "test/wasm/" + name
	if _, err := os.Stat(path); err == nil {
		return path
	}
	t.Fatalf("WASM test file not found: %s", name)
	return ""
}