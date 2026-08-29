package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient(t *testing.T) {
	client := NewClient("test-key", "", 30*time.Second, []string{"gpt-4"})

	assert.Equal(t, "openai", client.Name())
	assert.Equal(t, "https://api.openai.com/v1", client.baseURL)
	assert.Equal(t, 30*time.Second, client.httpClient.Timeout)
	assert.Contains(t, client.Models(), "gpt-4")
}

func TestNewClient_Defaults(t *testing.T) {
	client := NewClient("test-key", "", 0, nil)

	assert.Equal(t, "https://api.openai.com/v1", client.baseURL)
	assert.Contains(t, client.Models(), "gpt-4")
	assert.Contains(t, client.Models(), "gpt-3.5-turbo")
}

func TestClient_HealthCheck_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "gpt-4"}}})
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, 5*time.Second, []string{"gpt-4"})

	err := client.HealthCheck(context.Background())
	assert.NoError(t, err)
}

func TestClient_HealthCheck_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := NewClient("invalid-key", server.URL, 5*time.Second, []string{"gpt-4"})

	err := client.HealthCheck(context.Background())
	assert.ErrorIs(t, err, model.ErrProviderAuthFailed)
}

func TestClient_HealthCheck_RateLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, 5*time.Second, []string{"gpt-4"})

	err := client.HealthCheck(context.Background())
	assert.ErrorIs(t, err, model.ErrProviderRateLimited)
}

func TestClient_Complete_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))

		var req OpenAIRequest
		json.NewDecoder(r.Body).Decode(&req)
		assert.Equal(t, "gpt-4", req.Model)
		assert.Len(t, req.Messages, 1)
		assert.Equal(t, "user", req.Messages[0].Role)
		assert.Equal(t, "Hello", req.Messages[0].Content)

		resp := OpenAIResponse{
			ID:      "chatcmpl-123",
			Object:  "chat.completion",
			Created: 1234567890,
			Model:   "gpt-4",
			Choices: []OAIChoice{
				{
					Index: 0,
					Message: OpenAIMessage{
						Role:    "assistant",
						Content: "Hello there!",
					},
					FinishReason: "stop",
				},
			},
			Usage: OAIUsage{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, 5*time.Second, []string{"gpt-4"})

	req := model.ChatRequest{
		Model: "gpt-4",
		Messages: []model.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	completion, err := client.Complete(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, "openai", completion.Provider)
	assert.Equal(t, "gpt-4", completion.Model)
	assert.Equal(t, "chatcmpl-123", completion.Response.ID)
	assert.Len(t, completion.Response.Choices, 1)
	assert.Equal(t, "assistant", completion.Response.Choices[0].Message.Role)
	assert.Equal(t, "Hello there!", completion.Response.Choices[0].Message.Content)
	assert.Equal(t, 10, completion.Response.Usage.PromptTokens)
	assert.Equal(t, 5, completion.Response.Usage.CompletionTokens)
	assert.GreaterOrEqual(t, completion.LatencyMs, int64(0))
}

func TestClient_Complete_WithTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req OpenAIRequest
		json.NewDecoder(r.Body).Decode(&req)

		assert.Len(t, req.Tools, 1)
		assert.Equal(t, "function", req.Tools[0].Type)
		assert.Equal(t, "get_weather", req.Tools[0].Function.Name)

		resp := OpenAIResponse{
			ID:      "chatcmpl-123",
			Object:  "chat.completion",
			Created: 1234567890,
			Model:   "gpt-4",
			Choices: []OAIChoice{
				{
					Index: 0,
					Message: OpenAIMessage{
						Role: "assistant",
						ToolCalls: []OAIToolCall{
							{
								ID:   "call_123",
								Type: "function",
								Function: OAIFunc{
									Name:      "get_weather",
									Arguments: `{"location":"NYC"}`,
								},
							},
						},
					},
					FinishReason: "tool_calls",
				},
			},
			Usage: OAIUsage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, 5*time.Second, []string{"gpt-4"})

	req := model.ChatRequest{
		Model: "gpt-4",
		Messages: []model.Message{
			{Role: "user", Content: "What's the weather in NYC?"},
		},
		Tools: []model.Tool{
			{
				Type: "function",
				Function: model.FunctionDef{
					Name:        "get_weather",
					Description: "Get weather",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"location": map[string]any{"type": "string"},
						},
						"required": []string{"location"},
					},
				},
			},
		},
	}

	completion, err := client.Complete(context.Background(), req)

	require.NoError(t, err)
	assert.Len(t, completion.Response.Choices, 1)
	assert.Len(t, completion.Response.Choices[0].Message.ToolCalls, 1)
	assert.Equal(t, "call_123", completion.Response.Choices[0].Message.ToolCalls[0].ID)
	assert.Equal(t, "get_weather", completion.Response.Choices[0].Message.ToolCalls[0].Function.Name)
}

func TestClient_Complete_RateLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(OpenAIResponse{
			Error: &OAIError{Message: "Rate limit exceeded"},
		})
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, 5*time.Second, []string{"gpt-4"})

	req := model.ChatRequest{
		Model: "gpt-4",
		Messages: []model.Message{{Role: "user", Content: "Hello"}},
	}

	_, err := client.Complete(context.Background(), req)
	assert.ErrorIs(t, err, model.ErrProviderRateLimited)
}

func TestClient_Complete_InvalidRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(OpenAIResponse{
			Error: &OAIError{Message: "Invalid request"},
		})
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, 5*time.Second, []string{"gpt-4"})

	req := model.ChatRequest{
		Model: "gpt-4",
		Messages: []model.Message{{Role: "user", Content: "Hello"}},
	}

	_, err := client.Complete(context.Background(), req)
	assert.ErrorIs(t, err, model.ErrProviderInvalidRequest)
}

func TestClient_Complete_ContextLengthExceeded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(OpenAIResponse{
			Error: &OAIError{Message: "This model's maximum context length is 4096 tokens"},
		})
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, 5*time.Second, []string{"gpt-4"})

	req := model.ChatRequest{
		Model: "gpt-4",
		Messages: []model.Message{{Role: "user", Content: "Hello"}},
	}

	_, err := client.Complete(context.Background(), req)
	assert.ErrorIs(t, err, model.ErrProviderInvalidRequest) // Context length maps to invalid request in our mapping
}

func TestClient_Complete_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, 5*time.Second, []string{"gpt-4"})

	req := model.ChatRequest{
		Model: "gpt-4",
		Messages: []model.Message{{Role: "user", Content: "Hello"}},
	}

	_, err := client.Complete(context.Background(), req)
	assert.ErrorIs(t, err, model.ErrProviderUnavailable)
}

func TestClient_Complete_Timeout(t *testing.T) {
	t.Skip("Skipping timeout test - httptest.Server blocks on close with hanging connections")
}

func TestMapError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		message    string
		wantErr    error
	}{
		{"unauthorized", http.StatusUnauthorized, "invalid key", model.ErrProviderAuthFailed},
		{"rate_limited", http.StatusTooManyRequests, "rate limited", model.ErrProviderRateLimited},
		{"not_found_model", http.StatusNotFound, "model not found", model.ErrProviderInvalidRequest},
		{"not_found_other", http.StatusNotFound, "other", model.ErrProviderInvalidRequest},
		{"bad_request", http.StatusBadRequest, "bad request", model.ErrProviderInvalidRequest},
		{"context_length", http.StatusBadRequest, "context_length_exceeded", model.ErrProviderInvalidRequest},
		{"server_error_500", http.StatusInternalServerError, "server error", model.ErrProviderUnavailable},
		{"server_error_502", http.StatusBadGateway, "bad gateway", model.ErrProviderUnavailable},
		{"server_error_503", http.StatusServiceUnavailable, "unavailable", model.ErrProviderUnavailable},
		{"server_error_504", http.StatusGatewayTimeout, "timeout", model.ErrProviderUnavailable},
		{"unknown", 418, "teapot", model.ErrProviderUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := MapError(tt.statusCode, tt.message)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestConvertRequest(t *testing.T) {
	client := NewClient("test-key", "", 30*time.Second, []string{"gpt-4"})

	req := model.ChatRequest{
		Model: "gpt-4",
		Messages: []model.Message{
			{Role: "system", Content: "You are helpful"},
			{Role: "user", Content: "Hello"},
			{
				Role: "assistant",
				ToolCalls: []model.ToolCall{
					{ID: "call_1", Type: "function", Function: model.FunctionCall{Name: "foo", Arguments: `{}`}},
				},
			},
			{Role: "tool", Content: "result", ToolCallID: "call_1"},
		},
		Temperature:      float64Ptr(0.7),
		MaxTokens:        intPtr(100),
		TopP:             float64Ptr(0.9),
		Tools:            []model.Tool{{Type: "function", Function: model.FunctionDef{Name: "bar"}}},
		ToolChoice:       "auto",
		Stream:           true,
		Stop:             []string{"END"},
		PresencePenalty:  float64Ptr(0.5),
		FrequencyPenalty: float64Ptr(0.3),
		User:             "user-123",
	}

	oaiReq := client.convertRequest(req)

	assert.Equal(t, "gpt-4", oaiReq.Model)
	assert.Len(t, oaiReq.Messages, 4)
	assert.Equal(t, "system", oaiReq.Messages[0].Role)
	assert.Equal(t, "user", oaiReq.Messages[1].Role)
	assert.Equal(t, "assistant", oaiReq.Messages[2].Role)
	assert.Len(t, oaiReq.Messages[2].ToolCalls, 1)
	assert.Equal(t, "tool", oaiReq.Messages[3].Role)
	assert.Equal(t, "call_1", oaiReq.Messages[3].ToolCallID)
	assert.Equal(t, 0.7, *oaiReq.Temperature)
	assert.Equal(t, 100, *oaiReq.MaxTokens)
	assert.Equal(t, 0.9, *oaiReq.TopP)
	assert.Len(t, oaiReq.Tools, 1)
	assert.Equal(t, "auto", oaiReq.ToolChoice)
	assert.True(t, oaiReq.Stream)
	assert.Equal(t, []string{"END"}, oaiReq.Stop)
	assert.Equal(t, 0.5, *oaiReq.PresencePenalty)
	assert.Equal(t, 0.3, *oaiReq.FrequencyPenalty)
	assert.Equal(t, "user-123", oaiReq.User)
}

func float64Ptr(f float64) *float64 { return &f }
func intPtr(i int) *int { return &i }