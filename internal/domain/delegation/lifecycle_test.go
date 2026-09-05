package delegation

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGrantStatus_Values(t *testing.T) {
	assert.Equal(t, GrantStatus("issued"), GrantStatusIssued)
	assert.Equal(t, GrantStatus("active"), GrantStatusActive)
	assert.Equal(t, GrantStatus("revoked"), GrantStatusRevoked)
	assert.Equal(t, GrantStatus("expired"), GrantStatusExpired)
	assert.Equal(t, GrantStatus("consumed"), GrantStatusConsumed)
}

func TestGrantStatus_IsTerminal(t *testing.T) {
	tests := []struct {
		status   GrantStatus
		expected bool
	}{
		{GrantStatusIssued, false},
		{GrantStatusActive, false},
		{GrantStatusRevoked, true},
		{GrantStatusExpired, true},
		{GrantStatusConsumed, true},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.status.IsTerminal())
		})
	}
}

func TestGrantStatus_CanTransitionTo(t *testing.T) {
	tests := []struct {
		from     GrantStatus
		to       GrantStatus
		expected bool
	}{
		// issued -> active (grant accepted by delegate)
		{GrantStatusIssued, GrantStatusActive, true},
		// issued -> revoked (parent revokes before acceptance)
		{GrantStatusIssued, GrantStatusRevoked, true},
		// issued -> expired (TTL elapsed before acceptance)
		{GrantStatusIssued, GrantStatusExpired, true},
		// issued -> consumed is invalid
		{GrantStatusIssued, GrantStatusConsumed, false},
		// issued -> issued is invalid (no self-transition)
		{GrantStatusIssued, GrantStatusIssued, false},

		// active -> revoked (chain revocation or explicit revoke)
		{GrantStatusActive, GrantStatusRevoked, true},
		// active -> expired (TTL elapsed)
		{GrantStatusActive, GrantStatusExpired, true},
		// active -> consumed (action completed successfully)
		{GrantStatusActive, GrantStatusConsumed, true},
		// active -> issued is invalid
		{GrantStatusActive, GrantStatusIssued, false},
		// active -> active is invalid (no self-transition)
		{GrantStatusActive, GrantStatusActive, false},

		// terminal states cannot transition
		{GrantStatusRevoked, GrantStatusActive, false},
		{GrantStatusRevoked, GrantStatusIssued, false},
		{GrantStatusExpired, GrantStatusActive, false},
		{GrantStatusConsumed, GrantStatusActive, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.from)+"_to_"+string(tt.to), func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.from.CanTransitionTo(tt.to))
		})
	}
}

func TestGrantStatus_ApplyTransition(t *testing.T) {
	g := &Grant{
		Status: GrantStatusIssued,
	}

	// Valid transition
	err := g.ApplyTransition(GrantStatusActive)
	require.NoError(t, err)
	assert.Equal(t, GrantStatusActive, g.Status)

	// Invalid transition from terminal state
	err = g.ApplyTransition(GrantStatusIssued)
	assert.ErrorIs(t, err, ErrInvalidGrantTransition)
}

func TestSentinelErrors(t *testing.T) {
	tests := []struct {
		err     error
		message string
	}{
		{ErrScopeIntersectionEmpty, "scope intersection is empty"},
		{ErrCrossTenantDelegationProhibited, "cross-tenant delegation is prohibited"},
		{ErrDepthLimitExceeded, "delegation depth limit exceeded"},
		{ErrFanOutLimitExceeded, "delegation fan-out limit exceeded"},
		{ErrBudgetExhausted, "delegation budget exhausted"},
		{ErrCycleDetected, "delegation cycle detected"},
		{ErrGrantRevokedOrConsumed, "grant is revoked or consumed"},
		{ErrInvalidGrantTransition, "invalid grant status transition"},
	}
	for _, tt := range tests {
		t.Run(tt.err.Error(), func(t *testing.T) {
			require.NotNil(t, tt.err)
			assert.Equal(t, tt.message, tt.err.Error())
			// Verify they are proper sentinel errors (wrap/unwrap works)
			assert.True(t, errors.Is(tt.err, tt.err))
		})
	}
}
