package mock

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
)

// ConfigurableError represents an error that can be configured for testing
type ConfigurableError struct {
	Error       error
	Probability float64 // 0.0 to 1.0
}

// Provider is a mock implementation of ModelProvider for testing
// It provides controllable latency, errors, and failures
type Provider struct {
	name           string
	models         []string
	enabled        bool
	
	// Behavior controls
	mu              sync.RWMutex
	fixedLatency    time.Duration
	latencyJitter   time.Duration
	errorSequence   []error
	errorIndex      int
	configurableErr *ConfigurableError
	failNext        atomic.Bool
	callCount       atomic.Int64
	
	// Response customization
	customResponse  *model.Completion
	responseFunc    func(model.ChatRequest) (model.Completion, error)
}

// Option configures the mock provider
type Option func(*Provider)

// WithName sets the provider name
func WithName(name string) Option {
	return func(p *Provider) {
		p.name = name
	}
}

// WithModels sets the supported models
func WithModels(models []string) Option {
	return func(p *Provider) {
		p.models = models
	}
}

// WithEnabled sets whether the provider is enabled
func WithEnabled(enabled bool) Option {
	return func(p *Provider) {
		p.enabled = enabled
	}
}

// WithFixedLatency sets a fixed latency for all calls
func WithFixedLatency(latency time.Duration) Option {
	return func(p *Provider) {
		p.fixedLatency = latency
	}
}

// WithLatencyJitter adds random jitter to latency
func WithLatencyJitter(jitter time.Duration) Option {
	return func(p *Provider) {
		p.latencyJitter = jitter
	}
}

// WithErrorSequence sets a sequence of errors to return
func WithErrorSequence(errors []error) Option {
	return func(p *Provider) {
		p.errorSequence = errors
	}
}

// WithConfigurableError sets a probabilistic error
func WithConfigurableError(err error, probability float64) Option {
	return func(p *Provider) {
		p.configurableErr = &ConfigurableError{Error: err, Probability: probability}
	}
}

// WithFailNext makes the next call fail
func WithFailNext(err error) Option {
	return func(p *Provider) {
		p.failNext.Store(true)
		// Store error in a way we can retrieve it
		p.mu.Lock()
		p.errorSequence = append(p.errorSequence, err)
		p.mu.Unlock()
	}
}

// WithCustomResponse sets a fixed response for all calls
func WithCustomResponse(resp model.Completion) Option {
	return func(p *Provider) {
		p.customResponse = &resp
	}
}

// WithResponseFunc sets a custom response function
func WithResponseFunc(fn func(model.ChatRequest) (model.Completion, error)) Option {
	return func(p *Provider) {
		p.responseFunc = fn
	}
}

// NewProvider creates a new mock provider with the given options
func NewProvider(opts ...Option) *Provider {
	p := &Provider{
		name:   "mock",
		models: []string{"mock-model-1", "mock-model-2"},
		enabled: true,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name returns the provider identifier
func (p *Provider) Name() string {
	return p.name
}

// Models returns the list of model IDs this provider supports
func (p *Provider) Models() []string {
	return p.models
}

// HealthCheck verifies the provider is reachable
func (p *Provider) HealthCheck(ctx context.Context) error {
	if !p.enabled {
		return model.ErrProviderUnavailable
	}
	return nil
}

// Complete sends a chat completion request (mock implementation)
func (p *Provider) Complete(ctx context.Context, req model.ChatRequest) (model.Completion, error) {
	p.callCount.Add(1)

	// Check if we should fail
	if p.failNext.Load() {
		p.failNext.Store(false)
		p.mu.Lock()
		if len(p.errorSequence) > 0 {
			err := p.errorSequence[0]
			p.errorSequence = p.errorSequence[1:]
			p.mu.Unlock()
			return model.Completion{}, err
		}
		p.mu.Unlock()
		return model.Completion{}, model.ErrProviderUnavailable
	}

	// Check configurable error
	if p.configurableErr != nil {
		// Simple deterministic pseudo-random based on call count
		callNum := p.callCount.Load()
		if float64(callNum%100)/100.0 < p.configurableErr.Probability {
			return model.Completion{}, p.configurableErr.Error
		}
	}

	// Check error sequence
	p.mu.RLock()
	if len(p.errorSequence) > 0 {
		err := p.errorSequence[0]
		p.errorSequence = p.errorSequence[1:]
		p.mu.RUnlock()
		return model.Completion{}, err
	}
	p.mu.RUnlock()

	// Simulate latency
	if p.fixedLatency > 0 || p.latencyJitter > 0 {
		latency := p.fixedLatency
		if p.latencyJitter > 0 {
			// Simple pseudo-random jitter
			callNum := p.callCount.Load()
			jitter := time.Duration(callNum%int64(p.latencyJitter.Milliseconds())) * time.Millisecond
			latency += jitter
		}
		select {
		case <-ctx.Done():
			return model.Completion{}, ctx.Err()
		case <-time.After(latency):
		}
	}

	// Use custom response function if provided
	if p.responseFunc != nil {
		return p.responseFunc(req)
	}

	// Use fixed custom response if provided
	if p.customResponse != nil {
		resp := *p.customResponse
		resp.Model = req.Model
		resp.Response.Model = req.Model
		return resp, nil
	}

	// Default successful response
	return model.Completion{
		Response: model.ChatResponse{
			ID:      "mock-completion-" + time.Now().Format("20060102150405"),
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   req.Model,
			Choices: []model.Choice{
				{
					Index: 0,
					Message: model.Message{
						Role:    "assistant",
						Content: "This is a mock response from " + p.name,
					},
					FinishReason: "stop",
				},
			},
			Usage: model.Usage{
				PromptTokens:     10,
				CompletionTokens: 20,
				TotalTokens:      30,
			},
		},
		Provider:  p.name,
		Model:     req.Model,
		LatencyMs: p.fixedLatency.Milliseconds(),
	}, nil
}

// CallCount returns the number of Complete calls made
func (p *Provider) CallCount() int64 {
	return p.callCount.Load()
}

// Reset resets the provider state
func (p *Provider) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.errorSequence = nil
	p.configurableErr = nil
	p.failNext.Store(false)
	p.callCount.Store(0)
	p.customResponse = nil
	p.responseFunc = nil
}

// SetEnabled enables or disables the provider
func (p *Provider) SetEnabled(enabled bool) {
	p.enabled = enabled
}

// SetFixedLatency updates the fixed latency
func (p *Provider) SetFixedLatency(latency time.Duration) {
	p.fixedLatency = latency
}

// AddErrorToSequence adds an error to the error sequence
func (p *Provider) AddErrorToSequence(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.errorSequence = append(p.errorSequence, err)
}

// Predefined error constructors for common test scenarios
var (
	ErrMockTimeout       = errors.New("mock timeout")
	ErrMockRateLimited   = errors.New("mock rate limited")
	ErrMockAuthFailed    = errors.New("mock auth failed")
	ErrMockInvalidRequest = errors.New("mock invalid request")
	ErrMockUnavailable   = errors.New("mock unavailable")
)