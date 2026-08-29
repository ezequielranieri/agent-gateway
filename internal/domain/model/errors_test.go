package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSentinelErrors(t *testing.T) {
	// Ensure all sentinel errors are defined and non-nil
	require.NotNil(t, ErrProviderUnavailable)
	require.NotNil(t, ErrProviderTimeout)
	require.NotNil(t, ErrProviderRateLimited)
	require.NotNil(t, ErrProviderAuthFailed)
	require.NotNil(t, ErrProviderInvalidRequest)
	require.NotNil(t, ErrNoHealthyProvider)

	// Ensure they have meaningful messages
	assert.Equal(t, "provider unavailable", ErrProviderUnavailable.Error())
	assert.Equal(t, "provider timeout", ErrProviderTimeout.Error())
	assert.Equal(t, "provider rate limited", ErrProviderRateLimited.Error())
	assert.Equal(t, "provider authentication failed", ErrProviderAuthFailed.Error())
	assert.Equal(t, "provider invalid request", ErrProviderInvalidRequest.Error())
	assert.Equal(t, "no healthy provider available", ErrNoHealthyProvider.Error())
}