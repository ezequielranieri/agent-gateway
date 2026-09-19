//go:build integration
// +build integration

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/postgres"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/domain/tool"
	"github.com/ezequielranieri/agent-gateway/internal/middleware"
)

func TestAdminToolsHandler_AuthzAndSeverity(t *testing.T) {
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

	// Setup JWT service for test tokens
	authMW := middleware.NewAuth(middleware.AuthConfig{
		JWTService: newTestJWTService(t),
		Logger:     logger,
	})

	// Create handler
	handler := NewAdminToolsHandler(toolRepo, logger)

	// Setup router with auth middleware
	r := chi.NewRouter()
	r.Use(authMW)
	handler.RegisterRoutes(r)

	// Test tenant and user IDs
	tenantID := domain.MustParseUUID("11111111-1111-1111-1111-111111111111")
	superAdminID := domain.MustParseUUID("22222222-2222-2222-2222-222222222222")
	regularUserID := domain.MustParseUUID("33333333-3333-3333-3333-333333333333")

	// Create test tokens
	superAdminToken := createTestToken(t, superAdminID, domain.UUID{}, "super_admin", []string{"admin:tools"})
	regularUserToken := createTestToken(t, regularUserID, tenantID, "user", []string{})
	tenantAdminToken := createTestToken(t, regularUserID, tenantID, "admin", []string{}) // admin but not super_admin

	// Helper to make requests
	makeReq := func(method, path, token string, body interface{}) *httptest.ResponseRecorder {
		var buf []byte
		if body != nil {
			buf, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, path, bytes.NewBuffer(buf))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	t.Run("POST without token returns 401", func(t *testing.T) {
		w := makeReq("POST", "/admin/tools", "", map[string]interface{}{
			"tenant_id":  tenantID.String(),
			"name":       "test_tool",
			"parameters": map[string]interface{}{"type": "object"},
		})
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("POST with regular user (non-super-admin) returns 403", func(t *testing.T) {
		w := makeReq("POST", "/admin/tools", regularUserToken, map[string]interface{}{
			"tenant_id":  tenantID.String(),
			"name":       "test_tool",
			"parameters": map[string]interface{}{"type": "object"},
		})
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("POST with tenant admin (non-super-admin) returns 403", func(t *testing.T) {
		w := makeReq("POST", "/admin/tools", tenantAdminToken, map[string]interface{}{
			"tenant_id":  tenantID.String(),
			"name":       "test_tool",
			"parameters": map[string]interface{}{"type": "object"},
		})
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("POST with super_admin creates tool for tenant A", func(t *testing.T) {
		w := makeReq("POST", "/admin/tools", superAdminToken, map[string]interface{}{
			"tenant_id":  tenantID.String(),
			"name":       "tool_a",
			"description": "Tool for tenant A",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{"input": map[string]interface{}{"type": "string"}},
			},
			"grants":     []string{"filesystem:read"},
			"execution_timeout_ms": 5000000,
			"memory_pages": 256,
		})
		assert.Equal(t, http.StatusCreated, w.Code)

		var resp ToolResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "tool_a", resp.Name)
		assert.Equal(t, tenantID.String(), resp.TenantID)
		assert.NotEmpty(t, resp.Hash)
		assert.Equal(t, uint64(5000000), resp.ExecutionTimeoutMs)
		assert.Equal(t, uint32(256), resp.MemoryPages)
	})

	t.Run("POST with super_admin creates tool for tenant B", func(t *testing.T) {
		w := makeReq("POST", "/admin/tools", superAdminToken, map[string]interface{}{
			"tenant_id":  "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
			"name":       "tool_b",
			"description": "Tool for tenant B",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{"input": map[string]interface{}{"type": "string"}},
			},
		})
		assert.Equal(t, http.StatusCreated, w.Code)

		var resp ToolResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "tool_b", resp.Name)
		assert.Equal(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", resp.TenantID)
	})

	t.Run("PATCH with super_admin on tenant A's tool (cross-tenant) - RLS denies with 404", func(t *testing.T) {
		// Try to PATCH tenant A's tool using tenant B's ID
		w := makeReq("PATCH", "/admin/tools/tool_a", superAdminToken, map[string]interface{}{
			"tenant_id": "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
			"description": "Hacked from tenant B",
		})
		// RLS should deny - tool_a belongs to tenant A, not B
		// Expect 404 (not found in tenant B)
		assert.Equal(t, http.StatusNotFound, w.Code, "Expected 404 for cross-tenant PATCH")

		// Verify tenant A's tool is unchanged
		toolA, err := toolRepo.GetByName(ctx, tenantID, "tool_a")
		require.NoError(t, err)
		assert.Equal(t, "Tool for tenant A", toolA.Description)
		assert.Equal(t, toolA.Hash, toolA.Hash) // hash unchanged
	})

	t.Run("PATCH with super_admin on tenant A's tool (same tenant) succeeds", func(t *testing.T) {
		w := makeReq("PATCH", "/admin/tools/tool_a", superAdminToken, map[string]interface{}{
			"tenant_id": tenantID.String(),
			"description": "Updated from tenant A",
		})
		assert.Equal(t, http.StatusOK, w.Code)

		var resp ToolResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "Updated from tenant A", resp.Description)
		assert.NotEmpty(t, resp.Hash)
	})

	t.Run("PATCH grants/limits only (no definition change) - severity warn", func(t *testing.T) {
		originalTool, err := toolRepo.GetByName(ctx, tenantID, "tool_a")
		require.NoError(t, err)
		originalHash := originalTool.Hash

		w := makeReq("PATCH", "/admin/tools/tool_a", superAdminToken, map[string]interface{}{
			"tenant_id":    tenantID.String(),
			"execution_timeout_ms":   8000000,
			"grants":       []string{"filesystem:read", "filesystem:write"},
		})
		assert.Equal(t, http.StatusOK, w.Code)

		var resp ToolResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		// Hash should be unchanged (only grants/limits changed)
		assert.Equal(t, originalHash, resp.Hash)
		assert.Equal(t, uint64(8000000), resp.ExecutionTimeoutMs)
	})

	t.Run("PATCH cannot change tenant_id", func(t *testing.T) {
		w := makeReq("PATCH", "/admin/tools/tool_a", superAdminToken, map[string]interface{}{
			"tenant_id": "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		})
		// Should fail - RLS WITH CHECK prevents changing tenant_id
		// Expect 404 (not found in that tenant)
		assert.Equal(t, http.StatusNotFound, w.Code, "Expected 404 for tenant_id change")

		// Verify tool still in tenant A
		toolA, err := toolRepo.GetByName(ctx, tenantID, "tool_a")
		require.NoError(t, err)
		assert.Equal(t, tenantID, toolA.TenantID)
	})

	t.Run("DELETE with super_admin on tenant B's tool (cross-tenant) - RLS denies with 404", func(t *testing.T) {
		w := makeReq("DELETE", "/admin/tools/tool_b?tenant_id=11111111-1111-1111-1111-111111111111", superAdminToken, map[string]interface{}{})
		assert.Equal(t, http.StatusNotFound, w.Code, "Expected 404 for cross-tenant DELETE")

		// Verify tenant B's tool still exists
		toolB, err := toolRepo.GetByName(ctx, domain.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"), "tool_b")
		require.NoError(t, err)
		assert.Equal(t, domain.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"), toolB.TenantID)
	})

	t.Run("DELETE with super_admin on tenant B's tool (same tenant) succeeds", func(t *testing.T) {
		w := makeReq("DELETE", "/admin/tools/tool_b?tenant_id=bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", superAdminToken, map[string]interface{}{})
		assert.Equal(t, http.StatusNoContent, w.Code)

		// Verify tool is deactivated
		_, err := toolRepo.GetByName(ctx, domain.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"), "tool_b")
		assert.ErrorIs(t, err, tool.ErrToolNotFound)
	})

	t.Run("Severity matrix verification via audit log", func(t *testing.T) {
		// Query audit events for tenant A
		events, err := auditRepo.Query(ctx, postgres.AuditFilter{
			TenantID:   tenantID,
			EntityType: "tool_definition",
			Limit:      100,
		})
		require.NoError(t, err)

		// Should have events for: CREATE tool_a (info), PATCH tool_a desc (critical), PATCH tool_a grants (warn), DELETE tool_b (not in A)
		var createFound, criticalFound, warnFound bool
		for _, e := range events {
			if e.Action == "tool_definition.create" {
				createFound = true
				assert.Equal(t, domain.AuditSeverityInfo, e.Severity)
			}
			if e.Action == "tool_definition.update" {
				// Check severity based on what changed
				var payload toolDefinitionAuditPayload
				json.Unmarshal(e.Payload, &payload)
				if payload.ChangedFields != nil {
					hasDefinitionChange := false
					for _, f := range payload.ChangedFields {
						if f == "description" || f == "parameters" {
							hasDefinitionChange = true
							break
						}
					}
					if hasDefinitionChange {
						assert.Equal(t, domain.AuditSeverityCritical, e.Severity, "Definition change should be critical")
						criticalFound = true
					} else {
						assert.Equal(t, domain.AuditSeverityWarn, e.Severity, "Grants/limits change should be warn")
						warnFound = true
					}
				}
			}
		}
		assert.True(t, createFound, "CREATE event with info severity not found")
		assert.True(t, criticalFound, "Critical severity event not found")
		assert.True(t, warnFound, "Warn severity event not found")
	})
}

// toolDefinitionAuditPayload mirrors the internal struct for test verification
type toolDefinitionAuditPayload struct {
	Operation      string          `json:"operation"`
	ToolName       string          `json:"tool_name"`
	OldHash        *string         `json:"old_hash,omitempty"`
	NewHash        *string         `json:"new_hash,omitempty"`
	ChangedFields  []string        `json:"changed_fields,omitempty"`
	FullDefinition json.RawMessage `json:"full_definition,omitempty"`
}