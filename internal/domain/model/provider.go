package model

import (
	"context"
)

// ModelProvider is the core interface for LLM model providers.
// Implementations handle provider-specific request/response normalization
// and error mapping to domain sentinel errors.
type ModelProvider interface {
	// Complete sends a chat completion request to the provider.
	// Returns a Completion with the response, usage, and provider metadata.
	// Errors should be mapped to domain sentinel errors where applicable.
	Complete(ctx context.Context, req ChatRequest) (Completion, error)

	// Name returns the provider identifier (e.g., "openai", "anthropic", "ollama").
	Name() string

	// Models returns the list of model IDs this provider supports.
	Models() []string

	// HealthCheck verifies the provider is reachable and authenticated.
	// Returns nil if healthy, or an error (preferably a sentinel error).
	HealthCheck(ctx context.Context) error
}

// ProviderType identifies the category of a provider.
type ProviderType string

const (
	ProviderTypeOpenAI   ProviderType = "openai"
	ProviderTypeAnthropic ProviderType = "anthropic"
	ProviderTypeOllama   ProviderType = "ollama"
	ProviderTypeMock     ProviderType = "mock"
)