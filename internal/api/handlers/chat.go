package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/tool/wazero"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
	"github.com/ezequielranieri/agent-gateway/internal/domain/tool"
	"github.com/ezequielranieri/agent-gateway/internal/middleware"
	"github.com/ezequielranieri/agent-gateway/internal/usecase/chat"
)

// ChatHandlers holds the chat completion handlers
type ChatHandlers struct {
	logger     zerolog.Logger
	usecase    *chat.ChatUsecase
	toolRepo   tool.ToolRepository
}

// NewChatHandlers creates new chat handlers with the chat usecase and tool repository
func NewChatHandlers(logger zerolog.Logger, usecase *chat.ChatUsecase, toolRepo tool.ToolRepository) *ChatHandlers {
	return &ChatHandlers{
		logger:   logger.With().Str("handler", "chat").Logger(),
		usecase:  usecase,
		toolRepo: toolRepo,
	}
}

// RegisterRoutes registers the chat routes
func (h *ChatHandlers) RegisterRoutes(r chi.Router) {
	// Chat completions endpoint - requires full middleware chain
	r.Post("/chat/completions", h.ChatCompletions)
}

// ChatCompletionRequest represents the OpenAI-compatible request
type ChatCompletionRequest struct {
	Model       string          `json:"model" validate:"required"`
	Messages    []ChatMessage   `json:"messages" validate:"required,min=1"`
	Temperature *float64        `json:"temperature,omitempty"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
	Stream      *bool           `json:"stream,omitempty"`
	Tools       json.RawMessage `json:"tools,omitempty"`
	ToolChoice  any             `json:"tool_choice,omitempty"`
	User        string          `json:"user,omitempty"`
}

// ChatMessage represents a chat message
type ChatMessage struct {
	Role       string          `json:"role" validate:"required,oneof=system user assistant tool"`
	Content    string          `json:"content"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
}

// ChatCompletionResponse represents the OpenAI-compatible response
type ChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
	Usage   ChatCompletionUsage    `json:"usage"`
	// Extensions for gateway metadata
	Provider string  `json:"provider,omitempty"`
	CostUSD  float64 `json:"cost_usd,omitempty"`
	LatencyMs int64  `json:"latency_ms,omitempty"`
	Retried  bool    `json:"retried,omitempty"`
}

// ChatCompletionChoice represents a single choice
type ChatCompletionChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// ChatCompletionUsage represents token usage
type ChatCompletionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ValidatedTool represents a tool definition validated against the registry
type ValidatedTool struct {
	Name         string
	Description  string
	InputSchema  json.RawMessage
	Grants       json.RawMessage
	FuelLimit    uint64
	MemoryPages  uint32
	Hash         string
}

// ChatCompletions handles POST /v1/chat/completions
func (h *ChatHandlers) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	logger := h.logger.With().Str("method", "ChatCompletions").Logger()

	var req ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Debug().Err(err).Msg("Invalid request body")
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	// Validate required fields
	if req.Model == "" {
		logger.Debug().Msg("Missing model")
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	if len(req.Messages) == 0 {
		logger.Debug().Msg("No messages provided")
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	// Get tenant and user from context for rate limit cost tracking
	tenantID, ok := middleware.GetTenantID(r)
	if !ok {
		logger.Debug().Msg("Missing tenant_id in context")
		h.writeError(w, r, http.StatusUnauthorized, domain.ErrUnauthorized)
		return
	}
	userID, _ := middleware.GetUserID(r)

	logger.Debug().
		Str("model", req.Model).
		Int("message_count", len(req.Messages)).
		Str("tenant_id", tenantID.String()).
		Str("user_id", userID.String()).
		Msg("Chat completion request")

	// Convert handler request to usecase request with tool validation
	usecaseReq, err := h.convertToUsecaseRequest(r.Context(), req, tenantID, userID)
	if err != nil {
		logger.Error().Err(err).Msg("Tool validation failed")
		h.writeError(w, r, h.mapError(err), err)
		return
	}

	// Execute chat completion via usecase
	ctx := r.Context()
	resp, err := h.usecase.Complete(ctx, usecaseReq)
	if err != nil {
		logger.Error().Err(err).Msg("Chat completion failed")
		h.writeError(w, r, h.mapError(err), err)
		return
	}

	// Convert usecase response to handler response
	handlerResp := h.convertFromUsecaseResponse(resp, req.Model)

	h.writeJSON(w, http.StatusOK, handlerResp)
}

// convertToUsecaseRequest converts handler request to usecase request with tool validation
func (h *ChatHandlers) convertToUsecaseRequest(
	ctx context.Context,
	req ChatCompletionRequest,
	tenantID domain.UUID,
	userID domain.UUID,
) (chat.ChatRequest, error) {
	messages := make([]model.Message, len(req.Messages))
	for i, msg := range req.Messages {
		messages[i] = model.Message{
			Role:       msg.Role,
			Content:    msg.Content,
			ToolCalls:  h.parseToolCalls(msg.ToolCalls),
			ToolCallID: msg.ToolCallID,
			Name:       msg.Name,
		}
	}

	var tools []model.Tool
	if len(req.Tools) > 0 {
		if err := json.Unmarshal(req.Tools, &tools); err != nil {
			h.logger.Warn().Err(err).Msg("Failed to parse tools")
		}
	}

	// Validate tools against registry if any tools provided
	var validatedTools []wazero.ValidatedTool
	if len(tools) > 0 && h.toolRepo != nil {
		validated, err := h.validateTools(ctx, tenantID, tools)
		if err != nil {
			return chat.ChatRequest{}, err
		}
		validatedTools = validated
	}

	return chat.ChatRequest{
		Model:           req.Model,
		Messages:        messages,
		Temperature:     req.Temperature,
		MaxTokens:       req.MaxTokens,
		Stream:          req.Stream != nil && *req.Stream,
		Tools:           tools,
		ValidatedTools:  validatedTools,
		ToolChoice:      req.ToolChoice,
		User:            userID.String(),
		TenantID:        tenantID,
	}, nil
}

// validateTools validates each tool in the request against the registry
// Returns validated tools with registry grants/limits, or error if validation fails
func (h *ChatHandlers) validateTools(ctx context.Context, tenantID domain.UUID, tools []model.Tool) ([]wazero.ValidatedTool, error) {
	validated := make([]wazero.ValidatedTool, 0, len(tools))

	for _, t := range tools {
		if t.Type != "function" {
			continue // Skip non-function tools for now
		}

		fn := t.Function
		// Lookup tool in registry by (tenant_id, name)
		def, err := h.toolRepo.GetByName(ctx, tenantID, fn.Name)
		if err != nil {
			if errors.Is(err, tool.ErrToolRepository) {
				h.logger.Error().Err(err).Str("tool", fn.Name).Str("tenant", tenantID.String()).Msg("Tool repository error")
				return nil, err // Return repo error directly (maps to 503)
			}
			h.logger.Warn().Err(err).Str("tool", fn.Name).Str("tenant", tenantID.String()).Msg("Tool not found in registry")
			return nil, tool.ErrToolNotFound
		}

		if !def.IsActive {
			h.logger.Warn().Str("tool", fn.Name).Str("tenant", tenantID.String()).Msg("Tool is inactive")
			return nil, tool.ErrToolNotFound
		}

		// Compute hash of request tool definition (name, description, parameters)
		paramsJSON, _ := json.Marshal(fn.Parameters)
		requestHash := tool.ComputeHash(fn.Name, fn.Description, paramsJSON)

		// Compare with registry hash
		if requestHash != def.Hash {
			h.logger.Error().
				Str("tool", fn.Name).
				Str("tenant", tenantID.String()).
				Str("request_hash", requestHash).
				Str("registry_hash", def.Hash).
				Msg("Tool definition hash mismatch - potential attack")
			// Audit rejection event with severity critical (mismatch = attack signal)
			// The audit is emitted by the repository layer on failed lookups
			return nil, tool.ErrToolDefinitionMismatch
		}

		// Build validated tool with registry grants/limits
		// Re-enviar al proveedor la definición del registry (no los bytes del request)
		validated = append(validated, wazero.ValidatedTool{
			Name:                def.Name,
			Description:         def.Description,
			InputSchema:         def.InputSchema,
			Grants:              def.Grants,
			ExecutionTimeoutMs:  def.ExecutionTimeoutMs,
			MemoryPages:         def.MemoryPages,
			Hash:                def.Hash,
		})
	}

	return validated, nil
}

// parseToolCalls parses tool calls from raw JSON
func (h *ChatHandlers) parseToolCalls(raw json.RawMessage) []model.ToolCall {
	if len(raw) == 0 {
		return nil
	}
	var calls []model.ToolCall
	if err := json.Unmarshal(raw, &calls); err != nil {
		h.logger.Warn().Err(err).Msg("Failed to parse tool calls")
		return nil
	}
	return calls
}

// convertFromUsecaseResponse converts usecase response to handler response
func (h *ChatHandlers) convertFromUsecaseResponse(
	resp chat.ChatResponse,
	requestedModel string,
) ChatCompletionResponse {
	choices := make([]ChatCompletionChoice, len(resp.Completion.Response.Choices))
	for i, choice := range resp.Completion.Response.Choices {
		choices[i] = ChatCompletionChoice{
			Index: choice.Index,
			Message: ChatMessage{
				Role:       choice.Message.Role,
				Content:    choice.Message.Content,
				ToolCalls:  h.marshalToolCalls(choice.Message.ToolCalls),
				ToolCallID: choice.Message.ToolCallID,
				Name:       choice.Message.Name,
			},
			FinishReason: choice.FinishReason,
		}
	}

	return ChatCompletionResponse{
		ID:        resp.Completion.Response.ID,
		Object:    resp.Completion.Response.Object,
		Created:   resp.Completion.Response.Created,
		Model:     resp.Completion.Response.Model,
		Choices:   choices,
		Usage: ChatCompletionUsage{
			PromptTokens:     resp.Completion.Response.Usage.PromptTokens,
			CompletionTokens: resp.Completion.Response.Usage.CompletionTokens,
			TotalTokens:      resp.Completion.Response.Usage.TotalTokens,
		},
		Provider:   resp.Provider,
		CostUSD:    resp.CostUSD,
		LatencyMs:  resp.LatencyMs,
		Retried:    resp.Retried,
	}
}

// marshalToolCalls marshals tool calls to JSON
func (h *ChatHandlers) marshalToolCalls(calls []model.ToolCall) json.RawMessage {
	if calls == nil {
		return nil
	}
	data, _ := json.Marshal(calls)
	return data
}

// mapError maps domain errors to HTTP status codes
func (h *ChatHandlers) mapError(err error) int {
	switch {
	case err == domain.ErrValidation:
		return http.StatusBadRequest
	case err == domain.ErrUnauthorized:
		return http.StatusUnauthorized
	case err == domain.ErrForbidden:
		return http.StatusForbidden
	case err == domain.ErrNotFound:
		return http.StatusNotFound
	case err == tool.ErrToolNotFound:
		return http.StatusBadRequest
	case err == tool.ErrToolDefinitionMismatch:
		return http.StatusBadRequest
	case err == tool.ErrToolRepository:
		return http.StatusServiceUnavailable
	case err == model.ErrProviderRateLimited:
		return http.StatusTooManyRequests
	case err == model.ErrProviderTimeout:
		return http.StatusGatewayTimeout
	case err == model.ErrProviderUnavailable,
		err == model.ErrNoHealthyProvider:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func (h *ChatHandlers) writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func (h *ChatHandlers) writeError(w http.ResponseWriter, r *http.Request, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}