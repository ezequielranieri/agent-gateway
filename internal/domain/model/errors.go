package model

import "errors"

// Sentinel errors for the model provider domain
var (
	// ErrProviderUnavailable indicates the model provider is not available
	ErrProviderUnavailable = errors.New("provider unavailable")

	// ErrProviderTimeout indicates the provider request timed out
	ErrProviderTimeout = errors.New("provider timeout")

	// ErrProviderRateLimited indicates the provider rate limited the request
	ErrProviderRateLimited = errors.New("provider rate limited")

	// ErrProviderAuthFailed indicates authentication with the provider failed
	ErrProviderAuthFailed = errors.New("provider authentication failed")

	// ErrProviderInvalidRequest indicates the request was invalid for the provider
	ErrProviderInvalidRequest = errors.New("provider invalid request")

	// ErrNoHealthyProvider indicates no healthy provider is available in the fallback chain
	ErrNoHealthyProvider = errors.New("no healthy provider available")
)