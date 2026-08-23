package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/middleware"
)

// ChatHandlers holds the chat completion handlers
type ChatHandlers struct {
	logger zerolog.Logger
}

// NewChatHandlers creates new chat handlers
func NewChatHandlers(logger zerolog.Logger) *ChatHandlers {
	return &ChatHandlers{
		logger: logger.With().Str("handler", "chat").Logger(),
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
}

// ChatMessage represents a chat message
type ChatMessage struct {
	Role       string          `json:"role" validate:"required,oneof=system user assistant tool"`
	Content    string          `json:"content"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

// ChatCompletionResponse represents the OpenAI-compatible response
type ChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
	Usage   ChatCompletionUsage    `json:"usage"`
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
	tenantID, _ := middleware.GetTenantID(r)
	userID, _ := middleware.GetUserID(r)

	logger.Debug().
		Str("model", req.Model).
		Int("message_count", len(req.Messages)).
		Str("tenant_id", tenantID.String()).
		Str("user_id", userID.String()).
		Msg("Chat completion request")

	// TODO: Call upstream provider (placeholder - returns mock response for now)
	// For MVP, return a mock response that matches OpenAI format

	// Estimate tokens (rough approximation: ~4 chars per token)
	promptTokens := 0
	for _, msg := range req.Messages {
		promptTokens += len(msg.Content) / 4
	}
	if promptTokens == 0 {
		promptTokens = 100
	}

	maxTokens := 1000
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}

	completionTokens := maxTokens / 2 // Rough estimate
	totalTokens := promptTokens + completionTokens

	// Create mock response
	response := ChatCompletionResponse{
		ID:      "chatcmpl-" + domain.NewUUID().String()[:8],
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []ChatCompletionChoice{
			{
				Index: 0,
				Message: ChatMessage{
					Role:    "assistant",
					Content: "This is a mock response from the agent-gateway. Upstream provider integration pending.",
				},
				FinishReason: "stop",
			},
		},
		Usage: ChatCompletionUsage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      totalTokens,
		},
	}

	h.writeJSON(w, http.StatusOK, response)
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