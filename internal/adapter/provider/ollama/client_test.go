package ollama

import (
	"context"
	"testing"

	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
	"github.com/stretchr/testify/assert"
)

func TestNewClient(t *testing.T) {
	client := NewClient("", nil, true)

	assert.Equal(t, "ollama", client.Name())
	assert.Equal(t, "http://localhost:11434", client.baseURL)
	assert.Contains(t, client.Models(), "llama3")
	assert.True(t, client.enabled)
}

func TestNewClient_Disabled(t *testing.T) {
	client := NewClient("", nil, false)

	assert.False(t, client.enabled)
}

func TestNewClient_CustomConfig(t *testing.T) {
	client := NewClient("http://custom:11434", []string{"custom-1", "custom-2"}, true)

	assert.Equal(t, "http://custom:11434", client.baseURL)
	assert.Equal(t, []string{"custom-1", "custom-2"}, client.Models())
}

func TestClient_HealthCheck_Enabled(t *testing.T) {
	client := NewClient("", nil, true)

	err := client.HealthCheck(context.Background())
	assert.ErrorIs(t, err, ErrNotImplemented)
}

func TestClient_HealthCheck_Disabled(t *testing.T) {
	client := NewClient("", nil, false)

	err := client.HealthCheck(context.Background())
	assert.ErrorIs(t, err, model.ErrProviderUnavailable)
}

func TestClient_Complete_Enabled(t *testing.T) {
	client := NewClient("", nil, true)

	_, err := client.Complete(context.Background(), model.ChatRequest{Model: "llama3", Messages: []model.Message{{Role: "user", Content: "test"}}})
	assert.ErrorIs(t, err, ErrNotImplemented)
}

func TestClient_Complete_Disabled(t *testing.T) {
	client := NewClient("", nil, false)

	_, err := client.Complete(context.Background(), model.ChatRequest{Model: "llama3", Messages: []model.Message{{Role: "user", Content: "test"}}})
	assert.ErrorIs(t, err, model.ErrProviderUnavailable)
}

func TestErrNotImplemented(t *testing.T) {
	assert.Error(t, ErrNotImplemented)
	assert.Equal(t, "ollama adapter not fully implemented (config-gated stub)", ErrNotImplemented.Error())
}