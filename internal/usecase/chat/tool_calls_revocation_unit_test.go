//go:build unit
// +build unit

package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/tool/wazero"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/pricing"
	providermock "github.com/ezequielranieri/agent-gateway/internal/adapter/provider/mock"
	toolmock "github.com/ezequielranieri/agent-gateway/internal/adapter/tool/mock"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
	"github.com/ezequielranieri/agent-gateway/internal/domain/tool"
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
				Action:   "tool_repo_error",
				Severity: domain.AuditSeverityCritical,
				EntityType: "tool",
				EntityID:   nil,
				Payload:    json.RawMessage(fmt.Sprintf(`{"tool":"%s","error":"repo_error"}`, name)),
			})
		}
		return nil, f.err
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

// The rest of ToolRepository interface methods (not used in these tests)
func (f *fakeToolRepository) CreateToolDefinition(ctx context.Context, tenantID domain.UUID, def *tool.ToolDefinition) error { return nil }
func (f *fakeToolRepository) UpdateToolDefinition(ctx context.Context, tenantID domain.UUID, def *tool.ToolDefinition) (bool, error) { return false, nil }
func (f *fakeToolRepository) DeactivateToolDefinition(ctx context.Context, tenantID domain.UUID, name string) error { return nil }
func (f *fakeToolRepository) UpsertToolDefinition(ctx context.Context, tenantID domain.UUID, def *tool.ToolDefinition) error { return nil }
func (f *fakeToolRepository) InitFromConfig(ctx context.Context, tenantID domain.UUID, cfg *tool.ToolConfig) error { return nil }

func TestToolCalls_RevocationAndValidation_Unit(t *testing.T) {
	ctx := context.Background()
	logger := zerolog.New(zerolog.NewTestWriter(t)).Level(zerolog.DebugLevel)

	tenantID := domain.MustParseUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	toolName := "read_file"
	description := "Read a file from filesystem"
	parameters := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
	grants := json.RawMessage(`["filesystem:read"]`)
	hash := tool.ComputeHash(toolName, description, parameters)

	// Setup fake audit repo
	auditRepo := newFakeAuditRepository()

	// Setup fake repo with a registered tool
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

	// Build chat usecase with tool validation
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

	// Create a mock provider that responds to tool results
	// When it sees tool messages (results of rejected tool calls), it returns a simple text response
	// When it sees no tool calls in the response, it returns a normal response
	var modelCallCount int
	mockProvider := providermock.NewProvider(
		providermock.WithName("test-provider"),
		providermock.WithModels([]string{"test-model"}),
		providermock.WithEnabled(true),
		providermock.WithResponseFunc(func(req model.ChatRequest) (model.Completion, error) {
			modelCallCount++
			// Check if the last message is a tool result (indicates previous tool was rejected)
			hasToolResult := false
			for _, msg := range req.Messages {
				if msg.Role == "tool" {
					hasToolResult = true
					break
				}
			}
			
			if hasToolResult {
				// Model received tool result, now responds without tool calls
				return model.Completion{
					Response: model.ChatResponse{
						ID:      fmt.Sprintf("mock-completion-%d", modelCallCount),
						Object:  "chat.completion",
						Created: time.Now().Unix(),
						Model:   req.Model,
						Choices: []model.Choice{
							{
								Index: 0,
								Message: model.Message{
									Role:    "assistant",
									Content: "Tool was rejected, continuing without it",
								},
								FinishReason: "stop",
							},
						},
						Usage: model.Usage{
							PromptTokens:     10,
							CompletionTokens: 20,
							TotalTokens:      30,
						},
					},
					Provider:  "test-provider",
					Model:     req.Model,
					LatencyMs: 10,
				}, nil
			}
			
			// First call - return the tool calls we injected
			// This shouldn't happen in our test since we inject the FallbackResult directly
			return model.Completion{
				Response: model.ChatResponse{
					ID:      fmt.Sprintf("mock-completion-%d", modelCallCount),
					Object:  "chat.completion",
					Created: time.Now().Unix(),
					Model:   req.Model,
					Choices: []model.Choice{
						{
							Index: 0,
							Message: model.Message{
								Role:    "assistant",
								Content: "I'll help you",
							},
							FinishReason: "stop",
						},
					},
					Usage: model.Usage{
						PromptTokens:     10,
						CompletionTokens: 20,
						TotalTokens:      30,
					},
				},
				Provider:  "test-provider",
				Model:     req.Model,
				LatencyMs: 10,
			}, nil
		}),
	)

	toolExecutor := toolmock.NewMockExecutor(
		toolmock.WithSupportedTools(toolName),
		toolmock.WithLatency(10*time.Millisecond),
	)

	chatUC, err := BuildChatUsecaseFromConfigWithProvider(
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

	t.Run("Authorized tool set is immutable per request - tool_call outside set rejected", func(t *testing.T) {
		auditRepo.Clear()

		// This test verifies that once tools are validated in the request,
		// the authorized set cannot be changed by the model.
		// The model response might include a tool_call for a tool not in the request.
		// The orchestrator should reject it.

		// First, create a request with only "read_file" validated
		req := ChatRequest{
			Model: "test-model",
			Messages: []model.Message{
				{Role: "user", Content: "read a file"},
			},
			ValidatedTools: []wazero.ValidatedTool{
				{
					Name:                toolName,
					Description:         description,
					InputSchema:         parameters,
					Grants:              grants,
					ExecutionTimeoutMs:  5000000,
					MemoryPages:         256,
					Hash:                hash,
				},
			},
			TenantID: tenantID,
		}

		// Add "evil_tool" to the fake repo (exists in registry with valid hash)
		// This way the ONLY reason for rejection is it's not in the authorized set
		evilToolName := "evil_tool"
		evilDesc := "Malicious tool"
		evilParams := json.RawMessage(`{"type":"object","properties":{}}`)
		evilGrants := json.RawMessage(`[]`)
		evilHash := tool.ComputeHash(evilToolName, evilDesc, evilParams)
		fakeRepo.addTool(tenantID, &tool.ToolDefinition{
			TenantID:             tenantID,
			Name:                 evilToolName,
			Description:          evilDesc,
			InputSchema:          evilParams,
			Grants:               evilGrants,
			ExecutionTimeoutMs:   5000000,
			MemoryPages:          256,
			Hash:                 evilHash,
			IsActive:             true,
		})

		// Build a fake result with a tool_call for an unauthorized tool (evil_tool exists in registry but NOT in authorized set)
		result := FallbackResult{
			Completion: model.Completion{
				Model: "test-model",
				Response: model.ChatResponse{
					Choices: []model.Choice{
						{
							Message: model.Message{
								Role: "assistant",
								ToolCalls: []model.ToolCall{
									{
										ID:        "call_123",
										Type:      "function",
										Function: model.FunctionCall{Name: evilToolName, Arguments: `{}`},
									},
								},
							},
						},
					},
				},
			},
			Provider: "test-provider",
		}

		// Execute tool loop - should reject the unauthorized tool call
		finalResult, err := chatUC.ExecuteToolLoopForTest(ctx, req, model.ChatRequest{Model: "test-model"}, result)
		require.NoError(t, err)

		// The tool call should be rejected with an error result
		require.Len(t, finalResult.Completion.Response.Choices, 1)
		choice := finalResult.Completion.Response.Choices[0]
		require.Len(t, choice.Message.ToolCalls, 0) // No tool calls in final response

		// The mock executor should NOT have been called for the unauthorized tool
		assert.Equal(t, 0, toolExecutor.CallCount(), "Mock executor should not be called for unauthorized tool")

		// The final response should be the model's response after receiving the tool rejection
		assert.Contains(t, choice.Message.Content, "rejected", "Expected response mentioning tool rejection")

		// Note: No audit event expected here - rejection happens at authorized set check (before re-resolution/GetByName)
	})

	t.Run("Tool call for different tenant rejected", func(t *testing.T) {
		auditRepo.Clear()

		otherTenantID := domain.MustParseUUID("cccccccc-cccc-cccc-cccc-cccccccccccc")
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

		req := ChatRequest{
			Model: "test-model",
			Messages: []model.Message{
				{Role: "user", Content: "test"},
			},
			ValidatedTools: []wazero.ValidatedTool{
				{
					Name:                toolName,
					Description:         description,
					InputSchema:         parameters,
					Grants:              grants,
					ExecutionTimeoutMs:  5000000,
					MemoryPages:         256,
					Hash:                hash,
				},
			},
			TenantID: tenantID, // Request is for tenant A
		}

		// Model tries to call tool from tenant B
		result := FallbackResult{
			Completion: model.Completion{
				Model: "test-model",
				Response: model.ChatResponse{
					Choices: []model.Choice{
						{
							Message: model.Message{
								Role: "assistant",
								ToolCalls: []model.ToolCall{
									{
										ID:        "call_456",
										Type:      "function",
										Function: model.FunctionCall{Name: otherToolName, Arguments: `{}`},
									},
								},
							},
						},
					},
				},
			},
			Provider: "test-provider",
		}

		finalResult, err := chatUC.ExecuteToolLoopForTest(ctx, req, model.ChatRequest{Model: "test-model"}, result)
		require.NoError(t, err)

		// Should reject tool call for different tenant
		require.Len(t, finalResult.Completion.Response.Choices, 1)
		choice := finalResult.Completion.Response.Choices[0]
		require.Len(t, choice.Message.ToolCalls, 0)

		// The mock executor should NOT have been called for the cross-tenant tool
		assert.Equal(t, 0, toolExecutor.CallCount(), "Mock executor should not be called for cross-tenant tool")

		// The final response should be the model's response after receiving the tool rejection
		assert.Contains(t, choice.Message.Content, "rejected", "Expected response mentioning tool rejection")

		// Note: No audit event expected here - rejection happens at authorized set check (before re-resolution/GetByName)
		// The tool IS in the registry for tenant B, but not in the authorized set for tenant A
	})

	t.Run("Tool deleted mid-request cannot be executed", func(t *testing.T) {
		// Scenario: Request validated with tool X
		// Admin deletes tool X BETWEEN validation and execution
		// Model returns tool_call for X
		// Orchestrator re-resolves (tenant, name) + verifies hash
		// Should find tool deleted and reject

		req := ChatRequest{
			Model: "test-model",
			Messages: []model.Message{
				{Role: "user", Content: "read a file"},
			},
			ValidatedTools: []wazero.ValidatedTool{
				{
					Name:                toolName,
					Description:         description,
					InputSchema:         parameters,
					Grants:              grants,
					ExecutionTimeoutMs:  5000000,
					MemoryPages:         256,
					Hash:                hash,
				},
			},
			TenantID: tenantID,
		}

		// Tool exists in repo at validation time (simulated by ValidatedTools)
		// Admin deletes the tool BETWEEN validation and execution
		fakeRepo.removeTool(tenantID, toolName)

		result := FallbackResult{
			Completion: model.Completion{
				Model: "test-model",
				Response: model.ChatResponse{
					Choices: []model.Choice{
						{
							Message: model.Message{
								Role: "assistant",
								ToolCalls: []model.ToolCall{
									{
										ID:        "call_789",
										Type:      "function",
										Function: model.FunctionCall{Name: toolName, Arguments: `{}`},
									},
								},
							},
						},
					},
				},
			},
			Provider: "test-provider",
		}

		finalResult, err := chatUC.ExecuteToolLoopForTest(ctx, req, model.ChatRequest{Model: "test-model"}, result)
		require.NoError(t, err)

		// Should reject because tool was deleted
		require.Len(t, finalResult.Completion.Response.Choices, 1)
		choice := finalResult.Completion.Response.Choices[0]
		require.Len(t, choice.Message.ToolCalls, 0)

		// The mock executor should NOT have been called for the deleted tool
		assert.Equal(t, 0, toolExecutor.CallCount(), "Mock executor should not be called for deleted tool")

		// The final response should be the model's response after receiving the tool rejection
		assert.Contains(t, choice.Message.Content, "rejected", "Expected response mentioning tool rejection")
	})

	t.Run("Re-resolve at execution verifies hash matches", func(t *testing.T) {
		// At execution time, the orchestrator should:
		// 1. Lookup tool by (tenant_id, name) in registry
		// 2. Compare with stored hash
		// 3. If mismatch, reject (tool definition changed)

		req := ChatRequest{
			Model: "test-model",
			Messages: []model.Message{
				{Role: "user", Content: "read a file"},
			},
			ValidatedTools: []wazero.ValidatedTool{
				{
					Name:                toolName,
					Description:         description,
					InputSchema:         parameters,
					Grants:              grants,
					ExecutionTimeoutMs:  5000000,
					MemoryPages:         256,
					Hash:                hash,
				},
			},
			TenantID: tenantID,
		}

		// Admin changes the tool definition (hash changes)
		newDescription := "Modified description"
		newParams := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"recursive":{"type":"boolean"}},"required":["path"]}`)
		newHash := tool.ComputeHash(toolName, newDescription, newParams)
		fakeRepo.updateToolHash(tenantID, toolName, newHash)

		result := FallbackResult{
			Completion: model.Completion{
				Model: "test-model",
				Response: model.ChatResponse{
					Choices: []model.Choice{
						{
							Message: model.Message{
								Role: "assistant",
								ToolCalls: []model.ToolCall{
									{
										ID:        "call_999",
										Type:      "function",
										Function: model.FunctionCall{Name: toolName, Arguments: `{}`},
									},
								},
							},
						},
					},
				},
			},
			Provider: "test-provider",
		}

		finalResult, err := chatUC.ExecuteToolLoopForTest(ctx, req, model.ChatRequest{Model: "test-model"}, result)
		require.NoError(t, err)

		// Should reject because hash mismatch
		require.Len(t, finalResult.Completion.Response.Choices, 1)
		choice := finalResult.Completion.Response.Choices[0]
		require.Len(t, choice.Message.ToolCalls, 0)

		// The mock executor should NOT have been called due to hash mismatch
		assert.Equal(t, 0, toolExecutor.CallCount(), "Mock executor should not be called for hash mismatch")

		// The final response should be the model's response after receiving the tool rejection
		assert.Contains(t, choice.Message.Content, "rejected", "Expected response mentioning tool rejection")
	})

	t.Run("Repo error at re-resolution fails closed", func(t *testing.T) {
		req := ChatRequest{
			Model: "test-model",
			Messages: []model.Message{
				{Role: "user", Content: "read a file"},
			},
			ValidatedTools: []wazero.ValidatedTool{
				{
					Name:                toolName,
					Description:         description,
					InputSchema:         parameters,
					Grants:              grants,
					ExecutionTimeoutMs:  5000000,
					MemoryPages:         256,
					Hash:                hash,
				},
			},
			TenantID: tenantID,
		}

		fakeRepo.setError(assert.AnError)

		result := FallbackResult{
			Completion: model.Completion{
				Model: "test-model",
				Response: model.ChatResponse{
					Choices: []model.Choice{
						{
							Message: model.Message{
								Role: "assistant",
								ToolCalls: []model.ToolCall{
									{
										ID:        "call_111",
										Type:      "function",
										Function: model.FunctionCall{Name: toolName, Arguments: `{}`},
									},
								},
							},
						},
					},
				},
			},
			Provider: "test-provider",
		}

		finalResult, err := chatUC.ExecuteToolLoopForTest(ctx, req, model.ChatRequest{Model: "test-model"}, result)
		require.NoError(t, err)

		// Fail-closed: repo error should reject the tool call
		require.Len(t, finalResult.Completion.Response.Choices, 1)
		choice := finalResult.Completion.Response.Choices[0]
		require.Len(t, choice.Message.ToolCalls, 0)

		// The mock executor should NOT have been called due to repo error
		assert.Equal(t, 0, toolExecutor.CallCount(), "Mock executor should not be called for repo failure")

		// The final response should be the model's response after receiving the tool rejection
		assert.Contains(t, choice.Message.Content, "rejected", "Expected response mentioning tool rejection")
	})
}