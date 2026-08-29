package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCircuitBreaker_InitialState(t *testing.T) {
	cb := NewCircuitBreaker(DefaultCircuitBreakerConfig())
	assert.Equal(t, StateClosed, cb.State())
	assert.Nil(t, cb.AllowRequest())
}

func TestCircuitBreaker_RecordFailure_ClosesAfterThreshold(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 3
	cb := NewCircuitBreaker(config)

	// Record failures up to threshold
	for i := 0; i < 2; i++ {
		assert.Nil(t, cb.AllowRequest())
		cb.RecordFailure()
		assert.Equal(t, StateClosed, cb.State())
	}

	// Third failure opens the circuit
	assert.Nil(t, cb.AllowRequest())
	cb.RecordFailure()
	assert.Equal(t, StateOpen, cb.State())
}

func TestCircuitBreaker_OpenState_BlocksRequests(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 1
	cb := NewCircuitBreaker(config)

	// Open the circuit
	cb.RecordFailure()
	assert.Equal(t, StateOpen, cb.State())

	// Requests should be blocked
	err := cb.AllowRequest()
	assert.Error(t, err)
	assert.Equal(t, ErrProviderUnavailable, err)
}

func TestCircuitBreaker_HalfOpen_AfterTimeout(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 1
	config.Timeout = 10 * time.Millisecond
	cb := NewCircuitBreaker(config)

	// Open the circuit
	cb.RecordFailure()
	assert.Equal(t, StateOpen, cb.State())

	// Wait for timeout
	time.Sleep(20 * time.Millisecond)

	// Try transition to half-open
	transitioned := cb.TryTransitionToHalfOpen()
	assert.True(t, transitioned)
	assert.Equal(t, StateHalfOpen, cb.State())
}

func TestCircuitBreaker_HalfOpen_AllowsLimitedRequests(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 1
	config.Timeout = 10 * time.Millisecond
	config.HalfOpenMaxRequests = 2
	cb := NewCircuitBreaker(config)

	// Open the circuit
	cb.RecordFailure()
	time.Sleep(20 * time.Millisecond)
	cb.TryTransitionToHalfOpen()
	assert.Equal(t, StateHalfOpen, cb.State())

	// First request allowed
	assert.Nil(t, cb.AllowRequest())

	// Second request allowed
	assert.Nil(t, cb.AllowRequest())

	// Third request blocked
	err := cb.AllowRequest()
	assert.Error(t, err)
	assert.Equal(t, ErrProviderUnavailable, err)
}

func TestCircuitBreaker_HalfOpen_SuccessClosesCircuit(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 1
	config.Timeout = 10 * time.Millisecond
	config.SuccessThreshold = 2
	cb := NewCircuitBreaker(config)

	// Open and transition to half-open
	cb.RecordFailure()
	time.Sleep(20 * time.Millisecond)
	cb.TryTransitionToHalfOpen()
	assert.Equal(t, StateHalfOpen, cb.State())

	// First success
	cb.RecordSuccess()
	assert.Equal(t, StateHalfOpen, cb.State())

	// Second success closes circuit
	cb.RecordSuccess()
	assert.Equal(t, StateClosed, cb.State())
}

func TestCircuitBreaker_HalfOpen_FailureReopensCircuit(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 1
	config.Timeout = 10 * time.Millisecond
	cb := NewCircuitBreaker(config)

	// Open and transition to half-open
	cb.RecordFailure()
	time.Sleep(20 * time.Millisecond)
	cb.TryTransitionToHalfOpen()
	assert.Equal(t, StateHalfOpen, cb.State())

	// Failure in half-open reopens circuit
	cb.RecordFailure()
	assert.Equal(t, StateOpen, cb.State())
}

func TestCircuitBreaker_Reset(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 1
	cb := NewCircuitBreaker(config)

	// Open the circuit
	cb.RecordFailure()
	assert.Equal(t, StateOpen, cb.State())

	// Reset
	cb.Reset()
	assert.Equal(t, StateClosed, cb.State())
	assert.Nil(t, cb.AllowRequest())
}

func TestCircuitBreaker_Stats(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 3
	cb := NewCircuitBreaker(config)

	stats := cb.Stats()
	assert.Equal(t, StateClosed, stats.State)
	assert.Equal(t, 0, stats.FailureCount)
	assert.Equal(t, 0, stats.SuccessCount)

	cb.RecordFailure()
	cb.RecordFailure()

	stats = cb.Stats()
	assert.Equal(t, StateClosed, stats.State)
	assert.Equal(t, 2, stats.FailureCount)
}

func TestCircuitBreaker_StateString(t *testing.T) {
	assert.Equal(t, "closed", StateClosed.String())
	assert.Equal(t, "open", StateOpen.String())
	assert.Equal(t, "half-open", StateHalfOpen.String())
}

func TestCircuitBreaker_ConcurrentAccess(t *testing.T) {
	// This test verifies the circuit breaker doesn't panic under concurrent access
	// Note: The implementation uses RWMutex for basic safety but is documented as
	// not fully thread-safe for state reads during transitions (per-process limitation)
	cb := NewCircuitBreaker(DefaultCircuitBreakerConfig())

	done := make(chan bool, 100)
	for i := 0; i < 50; i++ {
		go func() {
			_ = cb.AllowRequest()
			cb.RecordSuccess()
			done <- true
		}()
		go func() {
			_ = cb.AllowRequest()
			cb.RecordFailure()
			done <- true
		}()
	}

	for i := 0; i < 100; i++ {
		<-done
	}

	// Should not panic, state should be valid
	stats := cb.Stats()
	assert.Contains(t, []CircuitBreakerState{StateClosed, StateOpen, StateHalfOpen}, stats.State)
}