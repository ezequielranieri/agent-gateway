package model

import (
	"errors"
	"sync"
	"time"
)

// CircuitBreakerState represents the state of the circuit breaker.
type CircuitBreakerState int

const (
	// StateClosed: normal operation, requests pass through.
	StateClosed CircuitBreakerState = iota

	// StateOpen: circuit is open, requests fail fast.
	StateOpen

	// StateHalfOpen: testing if service recovered, limited requests allowed.
	StateHalfOpen
)

// String returns the string representation of the state.
func (s CircuitBreakerState) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreaker implements a half-open state machine for fault tolerance.
// It is NOT thread-safe for concurrent state reads during transitions;
// callers must synchronize if needed. For per-process use, this is acceptable.
// See DECISIONS.md for per-process limitation documentation.
type CircuitBreaker struct {
	mu sync.RWMutex

	// Configuration
	failureThreshold   int
	successThreshold   int
	timeout            time.Duration
	halfOpenMaxRequests int
	enabled            bool

	// State
	state              CircuitBreakerState
	failureCount       int
	successCount       int
	lastFailureTime    time.Time
	halfOpenRequests   int
}

// CircuitBreakerConfig holds the configuration for a circuit breaker.
type CircuitBreakerConfig struct {
	Enabled              bool          // enables circuit breaker
	FailureThreshold     int           // failures before opening
	SuccessThreshold     int           // successes in half-open before closing
	Timeout              time.Duration // time in open before half-open
	HalfOpenMaxRequests  int           // max concurrent requests in half-open
}

// NewCircuitBreaker creates a new circuit breaker with the given config.
func NewCircuitBreaker(config CircuitBreakerConfig) *CircuitBreaker {
	if config.FailureThreshold <= 0 {
		config.FailureThreshold = 5
	}
	if config.SuccessThreshold <= 0 {
		config.SuccessThreshold = 2
	}
	if config.Timeout <= 0 {
		config.Timeout = 30 * time.Second
	}
	if config.HalfOpenMaxRequests <= 0 {
		config.HalfOpenMaxRequests = 3
	}

	return &CircuitBreaker{
		failureThreshold:    config.FailureThreshold,
		successThreshold:    config.SuccessThreshold,
		timeout:             config.Timeout,
		halfOpenMaxRequests: config.HalfOpenMaxRequests,
		enabled:             config.Enabled,
		state:               StateClosed,
	}
}

// State returns the current circuit breaker state.
func (cb *CircuitBreaker) State() CircuitBreakerState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	// Check if we should transition from open to half-open
	if cb.state == StateOpen {
		if time.Since(cb.lastFailureTime) >= cb.timeout {
			// Note: we don't transition here to avoid lock upgrade;
			// the caller should call TryTransitionToHalfOpen()
		}
	}
	return cb.state
}

// TryTransitionToHalfOpen attempts to transition from open to half-open.
// Returns true if transition occurred.
func (cb *CircuitBreaker) TryTransitionToHalfOpen() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == StateOpen && time.Since(cb.lastFailureTime) >= cb.timeout {
		cb.state = StateHalfOpen
		cb.successCount = 0
		cb.halfOpenRequests = 0
		return true
	}
	return false
}

// AllowRequest checks if a request should be allowed through.
// Returns an error if the circuit is open and not allowing requests.
func (cb *CircuitBreaker) AllowRequest() error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	// If disabled, always allow
	if !cb.enabled {
		return nil
	}

	switch cb.state {
	case StateClosed:
		return nil
	case StateOpen:
		// Check if timeout has elapsed to transition to half-open
		if time.Since(cb.lastFailureTime) >= cb.timeout {
			cb.state = StateHalfOpen
			cb.successCount = 0
			cb.halfOpenRequests = 0
			// Fall through to half-open logic
		} else {
			return ErrProviderUnavailable
		}
	case StateHalfOpen:
		if cb.halfOpenRequests >= cb.halfOpenMaxRequests {
			return ErrProviderUnavailable
		}
		cb.halfOpenRequests++
		return nil
	}
	return nil
}

// RecordSuccess records a successful request.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if !cb.enabled {
		return
	}

	switch cb.state {
	case StateClosed:
		cb.failureCount = 0 // Reset failure count on success
	case StateHalfOpen:
		cb.successCount++
		if cb.successCount >= cb.successThreshold {
			cb.state = StateClosed
			cb.failureCount = 0
			cb.successCount = 0
		}
	}
}

// RecordFailure records a failed request.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if !cb.enabled {
		return
	}

	cb.lastFailureTime = time.Now()

	switch cb.state {
	case StateClosed:
		cb.failureCount++
		if cb.failureCount >= cb.failureThreshold {
			cb.state = StateOpen
		}
	case StateHalfOpen:
		// Any failure in half-open goes back to open
		cb.state = StateOpen
		cb.successCount = 0
	case StateOpen:
		// Already open, just update last failure time
	}
}

// Reset manually resets the circuit breaker to closed state.
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.state = StateClosed
	cb.failureCount = 0
	cb.successCount = 0
	cb.halfOpenRequests = 0
}

// Stats returns current circuit breaker statistics.
type CircuitBreakerStats struct {
	State             CircuitBreakerState
	FailureCount      int
	SuccessCount      int
	HalfOpenRequests  int
	LastFailureTime   time.Time
}

// Stats returns a snapshot of the circuit breaker state.
func (cb *CircuitBreaker) Stats() CircuitBreakerStats {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	return CircuitBreakerStats{
		State:            cb.state,
		FailureCount:     cb.failureCount,
		SuccessCount:     cb.successCount,
		HalfOpenRequests: cb.halfOpenRequests,
		LastFailureTime:  cb.lastFailureTime,
	}
}

// DefaultCircuitBreakerConfig returns sensible defaults for a circuit breaker.
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		Enabled:              true,
		FailureThreshold:     5,
		SuccessThreshold:     2,
		Timeout:              30 * time.Second,
		HalfOpenMaxRequests:  3,
	}
}

// ErrCircuitOpen is returned when the circuit breaker is open.
var ErrCircuitOpen = errors.New("circuit breaker open")