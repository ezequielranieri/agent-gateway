package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestProviderType_Constants(t *testing.T) {
	assert.Equal(t, "openai", string(ProviderTypeOpenAI))
	assert.Equal(t, "anthropic", string(ProviderTypeAnthropic))
	assert.Equal(t, "ollama", string(ProviderTypeOllama))
	assert.Equal(t, "mock", string(ProviderTypeMock))
}

func TestChatRequest_Defaults(t *testing.T) {
	req := ChatRequest{
		Model:    "gpt-4",
		Messages: []Message{{Role: "user", Content: "Hello"}},
	}

	assert.Equal(t, "gpt-4", req.Model)
	assert.Len(t, req.Messages, 1)
	assert.Equal(t, "user", req.Messages[0].Role)
	assert.Equal(t, "Hello", req.Messages[0].Content)
	assert.False(t, req.Stream)
	assert.Nil(t, req.Temperature)
	assert.Nil(t, req.MaxTokens)
}

func TestMessage_ToolCall(t *testing.T) {
	msg := Message{
		Role:      "assistant",
		Content:   "",
		ToolCalls: []ToolCall{{ID: "call_123", Type: "function", Function: FunctionCall{Name: "get_weather", Arguments: `{"location":"NYC"}`}}},
	}

	assert.Equal(t, "assistant", msg.Role)
	assert.Len(t, msg.ToolCalls, 1)
	assert.Equal(t, "call_123", msg.ToolCalls[0].ID)
	assert.Equal(t, "get_weather", msg.ToolCalls[0].Function.Name)
}

func TestTool_Definition(t *testing.T) {
	tool := Tool{
		Type: "function",
		Function: FunctionDef{
			Name:        "get_weather",
			Description: "Get current weather",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"location": map[string]any{"type": "string"},
				},
				"required": []string{"location"},
			},
		},
	}

	assert.Equal(t, "function", tool.Type)
	assert.Equal(t, "get_weather", tool.Function.Name)
	assert.NotNil(t, tool.Function.Parameters)
}

func TestUsage_Calculation(t *testing.T) {
	usage := Usage{
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
	}

	assert.Equal(t, 150, usage.TotalTokens)
	assert.Equal(t, 100, usage.PromptTokens)
	assert.Equal(t, 50, usage.CompletionTokens)
}

func TestChatResponse_Structure(t *testing.T) {
	resp := ChatResponse{
		ID:      "chatcmpl-123",
		Object:  "chat.completion",
		Created: 1234567890,
		Model:   "gpt-4",
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: "Hello!",
				},
				FinishReason: "stop",
			},
		},
		Usage: Usage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}

	assert.Equal(t, "chatcmpl-123", resp.ID)
	assert.Equal(t, "chat.completion", resp.Object)
	assert.Len(t, resp.Choices, 1)
	assert.Equal(t, "assistant", resp.Choices[0].Message.Role)
	assert.Equal(t, "stop", resp.Choices[0].FinishReason)
}

func TestCompletion_Metadata(t *testing.T) {
	comp := Completion{
		Response: ChatResponse{
			ID:    "chatcmpl-123",
			Model: "gpt-4",
		},
		Provider:  "openai",
		Model:     "gpt-4",
		LatencyMs: 150,
	}

	assert.Equal(t, "openai", comp.Provider)
	assert.Equal(t, "gpt-4", comp.Model)
	assert.Equal(t, int64(150), comp.LatencyMs)
}

func TestDefaultCircuitBreakerConfig(t *testing.T) {
	config := DefaultCircuitBreakerConfig()

	assert.True(t, config.Enabled)
	assert.Equal(t, 5, config.FailureThreshold)
	assert.Equal(t, 2, config.SuccessThreshold)
	assert.Equal(t, 30*time.Second, config.Timeout)
	assert.Equal(t, 3, config.HalfOpenMaxRequests)
}

func TestFallbackRule_Structure(t *testing.T) {
	rule := FallbackRule{
		OnErrors:     []string{"provider unavailable", "provider timeout"},
		NextProvider: "anthropic",
		MaxRetries:   2,
		Backoff:      100 * time.Millisecond,
	}

	assert.Len(t, rule.OnErrors, 2)
	assert.Equal(t, "anthropic", rule.NextProvider)
	assert.Equal(t, 2, rule.MaxRetries)
	assert.Equal(t, 100*time.Millisecond, rule.Backoff)
}

func TestProviderConfig_Structure(t *testing.T) {
	config := ProviderConfig{
		Name:     "openai-primary",
		Type:     ProviderTypeOpenAI,
		Models:   []string{"gpt-4", "gpt-3.5-turbo"},
		Priority: 1,
		Enabled:  true,
		APIKey:   "sk-test",
		Timeout:  30 * time.Second,
		CircuitBreaker: CircuitBreakerConfig{
			Enabled:            true,
			FailureThreshold:   5,
			SuccessThreshold:   2,
			Timeout:            30 * time.Second,
			HalfOpenMaxRequests: 3,
		},
	}

	assert.Equal(t, "openai-primary", config.Name)
	assert.Equal(t, ProviderTypeOpenAI, config.Type)
	assert.Len(t, config.Models, 2)
	assert.Equal(t, 1, config.Priority)
	assert.True(t, config.Enabled)
	assert.True(t, config.CircuitBreaker.Enabled)
}