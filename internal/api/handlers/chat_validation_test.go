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
	"github.com/ezequielranieri/agent-gateway/internal/adapter/tool/wazero"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
	"github.com/ezequielranieri/agent-gateway/internal/domain/tool"
	"github.com/ezequielranieri/agent-gateway/internal/middleware"
	"github.com/ezequielranieri/agent-gateway/internal/usecase/chat"
)

func TestChatHandler_Validation(t *testing.T) {
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
	jwtService := newTestJWTService(t)
	authMW := middleware.NewAuth(middleware.AuthConfig{
		JWTService: jwtService,
		Logger:     logger,
	})

	// Create tenant and user
	tenantID := domain.MustParseUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	userID := domain.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")

	// Create test token
	token := createTestToken(t, userID, tenantID, "user", []string{})

	// Register a tool in the registry for this tenant
	toolName := "read_file"
	description := "Read a file from filesystem"
	parameters := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
	grants := json.RawMessage(`["filesystem:read"]`)
	hash := tool.ComputeHash(toolName, description, parameters)

	err = createTestTool(ctx, dbPool, tenantID, toolName, description, parameters, grants, 5000000, 256, hash)
	require.NoError(t, err)

	// Build chat handler with tool validation
	toolConfig := tool.ToolConfig{
		DefaultTimeoutMs:   30000,
		DefaultMemoryPages: 512,
		CacheTTL:           5 * time.Minute,
		CacheMaxEntries:    1000,
		Tools: []tool.ToolModuleConfig{
			{
				Name:             toolName,
				ModulePath:       getTestWASMPath(t, "echo.wasm"),
				Grants:           tool.ToolGrants{FSReadOnlyMounts: nil, AllowNetwork: false},
				Limits:           tool.ToolLimits{TimeoutMs: 10000, MemoryPages: 256},
				RequiresApproval: false,
			},
		},
	}

	// Create executor for tool execution
	toolExecutor, err := wazero.NewWasmExecutor(toolConfig, logger)
	require.NoError(t, err)
	defer toolExecutor.Close(context.Background())

	// Build chat usecase with tool validation
	chatUC, err := chat.BuildChatUsecaseFromConfig(
		ctx,
		model.RouterConfig{
			Providers: []model.ProviderConfig{
				{Name: "test-provider", Type: model.ProviderTypeMock, Models: []string{"test-model"}, Enabled: true},
			},
			DefaultTimeout: 30 * time.Second,
		},
		&toolConfig,
		toolExecutor,
		toolRepo,
		nil, // pricing service
		logger,
	)
	require.NoError(t, err)

	chatHandlers := NewChatHandlers(logger, chatUC, toolRepo)

	router := chi.NewRouter()
	router.Use(authMW)
	router.Post("/v1/chat/completions", chatHandlers.ChatCompletions)

	t.Run("Valid tool with correct hash passes validation", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"model": "test-model",
			"messages": []map[string]string{
				{"role": "user", "content": "read a file"},
			},
			"tools": []map[string]interface{}{
				{
					"name":        toolName,
					"description": description,
					"parameters":  map[string]interface{}{"type": "object", "properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}}, "required": []string{"path"}},
				},
			},
		}

		buf, _ := json.Marshal(reqBody)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBuffer(buf))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Should not be rejected by validation (may fail later in execution)
		assert.NotEqual(t, http.StatusBadRequest, w.Code, "Valid tool should not be rejected by validation")
	})

	t.Run("Unknown tool rejected with tool_not_found", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"model": "test-model",
			"messages": []map[string]string{
				{"role": "user", "content": "do something"},
			},
			"tools": []map[string]interface{}{
				{
					"name":        "unknown_tool",
					"description": "Does not exist",
					"parameters":  map[string]interface{}{"type": "object"},
				},
			},
		}

		buf, _ := json.Marshal(reqBody)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBuffer(buf))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp map[string]string
		json.Unmarshal(w.Body.Bytes(), &resp)
		assert.Equal(t, "tool_not_found", resp["error"])
	})

t.Run("Known tool with hash mismatch rejected with tool_definition_mismatch", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"model": "test-model",
			"messages": []map[string]string{
				{"role": "user", "content": "read a file"},
			},
			"tools": []map[string]interface{}{
				{
					"name":        toolName,
					"description": "MODIFIED DESCRIPTION - hash will differ",
					"parameters":  map[string]interface{}{"type": "object", "properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}}, "required": []string{"path"}},
				},
			},
		}

		buf, _ := json.Marshal(reqBody)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBuffer(buf))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp map[string]string
		json.Unmarshal(w.Body.Bytes(), &resp)
		assert.Equal(t, "tool_definition_mismatch", resp["error"])
	})

	t.Run("Body tenant_id is ignored - uses JWT tenant", func(t *testing.T) {
		// Create another tenant
		otherTenantID := domain.MustParseUUID("cccccccc-cccc-cccc-cccc-cccccccccccc")

		// Register tool in OTHER tenant
		otherToolName := "other_tool"
		otherDesc := "Tool in other tenant"
		otherParams := json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}}}`)
		otherGrants := json.RawMessage(`["filesystem:read"]`)
		otherHash := tool.ComputeHash(otherToolName, otherDesc, otherParams)

		err := createTestTool(ctx, dbPool, otherTenantID, otherToolName, otherDesc, otherParams, otherGrants, 5000000, 256, otherHash)
		require.NoError(t, err)

		// Request with body tenant_id = other tenant, but JWT has our tenant
		reqBody := map[string]interface{}{
			"model": "test-model",
			"messages": []map[string]string{
				{"role": "user", "content": "test"},
			},
			"tools": []map[string]interface{}{
				{
"name":        otherToolName,
				"description": otherDesc,
				"parameters":  map[string]interface{}{"type": "object", "properties": map[string]interface{}{"input": map[string]interface{}{"type": "string"}}},
			},
		},
		"tenant_id": otherTenantID.String(), // This should be IGNORED
	}

	buf, _ := json.Marshal(reqBody)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBuffer(buf))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Should be rejected because tool doesn't exist in JWT tenant
		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp map[string]string
		json.Unmarshal(w.Body.Bytes(), &resp)
		assert.Equal(t, "tool_not_found", resp["error"])
	})

t.Run("Multiple tools - one invalid rejects entire request", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"model": "test-model",
			"messages": []map[string]string{
				{"role": "user", "content": "test"},
			},
			"tools": []map[string]interface{}{
				{
					"name":        toolName,
					"description": description,
					"parameters":  map[string]interface{}{"type": "object", "properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}}, "required": []string{"path"}},
				},
				{
					"name":        "evil_tool",
					"description": "Unknown tool",
					"parameters":  map[string]interface{}{"type": "object"},
				},
			},
		}

		buf, _ := json.Marshal(reqBody)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBuffer(buf))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp map[string]string
		json.Unmarshal(w.Body.Bytes(), &resp)
		assert.Equal(t, "tool_not_found", resp["error"])
	})
}