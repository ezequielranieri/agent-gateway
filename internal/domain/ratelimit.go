package domain

import (
	"context"
	"time"
)

// BucketType represents the type of rate limit bucket
type BucketType string

const (
	BucketTypeRequests BucketType = "requests"
	BucketTypeTokens   BucketType = "tokens"
	BucketTypeToolExecs BucketType = "tool_execs"
)

// RateLimitScope represents the scope of rate limiting
type RateLimitScope string

const (
	RateLimitScopeTenant RateLimitScope = "tenant"
	RateLimitScopeUser   RateLimitScope = "user"
	RateLimitScopeRole   RateLimitScope = "role"
)

// RateLimitKey identifies a rate limit bucket
type RateLimitKey struct {
	BucketType BucketType
	Scope      RateLimitScope
	ID         UUID
}

// String returns the Redis key format: rl:{bucket}:{scope}:{id}
func (k RateLimitKey) String() string {
	return "rl:" + string(k.BucketType) + ":" + string(k.Scope) + ":" + k.ID.String()
}

// RateLimitCost represents the cost of a request in different dimensions
type RateLimitCost struct {
	Requests  int // always 1 per request
	Tokens    int // estimated tokens
	ToolExecs int // tool executions (1 if tools used, 0 otherwise)
}

// RateLimitResult represents the result of a rate limit check
type RateLimitResult struct {
	Allowed    bool
	Remaining  int
	ResetAt    time.Time
	RetryAfter time.Duration
}

// RateLimiter is the interface for rate limiting
type RateLimiter interface {
	// Allow checks if a request is allowed for the given key, cost, limit, and window
	Allow(ctx context.Context, key RateLimitKey, cost RateLimitCost, limit int, window time.Duration) (RateLimitResult, error)
}

// QuotaResolver resolves effective rate limits for a request
// Highest-limit-wins: queries quota for tenant/user/role, takes max limit
type QuotaResolver interface {
	// GetEffectiveLimit returns the effective limit and window for a bucket type
	// by checking tenant, user, and role quotas and taking the maximum
	GetEffectiveLimit(ctx context.Context, tenantID, userID, roleID UUID, bucketType BucketType) (limit int, window time.Duration, err error)
}