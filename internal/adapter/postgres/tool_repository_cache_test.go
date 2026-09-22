//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/domain/tool"
)

func TestToolRepository_CacheBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	dbPool := setupTestDB(t)
	defer dbPool.Close()

	logger := zerolog.New(zerolog.NewTestWriter(t)).Level(zerolog.DebugLevel)
	auditRepo := NewAuditRepository(dbPool)

	// Create tool repository with cache config
	cacheTTL := 5 * time.Minute
	cacheMaxEntries := 1000
	repo := NewToolRepository(dbPool, auditRepo, logger, cacheTTL, cacheMaxEntries)

	tenantID := domain.MustParseUUID("11111111-1111-1111-1111-111111111111")

	// Ensure test tenant exists (FK requirement for tool_definitions)
	require.NoError(t, ensureTestTenant(ctx, dbPool, tenantID))

	toolName := "cache_test_tool"
	description := "Cache test tool"
	parameters := json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}}}`)
	grants := json.RawMessage(`["filesystem:read"]`)
	timeoutMs := uint64(30000) // 30 seconds in milliseconds (replaces fuel_limit)
	memoryPages := uint32(256)
	hash := tool.ComputeHash(toolName, description, parameters)

	t.Run("Cache miss on first lookup", func(t *testing.T) {
		// Create tool directly in DB
		err := createTestTool(ctx, dbPool, tenantID, toolName, description, parameters, grants, timeoutMs, memoryPages, hash)
		require.NoError(t, err)

		// First lookup - should be cache miss
		def, err := repo.GetByName(ctx, tenantID, toolName)
		require.NoError(t, err)
		assert.Equal(t, toolName, def.Name)
		assert.Equal(t, hash, def.Hash)
	})

	t.Run("Cache hit on second lookup", func(t *testing.T) {
		// Second lookup - should be cache hit (no DB query)
		def, err := repo.GetByName(ctx, tenantID, toolName)
		require.NoError(t, err)
		assert.Equal(t, toolName, def.Name)
		assert.Equal(t, hash, def.Hash)
	})

	t.Run("Cache tenant isolation", func(t *testing.T) {
		// Create same tool name in different tenant
		tenantID2 := domain.MustParseUUID("22222222-2222-2222-2222-222222222222")
		err := createTestTool(ctx, dbPool, tenantID2, toolName, "Different description", parameters, grants, timeoutMs, memoryPages, hash)
		require.NoError(t, err)

		// Lookup in tenant2 - should be cache miss (different tenant key)
		def, err := repo.GetByName(ctx, tenantID2, toolName)
		require.NoError(t, err)
		assert.Equal(t, "Different description", def.Description)
	})

	t.Run("Cache invalidated on UpdateToolDefinition", func(t *testing.T) {
		// Update tool
		newDescription := "Updated description"
		newHash := tool.ComputeHash(toolName, newDescription, parameters)

		hashChanged, err := repo.UpdateToolDefinition(ctx, tenantID, &tool.ToolDefinition{
			ID:                  1, // placeholder, will be ignored (looked up by name)
			TenantID:            tenantID,
			Name:                toolName,
			Description:         newDescription,
			InputSchema:         parameters,
			Grants:              grants,
			ExecutionTimeoutMs:  timeoutMs,
			MemoryPages:         memoryPages,
			Hash:                newHash,
			IsActive:            true,
		})
		require.NoError(t, err)
		assert.True(t, hashChanged, "hash should have changed due to description update")

		// Next lookup should get updated value (cache invalidated)
		def, err := repo.GetByName(ctx, tenantID, toolName)
		require.NoError(t, err)
		assert.Equal(t, newDescription, def.Description)
		assert.Equal(t, newHash, def.Hash)
	})

	t.Run("Cache invalidated on CreateToolDefinition", func(t *testing.T) {
		newToolName := "new_cache_tool"
		newDescription := "New tool for cache test"
		newHash := tool.ComputeHash(newToolName, newDescription, parameters)

		err := repo.CreateToolDefinition(ctx, tenantID, &tool.ToolDefinition{
			TenantID:             tenantID,
			Name:                 newToolName,
			Description:          newDescription,
			InputSchema:          parameters,
			Grants:               grants,
			ExecutionTimeoutMs:   timeoutMs,
			MemoryPages:          memoryPages,
			Hash:                 newHash,
			IsActive:             true,
		})
		require.NoError(t, err)

		// Lookup should work (cache populated)
		def, err := repo.GetByName(ctx, tenantID, newToolName)
		require.NoError(t, err)
		assert.Equal(t, newToolName, def.Name)
	})

	t.Run("Cache invalidated on DeactivateToolDefinition", func(t *testing.T) {
		deactivateToolName := "deactivate_cache_tool"
		deactivateHash := tool.ComputeHash(deactivateToolName, "Deactivate test", parameters)

		// Create tool first
		err := repo.CreateToolDefinition(ctx, tenantID, &tool.ToolDefinition{
			TenantID:             tenantID,
			Name:                 deactivateToolName,
			Description:          "Deactivate test",
			InputSchema:          parameters,
			Grants:               grants,
			ExecutionTimeoutMs:   timeoutMs,
			MemoryPages:          memoryPages,
			Hash:                 deactivateHash,
			IsActive:             true,
		})
		require.NoError(t, err)

		// Lookup to populate cache
		_, err = repo.GetByName(ctx, tenantID, deactivateToolName)
		require.NoError(t, err)

		// Now deactivate
		err = repo.DeactivateToolDefinition(ctx, tenantID, deactivateToolName)
		require.NoError(t, err)

		// Lookup should return not found (cache invalidated)
		_, err = repo.GetByName(ctx, tenantID, deactivateToolName)
		assert.ErrorIs(t, err, tool.ErrToolNotFound)
	})

	t.Run("Cache TTL expiration", func(t *testing.T) {
		// Create tool with short TTL for testing
		shortTTLRepo := NewToolRepository(dbPool, auditRepo, logger, 100*time.Millisecond, cacheMaxEntries)

		toolNameTTL := "ttl_test_tool"
		ttlHash := tool.ComputeHash(toolNameTTL, "TTL test", parameters)

		err := createTestTool(ctx, dbPool, tenantID, toolNameTTL, "TTL test", parameters, grants, timeoutMs, memoryPages, ttlHash)
		require.NoError(t, err)

		// First lookup
		def, err := shortTTLRepo.GetByName(ctx, tenantID, toolNameTTL)
		require.NoError(t, err)
		assert.Equal(t, toolNameTTL, def.Name)

		// Wait for TTL to expire
		time.Sleep(200 * time.Millisecond)

		// Second lookup should be cache miss (hit DB)
		def, err = shortTTLRepo.GetByName(ctx, tenantID, toolNameTTL)
		require.NoError(t, err)
		assert.Equal(t, toolNameTTL, def.Name)
	})
}

func TestToolRepository_CacheStructKey(t *testing.T) {
	// This test verifies the cache key is a struct {TenantID, Name} not string concatenation
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	dbPool := setupTestDB(t)
	defer dbPool.Close()

	logger := zerolog.New(zerolog.NewTestWriter(t)).Level(zerolog.DebugLevel)
	auditRepo := NewAuditRepository(dbPool)

	cacheTTL := 5 * time.Minute
	cacheMaxEntries := 1000
	repo := NewToolRepository(dbPool, auditRepo, logger, cacheTTL, cacheMaxEntries)

	tenantID1 := domain.MustParseUUID("11111111-1111-1111-1111-111111111111")
	tenantID2 := domain.MustParseUUID("22222222-2222-2222-2222-222222222222")

	// Ensure test tenants exist (FK requirement for tool_definitions)
	require.NoError(t, ensureTestTenant(ctx, dbPool, tenantID1))
	require.NoError(t, ensureTestTenant(ctx, dbPool, tenantID2))

	toolName := "struct_key_test"

	parameters := json.RawMessage(`{"type":"object","properties":{}}`)
	grants := json.RawMessage(`[]`)
	timeoutMs := uint64(30000)
	memoryPages := uint32(512)

	// Create tool in tenant1
	hash1 := tool.ComputeHash(toolName, "Tenant 1 tool", parameters)
	err := createTestTool(ctx, dbPool, tenantID1, toolName, "Tenant 1 tool", parameters, grants, timeoutMs, memoryPages, hash1)
	require.NoError(t, err)

	// Create tool in tenant2 with DIFFERENT description
	hash2 := tool.ComputeHash(toolName, "Tenant 2 tool", parameters)
	err = createTestTool(ctx, dbPool, tenantID2, toolName, "Tenant 2 tool", parameters, grants, timeoutMs, memoryPages, hash2)
	require.NoError(t, err)

	// Lookup tenant1
	def1, err := repo.GetByName(ctx, tenantID1, toolName)
	require.NoError(t, err)
	assert.Equal(t, "Tenant 1 tool", def1.Description)

	// Lookup tenant2
	def2, err := repo.GetByName(ctx, tenantID2, toolName)
	require.NoError(t, err)
	assert.Equal(t, "Tenant 2 tool", def2.Description)

	// Verify they have different hashes (different descriptions)
	assert.NotEqual(t, def1.Hash, def2.Hash)
}

// Helper functions
func setupTestDB(t *testing.T) *pgxpool.Pool {
	dsn := getTestDSN(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	return pool
}

// ensureTestTenant creates the test tenant in the database if it doesn't exist.
// Required because tool_definitions has FK to tenants table.
// The tenants table does not have RLS, so we can insert directly without WithTenantTx.
func ensureTestTenant(ctx context.Context, pool *pgxpool.Pool, tenantID domain.UUID) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO public.tenants (id, name, status) VALUES ($1, 'Test Tenant', 'active')
		ON CONFLICT (id) DO NOTHING
	`, uuid.UUID(tenantID))
	return err
}

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

func createTestTool(ctx context.Context, pool *pgxpool.Pool, tenantID domain.UUID, name, description string, parameters, grants json.RawMessage, timeoutMs uint64, memoryPages uint32, hash string) error {
	return WithTenantTx(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
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
		`, uuid.UUID(tenantID), name, description, parameters, grants, timeoutMs, memoryPages, hash)
		return err
	})
}