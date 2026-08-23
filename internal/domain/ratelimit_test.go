package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRateLimitKey_String(t *testing.T) {
	id := NewUUID()

	tests := []struct {
		name     string
		key      RateLimitKey
		expected string
	}{
		{
			name: "requests tenant",
			key: RateLimitKey{
				BucketType: BucketTypeRequests,
				Scope:      RateLimitScopeTenant,
				ID:         id,
			},
			expected: "rl:requests:tenant:" + id.String(),
		},
		{
			name: "tokens user",
			key: RateLimitKey{
				BucketType: BucketTypeTokens,
				Scope:      RateLimitScopeUser,
				ID:         id,
			},
			expected: "rl:tokens:user:" + id.String(),
		},
		{
			name: "tool_execs role",
			key: RateLimitKey{
				BucketType: BucketTypeToolExecs,
				Scope:      RateLimitScopeRole,
				ID:         id,
			},
			expected: "rl:tool_execs:role:" + id.String(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.key.String())
		})
	}
}

func TestRateLimitCost_Defaults(t *testing.T) {
	cost := RateLimitCost{
		Requests:  1,
		Tokens:    1000,
		ToolExecs: 0,
	}

	assert.Equal(t, 1, cost.Requests)
	assert.Equal(t, 1000, cost.Tokens)
	assert.Equal(t, 0, cost.ToolExecs)
}

func TestBucketType_Constants(t *testing.T) {
	assert.Equal(t, "requests", string(BucketTypeRequests))
	assert.Equal(t, "tokens", string(BucketTypeTokens))
	assert.Equal(t, "tool_execs", string(BucketTypeToolExecs))
}

func TestRateLimitScope_Constants(t *testing.T) {
	assert.Equal(t, "tenant", string(RateLimitScopeTenant))
	assert.Equal(t, "user", string(RateLimitScopeUser))
	assert.Equal(t, "role", string(RateLimitScopeRole))
}