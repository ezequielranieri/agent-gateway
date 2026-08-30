package guardrail

import "errors"

// Sentinel errors for external classifier
var (
	// ErrExternalClassifierUnavailable is returned when external classifier is not available
	ErrExternalClassifierUnavailable = errors.New("external classifier unavailable")

	// ErrClassifierTimeout is returned when classifier request times out
	ErrClassifierTimeout = errors.New("classifier request timeout")

	// ErrClassifierConfig is returned when classifier configuration is invalid
	ErrClassifierConfig = errors.New("classifier configuration invalid")

	// ErrCompositeNoClassifiers is returned when composite has no classifiers configured
	ErrCompositeNoClassifiers = errors.New("no classifiers configured in composite")

	// ErrCircuitBreakerOpen is returned when circuit breaker is open
	ErrCircuitBreakerOpen = errors.New("circuit breaker open")
)