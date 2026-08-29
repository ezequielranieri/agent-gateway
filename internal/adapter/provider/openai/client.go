package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
)

// Client implements the ModelProvider interface for OpenAI
type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	models     []string
}

// OpenAIRequest represents the OpenAI chat completion request format
type OpenAIRequest struct {
	Model            string          `json:"model"`
	Messages         []OpenAIMessage `json:"messages"`
	Temperature      *float64        `json:"temperature,omitempty"`
	MaxTokens        *int            `json:"max_tokens,omitempty"`
	TopP             *float64        `json:"top_p,omitempty"`
	Tools            []OAITool       `json:"tools,omitempty"`
	ToolChoice       any             `json:"tool_choice,omitempty"`
	Stream           bool            `json:"stream,omitempty"`
	Stop             []string        `json:"stop,omitempty"`
	PresencePenalty  *float64        `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64        `json:"frequency_penalty,omitempty"`
	User             string          `json:"user,omitempty"`
}

// OpenAIMessage represents a message in OpenAI format
type OpenAIMessage struct {
	Role       string        `json:"role"`
	Content    string        `json:"content,omitempty"`
	ToolCalls  []OAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Name       string        `json:"name,omitempty"`
}

// OAIToolCall represents a tool call in OpenAI format
type OAIToolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function OAIFunc  `json:"function"`
}

// OAIFunc represents a function call
type OAIFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// OAITool represents a tool definition in OpenAI format
type OAITool struct {
	Type     string    `json:"type"`
	Function OAIFuncDef `json:"function"`
}

// OAIFuncDef represents a function definition
type OAIFuncDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// OpenAIResponse represents the OpenAI chat completion response
type OpenAIResponse struct {
	ID                string         `json:"id"`
	Object            string         `json:"object"`
	Created           int64          `json:"created"`
	Model             string         `json:"model"`
	Choices           []OAIChoice    `json:"choices"`
	Usage             OAIUsage       `json:"usage"`
	SystemFingerprint string         `json:"system_fingerprint,omitempty"`
	Error             *OAIError      `json:"error,omitempty"`
}

// OAIChoice represents a completion choice
type OAIChoice struct {
	Index        int           `json:"index"`
	Message      OpenAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

// OAIUsage represents token usage
type OAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// OAIError represents an OpenAI API error
type OAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

// NewClient creates a new OpenAI client
func NewClient(apiKey, baseURL string, timeout time.Duration, models []string) *Client {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if len(models) == 0 {
		models = []string{"gpt-4", "gpt-4-turbo", "gpt-3.5-turbo"}
	}

	return &Client{
		apiKey:  apiKey,
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		models: models,
	}
}

// Name returns the provider identifier
func (c *Client) Name() string {
	return "openai"
}

// Models returns the list of model IDs this provider supports
func (c *Client) Models() []string {
	return c.models
}

// HealthCheck verifies the provider is reachable and authenticated
func (c *Client) HealthCheck(ctx context.Context) error {
	// Use a minimal request to /models endpoint to check connectivity
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/models", nil)
	if err != nil {
		return model.ErrProviderUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return model.ErrProviderTimeout
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return model.ErrProviderAuthFailed
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return model.ErrProviderRateLimited
	}
	if resp.StatusCode >= 500 {
		return model.ErrProviderUnavailable
	}
	return nil
}

// Complete sends a chat completion request to OpenAI
func (c *Client) Complete(ctx context.Context, req model.ChatRequest) (model.Completion, error) {
	startTime := time.Now()

	// Convert domain request to OpenAI format
	oaiReq := c.convertRequest(req)

	// Marshal request
	body, err := json.Marshal(oaiReq)
	if err != nil {
		return model.Completion{}, fmt.Errorf("marshal request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return model.Completion{}, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	// Send request
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return model.Completion{}, model.ErrProviderTimeout
	}
	defer resp.Body.Close()

	// Read response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return model.Completion{}, fmt.Errorf("read response: %w", err)
	}

	// Handle non-2xx responses
	if resp.StatusCode != http.StatusOK {
		var errResp OpenAIResponse
		if err := json.Unmarshal(respBody, &errResp); err != nil {
			return model.Completion{}, MapError(resp.StatusCode, string(respBody))
		}
		if errResp.Error != nil {
			return model.Completion{}, MapError(resp.StatusCode, errResp.Error.Message)
		}
		return model.Completion{}, MapError(resp.StatusCode, string(respBody))
	}

	// Parse successful response
	var oaiResp OpenAIResponse
	if err := json.Unmarshal(respBody, &oaiResp); err != nil {
		return model.Completion{}, fmt.Errorf("unmarshal response: %w", err)
	}

	// Convert to domain completion
	completion := c.convertResponse(oaiReq.Model, oaiResp, time.Since(startTime).Milliseconds())
	return completion, nil
}

// convertRequest converts domain ChatRequest to OpenAI format
func (c *Client) convertRequest(req model.ChatRequest) OpenAIRequest {
	messages := make([]OpenAIMessage, len(req.Messages))
	for i, msg := range req.Messages {
		messages[i] = OpenAIMessage{
			Role:       msg.Role,
			Content:    msg.Content,
			ToolCalls:  convertToolCalls(msg.ToolCalls),
			ToolCallID: msg.ToolCallID,
			Name:       msg.Name,
		}
	}

	tools := make([]OAITool, len(req.Tools))
	for i, tool := range req.Tools {
		tools[i] = OAITool{
			Type: tool.Type,
			Function: OAIFuncDef{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Parameters:  tool.Function.Parameters,
			},
		}
	}

	return OpenAIRequest{
		Model:            req.Model,
		Messages:         messages,
		Temperature:      req.Temperature,
		MaxTokens:        req.MaxTokens,
		TopP:             req.TopP,
		Tools:            tools,
		ToolChoice:       req.ToolChoice,
		Stream:           req.Stream,
		Stop:             req.Stop,
		PresencePenalty:  req.PresencePenalty,
		FrequencyPenalty: req.FrequencyPenalty,
		User:             req.User,
	}
}

// convertToolCalls converts domain tool calls to OpenAI format
func convertToolCalls(calls []model.ToolCall) []OAIToolCall {
	if calls == nil {
		return nil
	}
	result := make([]OAIToolCall, len(calls))
	for i, call := range calls {
		result[i] = OAIToolCall{
			ID:   call.ID,
			Type: call.Type,
			Function: OAIFunc{
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			},
		}
	}
	return result
}

// convertResponse converts OpenAI response to domain Completion
func (c *Client) convertResponse(requestedModel string, resp OpenAIResponse, latencyMs int64) model.Completion {
	choices := make([]model.Choice, len(resp.Choices))
	for i, choice := range resp.Choices {
		choices[i] = model.Choice{
			Index: choice.Index,
			Message: model.Message{
				Role:       choice.Message.Role,
				Content:    choice.Message.Content,
				ToolCalls:  convertToolCallsBack(choice.Message.ToolCalls),
				ToolCallID: choice.Message.ToolCallID,
				Name:       choice.Message.Name,
			},
			FinishReason: choice.FinishReason,
		}
	}

	return model.Completion{
		Response: model.ChatResponse{
			ID:                resp.ID,
			Object:            resp.Object,
			Created:           resp.Created,
			Model:             resp.Model,
			Choices:           choices,
			Usage: model.Usage{
				PromptTokens:     resp.Usage.PromptTokens,
				CompletionTokens: resp.Usage.CompletionTokens,
				TotalTokens:      resp.Usage.TotalTokens,
			},
			SystemFingerprint: resp.SystemFingerprint,
		},
		Provider:  "openai",
		Model:     resp.Model,
		LatencyMs: latencyMs,
	}
}

// convertToolCallsBack converts OpenAI tool calls to domain format
func convertToolCallsBack(calls []OAIToolCall) []model.ToolCall {
	if calls == nil {
		return nil
	}
	result := make([]model.ToolCall, len(calls))
	for i, call := range calls {
		result[i] = model.ToolCall{
			ID:   call.ID,
			Type: call.Type,
			Function: model.FunctionCall{
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			},
		}
	}
	return result
}