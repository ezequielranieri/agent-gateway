package external

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

var (
	ErrCircuitBreakerOpen = errors.New("circuit breaker open")
	ErrMaxRetriesExceeded = errors.New("max retries exceeded")
)

// CircuitBreakerState represents the state of a circuit breaker
type CircuitBreakerState int

const (
	CircuitBreakerClosed CircuitBreakerState = iota
	CircuitBreakerOpen
	CircuitBreakerHalfOpen
)

// CircuitBreakerConfig holds circuit breaker configuration
type CircuitBreakerConfig struct {
	FailureThreshold int           `koanf:"failure_threshold"`
	Window           time.Duration `koanf:"window"`
	ResetTimeout     time.Duration `koanf:"reset_timeout"`
}

// DefaultCircuitBreakerConfig returns default circuit breaker configuration
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		FailureThreshold: 5,
		Window:           30 * time.Second,
		ResetTimeout:     60 * time.Second,
	}
}

// CircuitBreaker implements a thread-safe circuit breaker
type CircuitBreaker struct {
	mu                sync.RWMutex
	state             CircuitBreakerState
	failureCount      int
	lastFailureTime   time.Time
	config            CircuitBreakerConfig
	successesInHalfOpen int
	logger            zerolog.Logger
}

// NewCircuitBreaker creates a new circuit breaker
func NewCircuitBreaker(config CircuitBreakerConfig, logger zerolog.Logger) *CircuitBreaker {
	return &CircuitBreaker{
		state:  CircuitBreakerClosed,
		config: config,
		logger: logger.With().Str("component", "circuit_breaker").Logger(),
	}
}

// Execute executes the given function with circuit breaker protection
func (cb *CircuitBreaker) Execute(ctx context.Context, fn func() error) error {
	if !cb.allowRequest() {
		cb.logger.Debug().Msg("Circuit breaker open, rejecting request")
		return ErrCircuitBreakerOpen
	}

	err := fn()
	cb.recordResult(err)
	return err
}

// allowRequest checks if a request should be allowed
func (cb *CircuitBreaker) allowRequest() bool {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	switch cb.state {
	case CircuitBreakerClosed:
		return true
	case CircuitBreakerOpen:
		// Check if we should transition to half-open
		if time.Since(cb.lastFailureTime) >= cb.config.ResetTimeout {
			cb.mu.RUnlock()
			cb.mu.Lock()
			if cb.state == CircuitBreakerOpen && time.Since(cb.lastFailureTime) >= cb.config.ResetTimeout {
				cb.state = CircuitBreakerHalfOpen
				cb.successesInHalfOpen = 0
				cb.logger.Info().Msg("Circuit breaker transitioning to half-open")
			}
			cb.mu.Unlock()
			cb.mu.RLock()
			return cb.state == CircuitBreakerHalfOpen
		}
		return false
	case CircuitBreakerHalfOpen:
		return true
	}
	return false
}

// recordResult records the result of a request
func (cb *CircuitBreaker) recordResult(err error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err != nil {
		cb.failureCount++
		cb.lastFailureTime = time.Now()

		if cb.state == CircuitBreakerHalfOpen {
			// Any failure in half-open goes back to open
			cb.state = CircuitBreakerOpen
			cb.logger.Warn().Msg("Circuit breaker reopened after half-open failure")
		} else if cb.failureCount >= cb.config.FailureThreshold {
			cb.state = CircuitBreakerOpen
			cb.logger.Warn().
				Int("failure_count", cb.failureCount).
				Int("threshold", cb.config.FailureThreshold).
				Msg("Circuit breaker opened")
		}
	} else {
		// Success
		if cb.state == CircuitBreakerHalfOpen {
			cb.successesInHalfOpen++
			if cb.successesInHalfOpen >= 2 {
				cb.state = CircuitBreakerClosed
				cb.failureCount = 0
				cb.logger.Info().Msg("Circuit breaker closed after successful recovery")
			}
		} else if cb.state == CircuitBreakerClosed {
			cb.failureCount = 0
		}
	}
}

// State returns the current state
func (cb *CircuitBreaker) State() CircuitBreakerState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// String returns string representation of state
func (s CircuitBreakerState) String() string {
	switch s {
	case CircuitBreakerClosed:
		return "closed"
	case CircuitBreakerOpen:
		return "open"
	case CircuitBreakerHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// RetryConfig holds retry configuration
type RetryConfig struct {
	MaxAttempts int           `koanf:"max_attempts"`
	Backoff     time.Duration `koanf:"backoff"`
}

// DefaultRetryConfig returns default retry configuration
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts: 2, // 1 retry + original
		Backoff:     500 * time.Millisecond,
	}
}

// HTTPClientConfig holds HTTP client configuration for external classifiers
type HTTPClientConfig struct {
	Timeout       time.Duration       `koanf:"timeout"`
	Retry         RetryConfig         `koanf:"retry"`
	CircuitBreaker CircuitBreakerConfig `koanf:"circuit_breaker"`
}

// DefaultHTTPClientConfig returns default HTTP client configuration
func DefaultHTTPClientConfig() HTTPClientConfig {
	return HTTPClientConfig{
		Timeout:       5 * time.Second,
		Retry:         DefaultRetryConfig(),
		CircuitBreaker: DefaultCircuitBreakerConfig(),
	}
}

// GuardrailHTTPClient wraps HTTP client with retry and circuit breaker
type GuardrailHTTPClient struct {
	client       *http.Client
	retryConfig  RetryConfig
	breaker      *CircuitBreaker
	logger       zerolog.Logger
}

// NewGuardrailHTTPClient creates a new HTTP client with retry and circuit breaker
func NewGuardrailHTTPClient(config HTTPClientConfig, logger zerolog.Logger) *GuardrailHTTPClient {
	return &GuardrailHTTPClient{
		client: &http.Client{
			Timeout: config.Timeout,
		},
		retryConfig: config.Retry,
		breaker:     NewCircuitBreaker(config.CircuitBreaker, logger),
		logger:      logger.With().Str("component", "guardrail_http_client").Logger(),
	}
}

// Do executes an HTTP request with retry and circuit breaker
func (c *GuardrailHTTPClient) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	for attempt := 0; attempt <= c.retryConfig.MaxAttempts; attempt++ {
		// Check circuit breaker
		if err := c.breaker.Execute(ctx, func() error { return nil }); err != nil {
			return nil, err
		}

		resp, err := c.client.Do(req.WithContext(ctx))
		if err == nil {
			// Check if we should retry on 5xx status codes
			if resp.StatusCode >= 500 && resp.StatusCode < 600 && attempt < c.retryConfig.MaxAttempts {
				resp.Body.Close()
				c.logger.Debug().
					Int("status", resp.StatusCode).
					Int("attempt", attempt+1).
					Int("max_attempts", c.retryConfig.MaxAttempts+1).
					Msg("HTTP request returned 5xx, retrying")
				// Continue to next attempt
			} else {
				// Success or non-retryable status
				return resp, nil
			}
		}

		c.logger.Debug().
			Err(err).
			Int("attempt", attempt+1).
			Int("max_attempts", c.retryConfig.MaxAttempts+1).
			Msg("HTTP request failed")

		// Check if we should retry
		if attempt < c.retryConfig.MaxAttempts {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.retryConfig.Backoff):
				// Continue to next attempt
			}
		}
	}

	return nil, fmt.Errorf("max retries exceeded")
}

// Close closes the HTTP client
func (c *GuardrailHTTPClient) Close() error {
	return nil
}