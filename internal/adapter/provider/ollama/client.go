package ollama

import (
	"context"
	"errors"

	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
)

// ErrNotImplemented is returned when the Ollama adapter is not fully implemented
var ErrNotImplemented = errors.New("ollama adapter not fully implemented (config-gated stub)")

// Client is a stub implementation of ModelProvider for Ollama
// It satisfies the interface but returns ErrNotImplemented for all operations
// Enable by setting enabled: true in provider config
type Client struct {
	baseURL string
	models  []string
	enabled bool
}

// NewClient creates a new Ollama stub client
func NewClient(baseURL string, models []string, enabled bool) *Client {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if len(models) == 0 {
		models = []string{"llama3", "mistral", "codellama"}
	}
	return &Client{
		baseURL: baseURL,
		models:  models,
		enabled: enabled,
	}
}

// Name returns the provider identifier
func (c *Client) Name() string {
	return "ollama"
}

// Models returns the list of model IDs this provider supports
func (c *Client) Models() []string {
	return c.models
}

// HealthCheck verifies the provider is reachable
func (c *Client) HealthCheck(ctx context.Context) error {
	if !c.enabled {
		return model.ErrProviderUnavailable
	}
	return ErrNotImplemented
}

// Complete sends a chat completion request to Ollama
func (c *Client) Complete(ctx context.Context, req model.ChatRequest) (model.Completion, error) {
	if !c.enabled {
		return model.Completion{}, model.ErrProviderUnavailable
	}
	return model.Completion{}, ErrNotImplemented
}