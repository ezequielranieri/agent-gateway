package chat

import (
	"fmt"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/provider/anthropic"
	"github.com/ezequielranieri/agent-gateway/internal/adapter/provider/mock"
	"github.com/ezequielranieri/agent-gateway/internal/adapter/provider/ollama"
	"github.com/ezequielranieri/agent-gateway/internal/adapter/provider/openai"
	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
)

// createProviderFromConfig creates a provider instance from configuration
func createProviderFromConfig(config model.ProviderConfig) (model.ModelProvider, error) {
	switch config.Type {
	case model.ProviderTypeOpenAI:
		return newOpenAIProvider(config)
	case model.ProviderTypeAnthropic:
		return newAnthropicProvider(config)
	case model.ProviderTypeOllama:
		return newOllamaProvider(config)
	case model.ProviderTypeMock:
		return newMockProvider(config)
	default:
		return nil, fmt.Errorf("unknown provider type: %s", config.Type)
	}
}

// newOpenAIProvider creates an OpenAI provider
func newOpenAIProvider(config model.ProviderConfig) (model.ModelProvider, error) {
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	
	baseURL := config.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	
	apiKey := config.APIKey
	// Note: API key should be resolved from environment variables in config loading
	// For now, we pass it directly
	
	return openai.NewClient(apiKey, baseURL, timeout, config.Models), nil
}

// newAnthropicProvider creates an Anthropic provider
func newAnthropicProvider(config model.ProviderConfig) (model.ModelProvider, error) {
	baseURL := config.BaseURL
	if baseURL == "" {
		baseURL = "https://api.anthropic.com/v1"
	}
	
	apiKey := config.APIKey
	enabled := config.Enabled
	
	return anthropic.NewClient(apiKey, baseURL, config.Models, enabled), nil
}

// newOllamaProvider creates an Ollama provider
func newOllamaProvider(config model.ProviderConfig) (model.ModelProvider, error) {
	baseURL := config.BaseURL
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	
	enabled := config.Enabled
	
	return ollama.NewClient(baseURL, config.Models, enabled), nil
}

// newMockProvider creates a mock provider
func newMockProvider(config model.ProviderConfig) (model.ModelProvider, error) {
	provider := mock.NewProvider(
		mock.WithName(config.Name),
		mock.WithModels(config.Models),
		mock.WithEnabled(config.Enabled),
	)
	
	if config.Timeout > 0 {
		provider.SetFixedLatency(config.Timeout)
	}
	
	return provider, nil
}