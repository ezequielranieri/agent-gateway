package anthropic

import (
	"context"
	"testing"

	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
	"github.com/stretchr/testify/assert"
)

func TestNewClient(t *testing.T) {
	client := NewClient("test-key", "", nil, true)

	assert.Equal(t, "anthropic", client.Name())
	assert.Equal(t, "https://api.anthropic.com/v1", client.baseURL)
	assert.Contains(t, client.Models(), "claude-3-opus-20240229")
	assert.True(t, client.enabled)
}

func TestNewClient_Disabled(t *testing.T) {
	client := NewClient("test-key", "", nil, false)

	assert.False(t, client.enabled)
}

func TestNewClient_CustomModels(t *testing.T) {
	client := NewClient("test-key", "", []string{"custom-1", "custom-2"}, true)

	assert.Equal(t, []string{"custom-1", "custom-2"}, client.Models())
}

func TestClient_HealthCheck_Enabled(t *testing.T) {
	client := NewClient("test-key", "", nil, true)

	err := client.HealthCheck(context.Background())
	assert.ErrorIs(t, err, ErrNotImplemented)
}

func TestClient_HealthCheck_Disabled(t *testing.T) {
	client := NewClient("test-key", "", nil, false)

	err := client.HealthCheck(context.Background())
	assert.ErrorIs(t, err, model.ErrProviderUnavailable)
}

func TestClient_Complete_Enabled(t *testing.T) {
	client := NewClient("test-key", "", nil, true)

	_, err := client.Complete(context.Background(), model.ChatRequest{Model: "claude-3-opus", Messages: []model.Message{{Role: "user", Content: "test"}}})
	assert.ErrorIs(t, err, ErrNotImplemented)
}

func TestClient_Complete_Disabled(t *testing.T) {
	client := NewClient("test-key", "", nil, false)

	_, err := client.Complete(context.Background(), model.ChatRequest{Model: "claude-3-opus", Messages: []model.Message{{Role: "user", Content: "test"}}})
	assert.ErrorIs(t, err, model.ErrProviderUnavailable)
}

func TestErrNotImplemented(t *testing.T) {
	assert.Error(t, ErrNotImplemented)
	assert.Equal(t, "anthropic adapter not fully implemented (config-gated stub)", ErrNotImplemented.Error())
}