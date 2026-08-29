package mock

import (
	"context"
	"testing"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewProvider_Defaults(t *testing.T) {
	p := NewProvider()

	assert.Equal(t, "mock", p.Name())
	assert.Contains(t, p.Models(), "mock-model-1")
	assert.True(t, p.enabled)
}

func TestNewProvider_WithOptions(t *testing.T) {
	p := NewProvider(
		WithName("test-mock"),
		WithModels([]string{"custom-1", "custom-2"}),
		WithEnabled(false),
	)

	assert.Equal(t, "test-mock", p.Name())
	assert.Equal(t, []string{"custom-1", "custom-2"}, p.Models())
	assert.False(t, p.enabled)
}

func TestProvider_HealthCheck_Enabled(t *testing.T) {
	p := NewProvider(WithEnabled(true))

	err := p.HealthCheck(context.Background())
	assert.NoError(t, err)
}

func TestProvider_HealthCheck_Disabled(t *testing.T) {
	p := NewProvider(WithEnabled(false))

	err := p.HealthCheck(context.Background())
	assert.ErrorIs(t, err, model.ErrProviderUnavailable)
}

func TestProvider_Complete_Success(t *testing.T) {
	p := NewProvider()

	req := model.ChatRequest{
		Model: "mock-model-1",
		Messages: []model.Message{{Role: "user", Content: "Hello"}},
	}

	completion, err := p.Complete(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, "mock", completion.Provider)
	assert.Equal(t, "mock-model-1", completion.Model)
	assert.Len(t, completion.Response.Choices, 1)
	assert.Equal(t, "assistant", completion.Response.Choices[0].Message.Role)
	assert.Contains(t, completion.Response.Choices[0].Message.Content, "mock response")
	assert.Equal(t, int64(0), completion.LatencyMs)
}

func TestProvider_Complete_CallCount(t *testing.T) {
	p := NewProvider()

	for i := 0; i < 5; i++ {
		p.Complete(context.Background(), model.ChatRequest{Model: "test", Messages: []model.Message{{Role: "user", Content: "test"}}})
	}

	assert.Equal(t, int64(5), p.CallCount())
}

func TestProvider_Complete_WithFixedLatency(t *testing.T) {
	p := NewProvider(WithFixedLatency(50 * time.Millisecond))

	start := time.Now()
	p.Complete(context.Background(), model.ChatRequest{Model: "test", Messages: []model.Message{{Role: "user", Content: "test"}}})
	elapsed := time.Since(start)

	assert.GreaterOrEqual(t, elapsed, 50*time.Millisecond)
	assert.Less(t, elapsed, 200*time.Millisecond) // Should not be too much more
}

func TestProvider_Complete_WithCustomResponse(t *testing.T) {
	customResp := model.Completion{
		Response: model.ChatResponse{
			ID:      "custom-123",
			Model:   "custom-model",
			Choices: []model.Choice{{Index: 0, Message: model.Message{Role: "assistant", Content: "Custom response"}, FinishReason: "stop"}},
			Usage:   model.Usage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 15},
		},
		Provider: "mock",
		Model:    "custom-model",
		LatencyMs: 42,
	}

	p := NewProvider(WithCustomResponse(customResp))

	completion, err := p.Complete(context.Background(), model.ChatRequest{Model: "test", Messages: []model.Message{{Role: "user", Content: "test"}}})

	require.NoError(t, err)
	assert.Equal(t, "custom-123", completion.Response.ID)
	assert.Equal(t, "Custom response", completion.Response.Choices[0].Message.Content)
	assert.Equal(t, 5, completion.Response.Usage.PromptTokens)
	assert.Equal(t, int64(42), completion.LatencyMs)
	// Model should be overridden by request
	assert.Equal(t, "test", completion.Model)
}

func TestProvider_Complete_WithResponseFunc(t *testing.T) {
	p := NewProvider(WithResponseFunc(func(req model.ChatRequest) (model.Completion, error) {
		return model.Completion{
			Response: model.ChatResponse{
				Model: req.Model,
				Choices: []model.Choice{{Index: 0, Message: model.Message{Role: "assistant", Content: "Func: " + req.Messages[0].Content}, FinishReason: "stop"}},
				Usage: model.Usage{PromptTokens: len(req.Messages[0].Content), CompletionTokens: 10, TotalTokens: len(req.Messages[0].Content) + 10},
			},
			Provider: "mock-func",
			Model:    req.Model,
		}, nil
	}))

	completion, err := p.Complete(context.Background(), model.ChatRequest{Model: "test", Messages: []model.Message{{Role: "user", Content: "hello"}}})

	require.NoError(t, err)
	assert.Equal(t, "mock-func", completion.Provider)
	assert.Equal(t, "Func: hello", completion.Response.Choices[0].Message.Content)
	assert.Equal(t, 5, completion.Response.Usage.PromptTokens) // len("hello")
}

func TestProvider_Complete_ErrorSequence(t *testing.T) {
	p := NewProvider(
		WithErrorSequence([]error{ErrMockTimeout, ErrMockRateLimited, ErrMockAuthFailed}),
	)

	// First call - timeout
	_, err := p.Complete(context.Background(), model.ChatRequest{Model: "test", Messages: []model.Message{{Role: "user", Content: "test"}}})
	assert.ErrorIs(t, err, ErrMockTimeout)

	// Second call - rate limited
	_, err = p.Complete(context.Background(), model.ChatRequest{Model: "test", Messages: []model.Message{{Role: "user", Content: "test"}}})
	assert.ErrorIs(t, err, ErrMockRateLimited)

	// Third call - auth failed
	_, err = p.Complete(context.Background(), model.ChatRequest{Model: "test", Messages: []model.Message{{Role: "user", Content: "test"}}})
	assert.ErrorIs(t, err, ErrMockAuthFailed)

	// Fourth call - success (sequence exhausted)
	completion, err := p.Complete(context.Background(), model.ChatRequest{Model: "test", Messages: []model.Message{{Role: "user", Content: "test"}}})
	require.NoError(t, err)
	assert.Equal(t, "mock", completion.Provider)
}

func TestProvider_Complete_FailNext(t *testing.T) {
	p := NewProvider()

	// First call succeeds
	completion, err := p.Complete(context.Background(), model.ChatRequest{Model: "test", Messages: []model.Message{{Role: "user", Content: "test"}}})
	require.NoError(t, err)
	assert.Equal(t, "mock", completion.Provider)

	// Set up fail next
	p.AddErrorToSequence(ErrMockUnavailable)

	// Second call fails
	_, err = p.Complete(context.Background(), model.ChatRequest{Model: "test", Messages: []model.Message{{Role: "user", Content: "test"}}})
	assert.ErrorIs(t, err, ErrMockUnavailable)

	// Third call succeeds again
	completion, err = p.Complete(context.Background(), model.ChatRequest{Model: "test", Messages: []model.Message{{Role: "user", Content: "test"}}})
	require.NoError(t, err)
	assert.Equal(t, "mock", completion.Provider)
}

func TestProvider_Complete_ConfigurableError(t *testing.T) {
	// 100% error probability
	p := NewProvider(WithConfigurableError(ErrMockRateLimited, 1.0))

	for i := 0; i < 10; i++ {
		_, err := p.Complete(context.Background(), model.ChatRequest{Model: "test", Messages: []model.Message{{Role: "user", Content: "test"}}})
		assert.ErrorIs(t, err, ErrMockRateLimited)
	}
}

func TestProvider_Complete_ConfigurableError_NoError(t *testing.T) {
	// 0% error probability
	p := NewProvider(WithConfigurableError(ErrMockRateLimited, 0.0))

	for i := 0; i < 10; i++ {
		completion, err := p.Complete(context.Background(), model.ChatRequest{Model: "test", Messages: []model.Message{{Role: "user", Content: "test"}}})
		require.NoError(t, err)
		assert.Equal(t, "mock", completion.Provider)
	}
}

func TestProvider_Reset(t *testing.T) {
	p := NewProvider(
		WithErrorSequence([]error{ErrMockTimeout}),
		WithFixedLatency(10*time.Millisecond),
	)

	// Make a call that fails
	_, _ = p.Complete(context.Background(), model.ChatRequest{Model: "test", Messages: []model.Message{{Role: "user", Content: "test"}}})
	assert.Equal(t, int64(1), p.CallCount())

	// Reset
	p.Reset()

	// Should work now
	_, err := p.Complete(context.Background(), model.ChatRequest{Model: "test", Messages: []model.Message{{Role: "user", Content: "test"}}})
	require.NoError(t, err)
	assert.Equal(t, int64(1), p.CallCount()) // Reset doesn't clear call count in current impl
}

func TestProvider_SetEnabled(t *testing.T) {
	p := NewProvider(WithEnabled(true))

	err := p.HealthCheck(context.Background())
	assert.NoError(t, err)

	p.SetEnabled(false)

	err = p.HealthCheck(context.Background())
	assert.ErrorIs(t, err, model.ErrProviderUnavailable)

	p.SetEnabled(true)

	err = p.HealthCheck(context.Background())
	assert.NoError(t, err)
}

func TestProvider_SetFixedLatency(t *testing.T) {
	p := NewProvider()

	p.SetFixedLatency(100 * time.Millisecond)

	start := time.Now()
	p.Complete(context.Background(), model.ChatRequest{Model: "test", Messages: []model.Message{{Role: "user", Content: "test"}}})
	elapsed := time.Since(start)

	assert.GreaterOrEqual(t, elapsed, 100*time.Millisecond)
}

func TestProvider_AddErrorToSequence(t *testing.T) {
	p := NewProvider()

	p.AddErrorToSequence(ErrMockTimeout)
	p.AddErrorToSequence(ErrMockRateLimited)

	_, err := p.Complete(context.Background(), model.ChatRequest{Model: "test", Messages: []model.Message{{Role: "user", Content: "test"}}})
	assert.ErrorIs(t, err, ErrMockTimeout)

	_, err = p.Complete(context.Background(), model.ChatRequest{Model: "test", Messages: []model.Message{{Role: "user", Content: "test"}}})
	assert.ErrorIs(t, err, ErrMockRateLimited)

	// Third call succeeds
	completion, err := p.Complete(context.Background(), model.ChatRequest{Model: "test", Messages: []model.Message{{Role: "user", Content: "test"}}})
	require.NoError(t, err)
	assert.Equal(t, "mock", completion.Provider)
}

func TestPredefinedErrors(t *testing.T) {
	errors := []error{
		ErrMockTimeout,
		ErrMockRateLimited,
		ErrMockAuthFailed,
		ErrMockInvalidRequest,
		ErrMockUnavailable,
	}

	for _, err := range errors {
		assert.Error(t, err)
		assert.NotEmpty(t, err.Error())
	}
}