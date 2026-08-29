package model

import (
	"time"
)

// RouterConfig holds the configuration for the model router.
type RouterConfig struct {
	// Providers is the list of provider configurations in priority order.
	// The first healthy provider is selected for each request.
	Providers []ProviderConfig `json:"providers"`

	// FallbackRules define fallback behavior for specific failure modes.
	FallbackRules []FallbackRule `json:"fallback_rules"`

	// DefaultTimeout is the default request timeout.
	DefaultTimeout time.Duration `json:"default_timeout"`

	// MaxRetries is the maximum number of fallback retries.
	MaxRetries int `json:"max_retries"`

	// EnableCircuitBreaker enables circuit breaker integration.
	EnableCircuitBreaker bool `json:"enable_circuit_breaker"`
}

// ProviderConfig holds the configuration for a single model provider.
type ProviderConfig struct {
	// Name is the provider identifier (must match ModelProvider.Name()).
	Name string `json:"name"`

	// Type is the provider type (openai, anthropic, ollama, mock).
	Type ProviderType `json:"type"`

	// Models is the list of model IDs this provider serves.
	Models []string `json:"models"`

	// Priority determines selection order (lower = higher priority).
	Priority int `json:"priority"`

	// Enabled controls whether this provider is active.
	Enabled bool `json:"enabled"`

	// APIKey is the provider API key (env var reference supported via config).
	APIKey string `json:"api_key"`

	// BaseURL is the provider API base URL (for Ollama/custom endpoints).
	BaseURL string `json:"base_url,omitempty"`

	// Timeout is the request timeout for this provider.
	Timeout time.Duration `json:"timeout"`

	// MaxRetries is the max retries for this provider before fallback.
	MaxRetries int `json:"max_retries"`

	// CircuitBreaker configures the circuit breaker for this provider.
	CircuitBreaker CircuitBreakerConfig `json:"circuit_breaker"`

	// Metadata holds provider-specific configuration.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// FallbackRule defines a fallback condition and action.
type FallbackRule struct {
	// OnErrors lists the sentinel errors that trigger this fallback.
	OnErrors []string `json:"on_errors"`

	// NextProvider specifies the provider to fall back to (optional, uses next priority if empty).
	NextProvider string `json:"next_provider,omitempty"`

	// MaxRetries overrides the default max retries for this rule.
	MaxRetries int `json:"max_retries,omitempty"`

	// Backoff is the backoff duration between retries.
	Backoff time.Duration `json:"backoff,omitempty"`
}