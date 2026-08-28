package redis

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/postgres"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// GCRA Lua script for atomic token bucket (Generic Cell Rate Algorithm)
// KEYS[1] = rate limit key
// ARGV[1] = limit (max tokens)
// ARGV[2] = window in seconds
// ARGV[3] = cost (tokens to consume)
// ARGV[4] = current time in milliseconds
// Returns: {allowed (0/1), remaining, retry_after_ms, reset_at_ms}
const gcraLuaScript = `
local key = KEYS[1]
local limit = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local cost = tonumber(ARGV[3])
local now = tonumber(ARGV[4])

local ttl = window * 1000
local emission_interval = ttl / limit
local tat_key = key .. ":tat"

local tat = redis.call('GET', tat_key)
if tat == false then
	tat = now
else
	tat = tonumber(tat)
end

local new_tat = math.max(tat, now) + emission_interval * cost
local allowed = 0
local remaining = 0
local retry_after = 0

if new_tat - now <= ttl + emission_interval then
	allowed = 1
	redis.call('SET', tat_key, new_tat, 'PX', ttl + emission_interval)
	remaining = math.floor((ttl - (new_tat - now)) / emission_interval)
	if remaining < 0 then remaining = 0 end
	if remaining > limit then remaining = limit end
else
	retry_after = new_tat - now - ttl
end

local reset_at = now + ttl
return {allowed, remaining, retry_after, reset_at}
`

// RedisRateLimiter implements domain.RateLimiter using Redis GCRA
type RedisRateLimiter struct {
	client  *redis.Client
	script  *redis.Script
	logger  zerolog.Logger
	failOpen bool
}

// NewRedisRateLimiter creates a new Redis rate limiter
func NewRedisRateLimiter(client *redis.Client, logger zerolog.Logger, failOpen bool) *RedisRateLimiter {
	return &RedisRateLimiter{
		client:   client,
		script:   redis.NewScript(gcraLuaScript),
		logger:   logger.With().Str("component", "redis_ratelimiter").Logger(),
		failOpen: failOpen,
	}
}

// Allow implements domain.RateLimiter.Allow
func (r *RedisRateLimiter) Allow(ctx context.Context, key domain.RateLimitKey, cost domain.RateLimitCost, limit int, window time.Duration) (domain.RateLimitResult, error) {
	// Determine cost for this bucket type
	var n int
	switch key.BucketType {
	case domain.BucketTypeRequests:
		n = cost.Requests
	case domain.BucketTypeTokens:
		n = cost.Tokens
	case domain.BucketTypeToolExecs:
		n = cost.ToolExecs
	default:
		n = 1
	}

	if n <= 0 {
		n = 1
	}

	return r.allowWithCost(ctx, key, n, limit, window)
}

// allowWithCost is the internal implementation
func (r *RedisRateLimiter) allowWithCost(ctx context.Context, key domain.RateLimitKey, cost int, limit int, window time.Duration) (domain.RateLimitResult, error) {
	if limit <= 0 {
		// No limit configured, allow
		return domain.RateLimitResult{
			Allowed:   true,
			Remaining: math.MaxInt32,
			ResetAt:   time.Now().Add(window),
		}, nil
	}

	keyStr := key.String()
	now := time.Now().UnixMilli()
	windowSec := int(window.Seconds())

	result, err := r.script.Run(ctx, r.client, []string{keyStr}, limit, windowSec, cost, now).Slice()
	if err != nil {
		r.logger.Error().Err(err).Str("key", keyStr).Msg("Rate limiter script error")
		if r.failOpen {
			return domain.RateLimitResult{
				Allowed:   true,
				Remaining: limit,
				ResetAt:   time.Now().Add(window),
			}, nil
		}
		return domain.RateLimitResult{}, err
	}

	if len(result) < 4 {
		return domain.RateLimitResult{}, fmt.Errorf("unexpected script result length: %d", len(result))
	}

	allowed := result[0].(int64) == 1
	remaining := int(result[1].(int64))
	retryAfterMs := result[2].(int64)
	resetAtMs := result[3].(int64)

	return domain.RateLimitResult{
		Allowed:    allowed,
		Remaining:  remaining,
		ResetAt:    time.UnixMilli(resetAtMs),
		RetryAfter: time.Duration(retryAfterMs) * time.Millisecond,
	}, nil
}

// AllowN checks rate limit with explicit cost N
func (r *RedisRateLimiter) AllowN(ctx context.Context, key domain.RateLimitKey, n int, limit int, window time.Duration) (domain.RateLimitResult, error) {
	return r.allowWithCost(ctx, key, n, limit, window)
}

// RedisQuotaResolver implements domain.QuotaResolver using QuotaRepository
type RedisQuotaResolver struct {
	quotaRepo *postgres.QuotaRepository
	logger    zerolog.Logger
}

// NewRedisQuotaResolver creates a new quota resolver
func NewRedisQuotaResolver(quotaRepo *postgres.QuotaRepository, logger zerolog.Logger) *RedisQuotaResolver {
	return &RedisQuotaResolver{
		quotaRepo: quotaRepo,
		logger:    logger.With().Str("component", "redis_quota_resolver").Logger(),
	}
}

// GetEffectiveLimit implements domain.QuotaResolver.GetEffectiveLimit
// Highest-limit-wins: queries quota for tenant/user/role, takes max limit
func (r *RedisQuotaResolver) GetEffectiveLimit(ctx context.Context, tenantID, userID, roleID domain.UUID, bucketType domain.BucketType) (limit int, window time.Duration, err error) {
	var maxLimit int
	window = time.Minute // Fixed 1-minute window per spec

	// Check tenant quota
	tenantQuota, err := r.quotaRepo.GetByScope(ctx, tenantID, domain.QuotaScopeTenant, domain.UUID{})
	if err == nil && tenantQuota != nil {
		limit := r.getBucketLimit(tenantQuota, bucketType)
		if limit > maxLimit {
			maxLimit = limit
		}
	} else if err != nil && !errors.Is(err, domain.ErrNotFound) && !errors.Is(err, sql.ErrNoRows) {
		r.logger.Error().Err(err).Str("tenant_id", tenantID.String()).Msg("Failed to get tenant quota")
	}

	// Check user quota (if userID provided)
	if !userID.IsZero() {
		userQuota, err := r.quotaRepo.GetByScope(ctx, tenantID, domain.QuotaScopeUser, userID)
		if err == nil && userQuota != nil {
			limit := r.getBucketLimit(userQuota, bucketType)
			if limit > maxLimit {
				maxLimit = limit
			}
		} else if err != nil && !errors.Is(err, domain.ErrNotFound) && !errors.Is(err, sql.ErrNoRows) {
			r.logger.Error().Err(err).Str("user_id", userID.String()).Msg("Failed to get user quota")
		}
	}

	// Check role quota (if roleID provided)
	if !roleID.IsZero() {
		roleQuota, err := r.quotaRepo.GetByScope(ctx, tenantID, domain.QuotaScopeRole, roleID)
		if err == nil && roleQuota != nil {
			limit := r.getBucketLimit(roleQuota, bucketType)
			if limit > maxLimit {
				maxLimit = limit
			}
		} else if err != nil && !errors.Is(err, domain.ErrNotFound) && !errors.Is(err, sql.ErrNoRows) {
			r.logger.Error().Err(err).Str("role_id", roleID.String()).Msg("Failed to get role quota")
		}
	}

	// If no quota found, use defaults
	if maxLimit == 0 {
		defaultQuota := domain.DefaultQuotas()
		maxLimit = r.getBucketLimit(&defaultQuota, bucketType)
	}

	return maxLimit, window, nil
}

func (r *RedisQuotaResolver) getBucketLimit(quota *domain.Quota, bucketType domain.BucketType) int {
	switch bucketType {
	case domain.BucketTypeRequests:
		return quota.RequestsPerMin
	case domain.BucketTypeTokens:
		return quota.TokensPerMin
	case domain.BucketTypeToolExecs:
		return quota.ToolExecsPerMin
	default:
		return 0
	}
}