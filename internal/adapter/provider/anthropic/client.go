package anthropic

import (
	"context"
	"errors"

	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
)

// ErrNotImplemented is returned when the Anthropic adapter is not fully implemented
var ErrNotImplemented = errors.New("anthropic adapter not fully implemented (config-gated stub)")

// Client is a stub implementation of ModelProvider for Anthropic
// It satisfies the interface but returns ErrNotImplemented for all operations
// Enable by setting enabled: true in provider config
type Client struct {
	apiKey  string
	baseURL string
	models  []string
	enabled bool
}

// NewClient creates a new Anthropic stub client
func NewClient(apiKey, baseURL string, models []string, enabled bool) *Client {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com/v1"
	}
	if len(models) == 0 {
		models = []string{"claude-3-opus-20240229", "claude-3-sonnet-20240229", "claude-3-haiku-20240307"}
	}
	return &Client{
		apiKey:  apiKey,
		baseURL: baseURL,
		models:  models,
		enabled: enabled,
	}
}

// Name returns the provider identifier
func (c *Client) Name() string {
	return "anthropic"
}

// Models returns the list of model IDs this provider supports
func (c *Client) Models() []string {
	return c.models
}

// HealthCheck verifies the provider is reachable and authenticated
func (c *Client) HealthCheck(ctx context.Context) error {
	if !c.enabled {
		return model.ErrProviderUnavailable
	}
	return ErrNotImplemented
}

// Complete sends a chat completion request to Anthropic
func (c *Client) Complete(ctx context.Context, req model.ChatRequest) (model.Completion, error) {
	if !c.enabled {
		return model.Completion{}, model.ErrProviderUnavailable
	}
	return model.Completion{}, ErrNotImplemented
}