package redis

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/domain/delegation"
)

func setupMiniredis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })

	return mr, client
}

func TestRedisBudgetDecrementor_DecrementBudget(t *testing.T) {
	_, client := setupMiniredis(t)
	dec := NewRedisBudgetDecrementor(client, zerolog.Nop())

	chainID := domain.NewUUID()

	// Initialize budget pool to 10
	err := dec.InitBudget(context.Background(), chainID, 10)
	require.NoError(t, err)

	// Decrement by 1
	remaining, err := dec.DecrementBudget(context.Background(), chainID, 1)
	require.NoError(t, err)
	assert.Equal(t, 9, remaining)

	// Decrement by 3
	remaining, err = dec.DecrementBudget(context.Background(), chainID, 3)
	require.NoError(t, err)
	assert.Equal(t, 6, remaining)
}

func TestRedisBudgetDecrementor_Exhausted(t *testing.T) {
	_, client := setupMiniredis(t)
	dec := NewRedisBudgetDecrementor(client, zerolog.Nop())

	chainID := domain.NewUUID()

	// Initialize budget pool to 2
	err := dec.InitBudget(context.Background(), chainID, 2)
	require.NoError(t, err)

	// Consume all budget
	remaining, err := dec.DecrementBudget(context.Background(), chainID, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, remaining)

	remaining, err = dec.DecrementBudget(context.Background(), chainID, 1)
	require.NoError(t, err)
	assert.Equal(t, 0, remaining)

	// Next decrement MUST fail — budget exhausted
	_, err = dec.DecrementBudget(context.Background(), chainID, 1)
	require.ErrorIs(t, err, delegation.ErrBudgetExhausted)
}

func TestRedisBudgetDecrementor_MutationEvasion(t *testing.T) {
	// Client claims budget_remaining=100 in the grant envelope.
	// The gateway MUST NOT trust client-side budget claims.
	// It uses the server-side Redis pool as the source of truth.
	_, client := setupMiniredis(t)
	dec := NewRedisBudgetDecrementor(client, zerolog.Nop())

	chainID := domain.NewUUID()

	// Server-side pool is initialized to 5
	err := dec.InitBudget(context.Background(), chainID, 5)
	require.NoError(t, err)

	// Even if the grant says BudgetRemaining=100, the gateway decrements
	// from the server-side pool. After 5 decrements, it's exhausted.
	for i := 0; i < 5; i++ {
		_, err := dec.DecrementBudget(context.Background(), chainID, 1)
		require.NoError(t, err, "decrement %d should succeed", i)
	}

	// 6th decrement fails regardless of what the client claims
	_, err = dec.DecrementBudget(context.Background(), chainID, 1)
	require.ErrorIs(t, err, delegation.ErrBudgetExhausted)
}

func TestRedisBudgetDecrementor_NegativeBudget(t *testing.T) {
	_, client := setupMiniredis(t)
	dec := NewRedisBudgetDecrementor(client, zerolog.Nop())

	chainID := domain.NewUUID()

	// Initialize to 0
	err := dec.InitBudget(context.Background(), chainID, 0)
	require.NoError(t, err)

	// Decrement from 0 MUST fail
	_, err = dec.DecrementBudget(context.Background(), chainID, 1)
	require.ErrorIs(t, err, delegation.ErrBudgetExhausted)
}

func TestRedisBudgetDecrementor_DecrementMoreThanAvailable(t *testing.T) {
	_, client := setupMiniredis(t)
	dec := NewRedisBudgetDecrementor(client, zerolog.Nop())

	chainID := domain.NewUUID()

	// Initialize to 3
	err := dec.InitBudget(context.Background(), chainID, 3)
	require.NoError(t, err)

	// Try to decrement by 5 (more than available) — MUST fail atomically
	_, err = dec.DecrementBudget(context.Background(), chainID, 5)
	require.ErrorIs(t, err, delegation.ErrBudgetExhausted)

	// Verify pool is NOT modified (atomic — no partial decrement)
	remaining, err := dec.DecrementBudget(context.Background(), chainID, 1)
	require.NoError(t, err)
	assert.Equal(t, 2, remaining) // still 3-1=2, not 3-5=-2
}
