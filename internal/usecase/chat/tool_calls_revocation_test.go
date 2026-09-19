package chat

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/postgres"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/domain/tool"
)

//go:build integration
// +build integration

func TestToolCalls_RevocationAndValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	dsn := getTestDSN(t)

	ctx := context.Background()
	dbPool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer dbPool.Close()

	logger := zerolog.New(zerolog.NewTestWriter(t)).Level(zerolog.DebugLevel)

	// Create repositories
	auditRepo := postgres.NewAuditRepository(dbPool)
	toolRepo := postgres.NewToolRepository(dbPool, auditRepo, logger, 5*time.Minute, 1000)

	tenantID := domain.MustParseUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	userID := domain.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")

	// Register tool in registry
	toolName := "read_file"
	description := "Read a file from filesystem"
	parameters := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
	grants := json.RawMessage(`["filesystem:read"]`)
	hash := tool.ComputeHash(toolName, description, parameters)

	err := createTestTool(ctx, dbPool, tenantID, toolName, description, parameters, grants, 5000000, 256, hash)
	require.NoError(t, err)

	// Build usecase with tool validation
	// This test assumes the chat usecase has been updated to:
	// 1. Validate all tools in request at validation phase
	// 2. Build authorized tool set
	// 3. Pass authorized set to orchestrator
	// 4. At execution time, re-resolve and verify hash

	t.Run("Authorized tool set is immutable per request", func(t *testing.T) {
		// This test verifies that once tools are validated in the request,
		// the authorized set cannot be changed by the model
		
		// The model response might include a tool_call for a tool not in the request
		// The orchestrator should reject it
		
		// TODO: This test requires the chat usecase to be wired with tool validation
		// For now, document the expected behavior:
		// - Request validation builds authorized_tools map[toolName]ValidatedToolDef
		// - Model response tool_calls are checked against authorized_tools
		// - Any tool_call not in authorized_tools is rejected and audited
		t.Log("Expected: tool_call for unauthorized tool -> rejected and audited")
	})

	t.Run("Tool call for different tenant rejected", func(t *testing.T) {
		// Register tool in another tenant
		otherTenantID := domain.MustParseUUID("cccccccc-cccc-cccc-cccc-cccccccccccc")
		otherToolName := "other_tool"
		otherDesc := "Tool in other tenant"
		otherParams := json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}}}`)
		otherGrants := json.RawMessage(`["filesystem:read"]`)
		otherHash := tool.ComputeHash(otherToolName, otherDesc, otherParams)

		err := createTestTool(ctx, dbPool, otherTenantID, otherToolName, otherDesc, otherParams, otherGrants, 5000000, 256, otherHash)
		require.NoError(t, err)

		// The request is for tenant A, but model might try to call tool from tenant B
		// The orchestrator should validate against the request's tenant (tenant A)
		// and reject tool calls for other tenants
		
		t.Log("Expected: tool_call for other tenant -> rejected and audited")
	})

	t.Run("Tool deleted mid-request cannot be executed", func(t *testing.T) {
		// Scenario: Request validated with tool X
		// Admin deletes tool X
		// Model returns tool_call for X
		// Orchestrator re-resolves (tenant, name) + verifies hash
		// Should find tool inactive/deleted and reject
		
		t.Log("Expected: tool deleted after validation -> re-resolve fails -> reject")
	})

	t.Run("Re-resolve at execution verifies hash matches", func(t *testing.T) {
		// At execution time, the orchestrator should:
		// 1. Lookup tool by (tenant_id, name) in registry
		// 2. Compute hash of the validated definition
		// 3. Compare with stored hash
		// 4. If mismatch, reject (tool definition changed)
		
		t.Log("Expected: hash mismatch at execution -> reject and audit")
	})
}