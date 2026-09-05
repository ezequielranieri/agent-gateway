package redis

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/domain/delegation"
)

// budgetDecrementLuaScript atomically decrements a per-chain budget pool.
// KEYS[1] = budget key (delegation:budget:{chainID})
// ARGV[1] = amount to decrement
// Returns: remaining budget after decrement, or -1 if exhausted.
//
// This is a compare-and-swap loop in Lua: read current, check sufficient,
// decrement only if sufficient. Atomic under Redis single-thread guarantee.
const budgetDecrementLuaScript = `
local key = KEYS[1]
local amount = tonumber(ARGV[1])

if not amount or amount <= 0 then
    amount = 1
end

local current = redis.call('GET', key)
if current == false then
    -- Key does not exist — budget not initialized
    return -1
end

current = tonumber(current)
if current < amount then
    -- Insufficient budget — do NOT decrement (atomic rollback)
    return -1
end

local remaining = current - amount
redis.call('SET', key, remaining)
return remaining
`

// RedisBudgetDecrementor implements delegation.DelegationBudgetDecrementor
// using Redis atomic Lua scripting. Follows the same fail-open/fail-closed
// discipline as the GCRA rate limiter.
type RedisBudgetDecrementor struct {
	client  *redis.Client
	script  *redis.Script
	logger  zerolog.Logger
}

// NewRedisBudgetDecrementor creates a new Redis-backed budget decrementor.
func NewRedisBudgetDecrementor(client *redis.Client, logger zerolog.Logger) *RedisBudgetDecrementor {
	return &RedisBudgetDecrementor{
		client: client,
		script: redis.NewScript(budgetDecrementLuaScript),
		logger: logger.With().Str("component", "redis_budget_decrementor").Logger(),
	}
}

// budgetKey returns the Redis key for a chain's budget pool.
func budgetKey(chainID domain.UUID) string {
	return fmt.Sprintf("delegation:budget:%s", chainID.String())
}

// InitBudget initializes the budget pool for a chain.
// Called at delegation issuance time (root grant creation).
func (r *RedisBudgetDecrementor) InitBudget(ctx context.Context, chainID domain.UUID, budget int) error {
	key := budgetKey(chainID)
	err := r.client.Set(ctx, key, budget, 0).Err()
	if err != nil {
		r.logger.Error().Err(err).Str("chain_id", chainID.String()).Msg("Failed to initialize budget pool")
		return fmt.Errorf("init budget: %w", err)
	}
	return nil
}

// DecrementBudget atomically decrements the budget for a chain.
// Returns the remaining budget after decrement.
// Returns delegation.ErrBudgetExhausted when the budget pool is depleted.
//
// The Lua script ensures atomicity: no partial decrements, no race conditions
// under concurrent requests. The client-side budget claim in the grant envelope
// is NEVER trusted — the server-side Redis pool is the source of truth.
func (r *RedisBudgetDecrementor) DecrementBudget(ctx context.Context, chainID domain.UUID, amount int) (int, error) {
	if amount <= 0 {
		amount = 1
	}

	key := budgetKey(chainID)
	result, err := r.script.Run(ctx, r.client, []string{key}, amount).Int()
	if err != nil {
		r.logger.Error().Err(err).Str("chain_id", chainID.String()).Msg("Budget decrement script error")
		return 0, fmt.Errorf("decrement budget: %w", err)
	}

	if result < 0 {
		return 0, delegation.ErrBudgetExhausted
	}

	return result, nil
}
