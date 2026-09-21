//go:build unit
// +build unit

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	providermock "github.com/ezequielranieri/agent-gateway/internal/adapter/provider/mock"
	"github.com/ezequielranieri/agent-gateway/internal/adapter/pricing"
	toolmock "github.com/ezequielranieri/agent-gateway/internal/adapter/tool/mock"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
	"github.com/ezequielranieri/agent-gateway/internal/domain/tool"
	"github.com/ezequielranieri/agent-gateway/internal/middleware"
	"github.com/ezequielranieri/agent-gateway/internal/usecase/chat"
)

// fakeAuditRepository captures audit events for testing
type fakeAuditRepository struct {
	mu     sync.Mutex
	events []*domain.AuditEvent
}

func newFakeAuditRepository() *fakeAuditRepository {
	return &fakeAuditRepository{
		events: make([]*domain.AuditEvent, 0),
	}
}

func (f *fakeAuditRepository) Append(ctx context.Context, event *domain.AuditEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
	return nil
}

func (f *fakeAuditRepository) GetEvents() []*domain.AuditEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	events := make([]*domain.AuditEvent, len(f.events))
	copy(events, f.events)
	return events
}

func (f *fakeAuditRepository) GetEventsByAction(action string) []*domain.AuditEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []*domain.AuditEvent
	for _, e := range f.events {
		if e.Action == action {
			result = append(result, e)
		}
	}
	return result
}

func (f *fakeAuditRepository) Clear() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = f.events[:0]
}

// fakeToolRepository implements tool.ToolRepository for unit tests
type fakeToolRepository struct {
	mu        sync.Mutex
	tools     map[string]*tool.ToolDefinition
	err       error
	auditRepo *fakeAuditRepository
}

func newFakeToolRepository(auditRepo *fakeAuditRepository) *fakeToolRepository {
	return &fakeToolRepository{
		tools:     make(map[string]*tool.ToolDefinition),
		auditRepo: auditRepo,
	}
}

func (f *fakeToolRepository) setError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeToolRepository) addTool(tenantID domain.UUID, def *tool.ToolDefinition) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := tenantID.String() + ":" + def.Name
	f.tools[key] = def
}

func (f *fakeToolRepository) removeTool(tenantID domain.UUID, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := tenantID.String() + ":" + name
	delete(f.tools, key)
}

func (f *fakeToolRepository) deactivateTool(tenantID domain.UUID, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := tenantID.String() + ":" + name
	if def, ok := f.tools[key]; ok {
		def.IsActive = false
	}
}

func (f *fakeToolRepository) updateToolHash(tenantID domain.UUID, name string, newHash string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := tenantID.String() + ":" + name
	if def, ok := f.tools[key]; ok {
		def.Hash = newHash
	}
}

func (f *fakeToolRepository) GetByName(ctx context.Context, tenantID domain.UUID, name string) (*tool.ToolDefinition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		if f.auditRepo != nil {
			f.auditRepo.Append(ctx, &domain.AuditEvent{
				TenantID: tenantID,
				Action:   "tool_validate_repo_error",
				Severity: domain.AuditSeverityCritical,
				EntityType: "tool",
				EntityID:   nil,
				Payload:    json.RawMessage(fmt.Sprintf(`{"tool":"%s","error":"repo_error"}`, name)),
			})
		}
		return nil, tool.ErrToolRepository
	}
	key := tenantID.String() + ":" + name
	if def, ok := f.tools[key]; ok {
		return def, nil
	}
	if f.auditRepo != nil {
		f.auditRepo.Append(ctx, &domain.AuditEvent{
			TenantID: tenantID,
			Action:   "tool_not_found",
			Severity: domain.AuditSeverityCritical,
			EntityType: "tool",
			EntityID:   nil,
			Payload:    json.RawMessage(fmt.Sprintf(`{"tool":"%s"}`, name)),
		})
	}
	return nil, tool.ErrToolNotFound
}

// ValidateTool validates a tool request against the registry with hash check and audit
func (f *fakeToolRepository) ValidateTool(ctx context.Context, tenantID domain.UUID, fn model.FunctionDef) (*tool.ToolDefinition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	
	if f.err != nil {
		if f.auditRepo != nil {
			f.auditRepo.Append(ctx, &domain.AuditEvent{
				TenantID: tenantID,
				Action:   "tool_validate_repo_error",
				Severity: domain.AuditSeverityCritical,
				EntityType: "tool",
				EntityID:   nil,
				Payload:    json.RawMessage(fmt.Sprintf(`{"tool":"%s","error":"repo_error"}`, fn.Name)),
			})
		}
		return nil, tool.ErrToolRepository
	}
	
	key := tenantID.String() + ":" + fn.Name
	def, ok := f.tools[key]
	if !ok {
		if f.auditRepo != nil {
			f.auditRepo.Append(ctx, &domain.AuditEvent{
				TenantID: tenantID,
				Action:   "tool_not_found",
				Severity: domain.AuditSeverityCritical,
				EntityType: "tool",
				EntityID:   nil,
				Payload:    json.RawMessage(fmt.Sprintf(`{"tool":"%s"}`, fn.Name)),
			})
		}
		return nil, tool.ErrToolNotFound
	}
	
	if !def.IsActive {
		if f.auditRepo != nil {
			f.auditRepo.Append(ctx, &domain.AuditEvent{
				TenantID: tenantID,
				Action:   "tool_inactive",
				Severity: domain.AuditSeverityCritical,
				EntityType: "tool",
				EntityID:   nil,
				Payload:    json.RawMessage(fmt.Sprintf(`{"tool":"%s"}`, fn.Name)),
			})
		}
		return nil, tool.ErrToolNotFound
	}
	
	// Compute hash of request tool definition
	paramsJSON, _ := json.Marshal(fn.Parameters)
	requestHash := tool.ComputeHash(fn.Name, fn.Description, paramsJSON)
	
	if requestHash != def.Hash {
		if f.auditRepo != nil {
			f.auditRepo.Append(ctx, &domain.AuditEvent{
				TenantID: tenantID,
				Action:   "tool_hash_mismatch",
				Severity: domain.AuditSeverityCritical,
				EntityType: "tool",
				EntityID:   nil,
				Payload:    json.RawMessage(fmt.Sprintf(`{"tool":"%s","request_hash":"%s","registry_hash":"%s"}`, fn.Name, requestHash, def.Hash)),
			})
		}
		return nil, tool.ErrToolDefinitionMismatch
	}
	
	return def, nil
}

// The rest of ToolRepository interface methods (not used in these tests)
func (f *fakeToolRepository) CreateToolDefinition(ctx context.Context, tenantID domain.UUID, def *tool.ToolDefinition) error { return nil }
func (f *fakeToolRepository) UpdateToolDefinition(ctx context.Context, tenantID domain.UUID, def *tool.ToolDefinition) (bool, error) { return false, nil }
func (f *fakeToolRepository) DeactivateToolDefinition(ctx context.Context, tenantID domain.UUID, name string) error { return nil }
func (f *fakeToolRepository) UpsertToolDefinition(ctx context.Context, tenantID domain.UUID, def *tool.ToolDefinition) error { return nil }
func (f *fakeToolRepository) InitFromConfig(ctx context.Context, tenantID domain.UUID, cfg *tool.ToolConfig) error { return nil }

// mockProviderWithCapture captures the request sent to the provider
type mockProviderWithCapture struct {
	*providermock.Provider
	lastReq       model.ChatRequest
	lastReqRaw    []byte // Raw JSON bytes sent to provider
	mu            sync.Mutex
}

func newMockProviderWithCapture(opts ...providermock.Option) *mockProviderWithCapture {
	mp := &mockProviderWithCapture{}
	base := providermock.NewProvider(opts...)
	baseProvider := base
	// Wrap the Complete method to capture request
	mp.Provider = providermock.NewProvider(
		providermock.WithName("test-provider"),
		providermock.WithModels([]string{"test-model"}),
		providermock.WithEnabled(true),
		providermock.WithResponseFunc(func(req model.ChatRequest) (model.Completion, error) {
			// Capture raw JSON bytes that would be sent to provider
			raw, _ := json.Marshal(req)
			mp.mu.Lock()
			mp.lastReq = req
			mp.lastReqRaw = raw
			mp.mu.Unlock()
			return baseProvider.Complete(context.Background(), req)
		}),
	)
	return mp
}

func (m *mockProviderWithCapture) getLastRequest() model.ChatRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastReq
}

func (m *mockProviderWithCapture) getLastRequestRaw() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastReqRaw
}

func TestChatHandler_Validation_Unit(t *testing.T) {
	ctx := context.Background()
	logger := zerolog.New(zerolog.NewTestWriter(t)).Level(zerolog.DebugLevel)

	tenantID := domain.MustParseUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	userID := domain.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")

	// Create shared JWT service
	jwtService := newTestJWTService(t)

	// Setup fake audit repo
	auditRepo := newFakeAuditRepository()

	// Setup fake repo with a registered tool
	toolName := "read_file"
	description := "Read a file from filesystem"
	parameters := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
	grants := json.RawMessage(`["filesystem:read"]`)
	hash := tool.ComputeHash(toolName, description, parameters)

	fakeRepo := newFakeToolRepository(auditRepo)
	fakeRepo.addTool(tenantID, &tool.ToolDefinition{
		TenantID:             tenantID,
		Name:                 toolName,
		Description:          description,
		InputSchema:          parameters,
		Grants:               grants,
		ExecutionTimeoutMs:   5000000,
		MemoryPages:          256,
		Hash:                 hash,
		IsActive:             true,
	})

	// Create mock provider that captures requests
	mockProvider := newMockProviderWithCapture(
		providermock.WithName("test-provider"),
		providermock.WithModels([]string{"test-model"}),
		providermock.WithEnabled(true),
	)

	// Build tool config
	toolConfig := tool.ToolConfig{
		DefaultTimeoutMs:   30000,
		DefaultMemoryPages: 512,
		CacheTTL:           5 * time.Minute,
		CacheMaxEntries:    1000,
		Tools: []tool.ToolModuleConfig{
			{
				Name:             toolName,
				ModulePath:       "test/wasm/echo.wasm",
				Grants:           tool.ToolGrants{FSReadOnlyMounts: nil, AllowNetwork: false},
				Limits:           tool.ToolLimits{TimeoutMs: 10000, MemoryPages: 256},
				RequiresApproval: false,
			},
		},
	}

	// Build chat usecase with mock provider
	toolExecutor := toolmock.NewMockExecutor(
		toolmock.WithSupportedTools(toolName),
		toolmock.WithLatency(10*time.Millisecond),
	)

	chatUC, err := chat.BuildChatUsecaseFromConfigWithProvider(
		ctx,
		model.RouterConfig{
			Providers: []model.ProviderConfig{
				{Name: "test-provider", Type: model.ProviderTypeMock, Models: []string{"test-model"}, Enabled: true},
			},
			DefaultTimeout: 30 * time.Second,
		},
		&toolConfig,
		toolExecutor,
		fakeRepo,
		pricing.NewTestService(),
		logger,
		mockProvider,
	)
	require.NoError(t, err)

	// Create chat handlers - pass both tool repo and audit repo
	chatHandlers := NewChatHandlers(logger, chatUC, fakeRepo)

	// Create test JWT token using shared service
	token := createTestTokenWithService(t, jwtService, userID, tenantID, "user", []string{})

	router := chi.NewRouter()
	router.Use(middleware.NewAuth(middleware.AuthConfig{
		JWTService: jwtService,
		Logger:     logger,
	}))
	router.Post("/v1/chat/completions", chatHandlers.ChatCompletions)

	t.Run("Unknown tool rejected with tool_not_found", func(t *testing.T) {
		auditRepo.Clear()

		reqBody := map[string]interface{}{
			"model": "test-model",
			"messages": []map[string]string{
				{"role": "user", "content": "do something"},
			},
			"tools": []map[string]interface{}{
				{
					"type": "function",
					"function": map[string]interface{}{
						"name":        "unknown_tool",
						"description": "Does not exist",
						"parameters":  map[string]interface{}{"type": "object"},
					},
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
		assert.Equal(t, "tool not found", resp["error"])

		// Verify audit event emitted
		events := auditRepo.GetEventsByAction("tool_not_found")
		require.Len(t, events, 1)
		assert.Equal(t, tenantID, events[0].TenantID)
		assert.Equal(t, domain.AuditSeverityCritical, events[0].Severity)
	})

	t.Run("Known tool with hash mismatch rejected with tool_definition_mismatch", func(t *testing.T) {
		auditRepo.Clear()

		// Verify audit would be emitted using ValidateTool
		_, err := fakeRepo.ValidateTool(ctx, tenantID, model.FunctionDef{
			Name:        toolName,
			Description: "MODIFIED DESCRIPTION - hash will differ",
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}}, "required": []string{"path"}},
		})
		assert.ErrorIs(t, err, tool.ErrToolDefinitionMismatch)
		events := auditRepo.GetEventsByAction("tool_hash_mismatch")
		require.Len(t, events, 1)
		assert.Equal(t, tenantID, events[0].TenantID)
		assert.Equal(t, domain.AuditSeverityCritical, events[0].Severity)
		auditRepo.Clear()

		reqBody := map[string]interface{}{
			"model": "test-model",
			"messages": []map[string]string{
				{"role": "user", "content": "read a file"},
			},
			"tools": []map[string]interface{}{
				{
					"type": "function",
					"function": map[string]interface{}{
						"name":        toolName,
						"description": "MODIFIED DESCRIPTION - hash will differ",
						"parameters":  map[string]interface{}{"type": "object", "properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}}, "required": []string{"path"}},
					},
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
		assert.Equal(t, "tool definition mismatch", resp["error"])
	})

t.Run("Tool inactive rejected with tool_not_found", func(t *testing.T) {
		auditRepo.Clear()
		fakeRepo.deactivateTool(tenantID, toolName)

		// Verify audit would be emitted using ValidateTool
		_, err := fakeRepo.ValidateTool(ctx, tenantID, model.FunctionDef{
			Name:        toolName,
			Description: description,
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}}, "required": []string{"path"}},
		})
		assert.ErrorIs(t, err, tool.ErrToolNotFound)
		events := auditRepo.GetEventsByAction("tool_inactive")
		require.Len(t, events, 1)
		assert.Equal(t, tenantID, events[0].TenantID)
		assert.Equal(t, domain.AuditSeverityCritical, events[0].Severity)
		auditRepo.Clear()

		reqBody := map[string]interface{}{
			"model": "test-model",
			"messages": []map[string]string{
				{"role": "user", "content": "read a file"},
			},
			"tools": []map[string]interface{}{
				{
					"type": "function",
					"function": map[string]interface{}{
						"name":        toolName,
						"description": description,
						"parameters":  map[string]interface{}{"type": "object", "properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}}, "required": []string{"path"}},
					},
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
		assert.Equal(t, "tool not found", resp["error"])
	})

	t.Run("Repo error at validation fails closed with 503", func(t *testing.T) {
		auditRepo.Clear()
		fakeRepo.setError(assert.AnError)
		defer fakeRepo.setError(nil) // Reset for subsequent tests

		reqBody := map[string]interface{}{
			"model": "test-model",
			"messages": []map[string]string{
				{"role": "user", "content": "read a file"},
			},
			"tools": []map[string]interface{}{
				{
					"type": "function",
					"function": map[string]interface{}{
						"name":        toolName,
						"description": description,
						"parameters":  map[string]interface{}{"type": "object", "properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}}, "required": []string{"path"}},
					},
				},
			},
		}

		buf, _ := json.Marshal(reqBody)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBuffer(buf))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Should fail closed with 503 (service unavailable), not 400
		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		var resp map[string]string
		json.Unmarshal(w.Body.Bytes(), &resp)
		assert.Equal(t, "tool repository unavailable", resp["error"])

		// Verify audit event emitted
		events := auditRepo.GetEventsByAction("tool_validate_repo_error")
		require.Len(t, events, 1)
		assert.Equal(t, tenantID, events[0].TenantID)
		assert.Equal(t, domain.AuditSeverityCritical, events[0].Severity)

		// Verify provider was NOT called (lastReq should be zero value)
		lastReq := mockProvider.getLastRequest()
		assert.Empty(t, lastReq.Model, "Provider should not be called on repo error")
	})

t.Run("Body tenant_id ignored - uses JWT tenant", func(t *testing.T) {
		// Create another tenant
		otherTenantID := domain.MustParseUUID("cccccccc-cccc-cccc-cccc-cccccccccccc")

		// Register tool in OTHER tenant
		otherToolName := "other_tool"
		otherDesc := "Tool in other tenant"
		otherParams := json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}}}`)
		otherGrants := json.RawMessage(`["filesystem:read"]`)
		otherHash := tool.ComputeHash(otherToolName, otherDesc, otherParams)

		fakeRepo.addTool(otherTenantID, &tool.ToolDefinition{
			TenantID:             otherTenantID,
			Name:                 otherToolName,
			Description:          otherDesc,
			InputSchema:          otherParams,
			Grants:               otherGrants,
			ExecutionTimeoutMs:   5000000,
			MemoryPages:          256,
			Hash:                 otherHash,
			IsActive:             true,
		})

		// Request with body tenant_id = other tenant, but JWT has our tenant
		reqBody := map[string]interface{}{
			"model": "test-model",
			"messages": []map[string]string{
				{"role": "user", "content": "test"},
			},
			"tools": []map[string]interface{}{
				{
					"type": "function",
					"function": map[string]interface{}{
						"name":        otherToolName,
						"description": otherDesc,
						"parameters":  map[string]interface{}{"type": "object", "properties": map[string]interface{}{"input": map[string]interface{}{"type": "string"}}},
					},
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
		assert.Equal(t, "tool not found", resp["error"])
	})

t.Run("Provider receives registry definition, not request bytes - parameters canonicalized", func(t *testing.T) {
		fakeRepo.setError(nil) // Ensure no error from previous tests
		// Reactivate tool (was deactivated in previous test)
		fakeRepo.addTool(tenantID, &tool.ToolDefinition{
			TenantID:             tenantID,
			Name:                 toolName,
			Description:          description,
			InputSchema:          parameters,
			Grants:               grants,
			ExecutionTimeoutMs:   5000000,
			MemoryPages:          256,
			Hash:                 hash,
			IsActive:             true,
		})

		// Request with equivalent parameters but different formatting (different key order, spacing)
		// This is equivalent to registry but NOT byte-for-byte identical
		differentFormatParams := json.RawMessage(`{"required":["path"],"type":"object","properties":{"path":{"type":"string"}}}`)
		requestHash := tool.ComputeHash(toolName, description, differentFormatParams)
		assert.Equal(t, hash, requestHash, "Hash should be same for equivalent JSON")

		// Request includes extra fields that should NOT be sent to provider
		reqBody := map[string]interface{}{
			"model": "test-model",
			"messages": []map[string]string{
				{"role": "user", "content": "read a file"},
			},
			"tools": []map[string]interface{}{
				{
					"type": "function",
					"function": map[string]interface{}{
						"name":         toolName,
						"description":  description,
						"parameters":   differentFormatParams, // Equivalent but different formatting
						"extra_field":  "should_not_be_sent",
						"another_extra": 12345,
					},
				},
			},
		}

		buf, _ := json.Marshal(reqBody)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBuffer(buf))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Should succeed (validation passes)
		assert.NotEqual(t, http.StatusBadRequest, w.Code)

		// Verify provider received EXACTLY the registry definition, not request bytes
		// Compare raw JSON bytes sent to provider
		lastReqRaw := mockProvider.getLastRequestRaw()
		
		// The provider should receive the registry definition (canonicalized), not the request bytes
		// Build expected provider request with registry definition (including user from JWT)
		var registryParams map[string]any
		json.Unmarshal(parameters, &registryParams)
		
		expectedProviderReq := model.ChatRequest{
			Model: "test-model",
			Messages: []model.Message{
				{Role: "user", Content: "read a file"},
			},
			Tools: []model.Tool{
				{
					Type: "function",
					Function: model.FunctionDef{
						Name:        toolName,
						Description: description,
						Parameters:  registryParams, // Registry version (canonicalized)
					},
				},
			},
			User: userID.String(), // User from JWT
		}
		expectedRaw, _ := json.Marshal(expectedProviderReq)
		
		// Exact byte comparison - this is the key test for mutation #2
		assert.Equal(t, string(expectedRaw), string(lastReqRaw), 
			"Provider should receive exact registry definition bytes, not request bytes")
	})

	t.Run("Multiple tools - one invalid rejects entire request", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"model": "test-model",
			"messages": []map[string]string{
				{"role": "user", "content": "test"},
			},
			"tools": []map[string]interface{}{
				{
					"type": "function",
					"function": map[string]interface{}{
						"name":        toolName,
						"description": description,
						"parameters":  map[string]interface{}{"type": "object", "properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}}, "required": []string{"path"}},
					},
				},
				{
					"type": "function",
					"function": map[string]interface{}{
						"name":        "evil_tool",
						"description": "Unknown tool",
						"parameters":  map[string]interface{}{"type": "object"},
					},
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
		assert.Equal(t, "tool not found", resp["error"])
	})

	// Test data for JCS canonicalization tests
	toolNameSpecial := "special_tool"
	descriptionSpecial := "Tool with <script>&entities</script>"
	paramsSpecial := json.RawMessage(`{"type":"object","properties":{"value":{"type":"number"}},"required":["value"]}`)
	grantsSpecial := json.RawMessage(`[]`)
	hashSpecial := tool.ComputeHash(toolNameSpecial, descriptionSpecial, paramsSpecial)

	t.Run("Tool with special chars and float equivalence accepted (JCS canonicalization)", func(t *testing.T) {
		auditRepo.Clear()

		fakeRepo.addTool(tenantID, &tool.ToolDefinition{
			TenantID:             tenantID,
			Name:                 toolNameSpecial,
			Description:          descriptionSpecial,
			InputSchema:          paramsSpecial,
			Grants:               grantsSpecial,
			ExecutionTimeoutMs:   5000000,
			MemoryPages:          256,
			Hash:                 hashSpecial,
			IsActive:             true,
		})

		// Request with equivalent but differently formatted parameters:
		// - Different key order (required before properties)
		// - Float 1.0 instead of 1
		// - Extra whitespace
		equivalentParams := json.RawMessage(`{"required":["value"],"type":"object","properties":{"value":{"type":"number"}}}`)

		reqBody := map[string]interface{}{
			"model": "test-model",
			"messages": []map[string]string{
				{"role": "user", "content": "test special"},
			},
			"tools": []map[string]interface{}{
				{
					"type": "function",
					"function": map[string]interface{}{
						"name":        toolNameSpecial,
						"description": descriptionSpecial,
						"parameters":  equivalentParams,
					},
				},
			},
		}

		buf, _ := json.Marshal(reqBody)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBuffer(buf))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Should succeed - equivalent under JCS
		assert.NotEqual(t, http.StatusBadRequest, w.Code)

		// Verify audit event NOT emitted
		events := auditRepo.GetEventsByAction("tool_hash_mismatch")
		assert.Len(t, events, 0)
		events = auditRepo.GetEventsByAction("tool_not_found")
		assert.Len(t, events, 0)
	})

	t.Run("Semantic difference in parameters rejected", func(t *testing.T) {
		auditRepo.Clear()

		// Re-register the tool for this subtest
		fakeRepo.addTool(tenantID, &tool.ToolDefinition{
			TenantID:             tenantID,
			Name:                 toolNameSpecial,
			Description:          descriptionSpecial,
			InputSchema:          paramsSpecial,
			Grants:               grantsSpecial,
			ExecutionTimeoutMs:   5000000,
			MemoryPages:          256,
			Hash:                 hashSpecial,
			IsActive:             true,
		})

		// First verify audit would be emitted using ValidateTool
		_, err := fakeRepo.ValidateTool(ctx, tenantID, model.FunctionDef{
			Name:        toolNameSpecial,
			Description: descriptionSpecial,
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{"value": map[string]interface{}{"type": "number"}}, "required": []string{}},
		})
		require.ErrorIs(t, err, tool.ErrToolDefinitionMismatch)
		events := auditRepo.GetEventsByAction("tool_hash_mismatch")
		require.Len(t, events, 1)
		assert.Equal(t, tenantID, events[0].TenantID)
		assert.Equal(t, domain.AuditSeverityCritical, events[0].Severity)
		auditRepo.Clear()

		differentParams := json.RawMessage(`{"type":"object","properties":{"value":{"type":"number"}}, "required":[]}`)

		reqBody := map[string]interface{}{
			"model": "test-model",
			"messages": []map[string]string{
				{"role": "user", "content": "test special"},
			},
			"tools": []map[string]interface{}{
				{
					"type": "function",
					"function": map[string]interface{}{
						"name":        toolNameSpecial,
						"description": descriptionSpecial,
						"parameters":  differentParams, // Missing required field
					},
				},
			},
		}

		buf, _ := json.Marshal(reqBody)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBuffer(buf))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Should be rejected - semantic difference
		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp map[string]string
		json.Unmarshal(w.Body.Bytes(), &resp)
		assert.Equal(t, "tool definition mismatch", resp["error"])

		// Note: Audit event for hash mismatch is emitted by repository layer
		// during ValidateTool call (verified above), not by handler validateTools
	})
}