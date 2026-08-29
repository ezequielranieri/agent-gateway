package chat

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
	"github.com/rs/zerolog"
)

// FallbackChain manages fallback logic with retries and circuit breaker integration
type FallbackChain struct {
	router         *Router
	pricing        model.PricingService
	maxRetries     int
	defaultTimeout time.Duration
	fallbackRules  []model.FallbackRule
	logger         zerolog.Logger
}

// FallbackResult holds the result of a fallback attempt
type FallbackResult struct {
	Completion  model.Completion
	Provider    string
	Model       string
	Attempt     int
	TotalLatency time.Duration
	Retried     bool
	FallbackReason string
}

// NewFallbackChain creates a new fallback chain
func NewFallbackChain(
	router *Router,
	pricing model.PricingService,
	cfg model.RouterConfig,
	logger zerolog.Logger,
) *FallbackChain {
	maxRetries := cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 2
	}
	
	defaultTimeout := cfg.DefaultTimeout
	if defaultTimeout <= 0 {
		defaultTimeout = 30 * time.Second
	}
	
	return &FallbackChain{
		router:         router,
		pricing:        pricing,
		maxRetries:     maxRetries,
		defaultTimeout: defaultTimeout,
		fallbackRules:  cfg.FallbackRules,
		logger:         logger.With().Str("component", "fallback_chain").Logger(),
	}
}

// ExecuteWithFallback executes a request with fallback logic
func (fc *FallbackChain) ExecuteWithFallback(
	ctx context.Context,
	req model.ChatRequest,
) (FallbackResult, error) {
	startTime := time.Now()
	
	// Get pre-estimate for cost tracking
	_, _, err := fc.estimateCost(ctx, req)
	if err != nil {
		fc.logger.Warn().
			Err(err).
			Msg("Failed to get pre-estimate cost")
	}
	
	var lastErr error
	attempt := 0
	
	// Get the list of providers to try (in priority order)
	providers := fc.router.GetRegistry().GetHealthyProviders(ctx)
	if len(providers) == 0 {
		return FallbackResult{}, model.ErrNoHealthyProvider
	}
	
	// Filter providers that support the requested model
	var candidateProviders []*RegisteredProvider
	for _, p := range providers {
		if p.supportsModel(req.Model) {
			candidateProviders = append(candidateProviders, p)
		}
	}
	
	if len(candidateProviders) == 0 {
		return FallbackResult{}, model.ErrNoHealthyProvider
	}
	
	// Try each provider with retries
	for _, provider := range candidateProviders {
		maxProviderRetries := fc.getMaxRetriesForProvider(provider)
		
		for retry := 0; retry <= maxProviderRetries; retry++ {
			attempt++
			
			// Check circuit breaker
			if err := provider.CircuitBreaker.AllowRequest(); err != nil {
				fc.logger.Debug().
					Str("provider", provider.Config.Name).
					Str("state", provider.CircuitBreaker.State().String()).
					Msg("Circuit breaker open, skipping provider")
				lastErr = model.ErrProviderUnavailable
				break // Move to next provider
			}
			
			// Create context with timeout for this attempt
			attemptCtx, cancel := context.WithTimeout(ctx, provider.Config.Timeout)
			
			// Execute request
			completion, err := provider.Provider.Complete(attemptCtx, req)
			cancel()
			
			latency := time.Since(startTime)
			
			if err == nil {
				// Success!
				provider.CircuitBreaker.RecordSuccess()
				
				// Record actual cost
				actualCost, actualVersion, _ := fc.estimateCost(ctx, req)
				_ = actualCost // Cost tracking for analytics
				_ = actualVersion
				
				result := FallbackResult{
					Completion:     completion,
					Provider:       provider.Config.Name,
					Model:          completion.Model,
					Attempt:        attempt,
					TotalLatency:   latency,
					Retried:        attempt > 1,
					FallbackReason: "",
				}
				
				fc.logger.Info().
					Str("provider", provider.Config.Name).
					Str("model", completion.Model).
					Int("attempt", attempt).
					Dur("latency", latency).
					Msg("Request completed successfully")
				
				return result, nil
			}
			
			// Request failed, record failure
			provider.CircuitBreaker.RecordFailure()
			lastErr = err
			
			fc.logger.Warn().
				Str("provider", provider.Config.Name).
				Int("attempt", attempt).
				Int("retry", retry).
				Err(err).
				Msg("Provider request failed")
			
			// Check if we should retry this provider or fall back
			shouldRetry, backoff := fc.shouldRetry(err, retry, provider)
			if !shouldRetry {
				fc.logger.Debug().
					Str("provider", provider.Config.Name).
					Err(err).
					Msg("Not retrying provider, falling back to next")
				break // Move to next provider
			}
			
			// Wait for backoff before retry
			if backoff > 0 {
				select {
				case <-ctx.Done():
					return FallbackResult{}, ctx.Err()
				case <-time.After(backoff):
				}
			}
		}
	}
	
	// All providers exhausted
	totalLatency := time.Since(startTime)
	fc.logger.Error().
		Int("attempts", attempt).
		Dur("total_latency", totalLatency).
		Err(lastErr).
		Msg("All providers exhausted")
	
	return FallbackResult{
		Attempt:     attempt,
		TotalLatency: totalLatency,
		Retried:     attempt > 1,
	}, fmt.Errorf("all providers exhausted: %w", lastErr)
}

// getMaxRetriesForProvider returns the max retries for a provider
func (fc *FallbackChain) getMaxRetriesForProvider(provider *RegisteredProvider) int {
	if provider.Config.MaxRetries > 0 {
		return provider.Config.MaxRetries
	}
	return fc.maxRetries
}

// shouldRetry determines if a request should be retried based on the error
func (fc *FallbackChain) shouldRetry(err error, retry int, provider *RegisteredProvider) (bool, time.Duration) {
	if retry >= fc.getMaxRetriesForProvider(provider) {
		return false, 0
	}
	
	// Check fallback rules
	for _, rule := range fc.fallbackRules {
		for _, ruleErr := range rule.OnErrors {
			if errors.Is(err, modelErrFromString(ruleErr)) {
				maxRetries := rule.MaxRetries
				if maxRetries == 0 {
					maxRetries = fc.maxRetries
				}
				if retry < maxRetries {
					backoff := rule.Backoff
					if backoff == 0 {
						backoff = time.Duration(retry+1) * 100 * time.Millisecond
					}
					return true, backoff
				}
				return false, 0
			}
		}
	}
	
	// Default: retry on transient errors
	if isTransientError(err) {
		backoff := time.Duration(retry+1) * 100 * time.Millisecond
		return true, backoff
	}
	
	return false, 0
}

// estimateCost estimates the cost for a request
func (fc *FallbackChain) estimateCost(ctx context.Context, req model.ChatRequest) (float64, string, error) {
	// Rough token estimation
	promptTokens := estimatePromptTokens(req.Messages)
	completionTokens := 1000
	if req.MaxTokens != nil {
		completionTokens = *req.MaxTokens
	}
	
	cost, version, err := fc.pricing.GetCost(ctx, req.Model, promptTokens, completionTokens)
	return cost, version, err
}

// modelErrFromString converts a string to a model sentinel error
func modelErrFromString(s string) error {
	switch s {
	case "provider unavailable":
		return model.ErrProviderUnavailable
	case "provider timeout":
		return model.ErrProviderTimeout
	case "provider rate limited":
		return model.ErrProviderRateLimited
	case "provider authentication failed":
		return model.ErrProviderAuthFailed
	case "provider invalid request":
		return model.ErrProviderInvalidRequest
	default:
		return errors.New(s)
	}
}

// isTransientError checks if an error is transient and worth retrying
func isTransientError(err error) bool {
	return errors.Is(err, model.ErrProviderUnavailable) ||
		errors.Is(err, model.ErrProviderTimeout) ||
		errors.Is(err, model.ErrProviderRateLimited)
}

// GetFallbackChainStats returns statistics about the fallback chain
func (fc *FallbackChain) GetFallbackChainStats() FallbackChainStats {
	return FallbackChainStats{
		MaxRetries:     fc.maxRetries,
		DefaultTimeout: fc.defaultTimeout,
		FallbackRules:  fc.fallbackRules,
	}
}

// FallbackChainStats holds statistics for the fallback chain
type FallbackChainStats struct {
	MaxRetries     int
	DefaultTimeout time.Duration
	FallbackRules  []model.FallbackRule
}