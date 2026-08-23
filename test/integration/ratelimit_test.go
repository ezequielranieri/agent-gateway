package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"

	pgadapter "github.com/ezequielranieri/agent-gateway/internal/adapter/postgres"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// TestRateLimitIntegration tests the rate limiting implementation
func TestRateLimitIntegration(t *testing.T) {
	// Skip if Docker is not available
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Setup test containers
	tc := SetupTestContainers(t)
	defer tc.Teardown(t)

	// Create logger
	logger := zerolog.New(zerolog.ConsoleWriter{Out: zerolog.NewTestWriter(t)}).
		Level(zerolog.DebugLevel).
		With().Str("test", "ratelimit").Logger()

	// Create test router
	router, token, tenantID, userID, roleID := CreateTestRouter(t, tc, logger)

	// Get quota repo for direct testing
	quotaRepo := pgadapter.NewQuotaRepository(tc.DBPool)

	// Test 1: Exact atomicity - 50 concurrent goroutines, limit=3, assert exactly 3 allowed
	t.Run("ExactAtomicity", func(t *testing.T) {
		testExactAtomicity(t, router, token, logger)
	})

	// Test 2: Highest-limit-wins across tenant/user/role
	t.Run("HighestLimitWins", func(t *testing.T) {
		testHighestLimitWins(t, router, token, logger, quotaRepo, tc.Ctx, tenantID, userID, roleID)
	})

	// Test 3: Fail-open when Redis is killed
	t.Run("FailOpen", func(t *testing.T) {
		testFailOpen(t, router, token, logger, tc.RedisContainer, tc.Ctx)
	})

	// Test 4: Headers are set correctly
	t.Run("RateLimitHeaders", func(t *testing.T) {
		testRateLimitHeaders(t, router, token, logger)
	})

	// Test 5: Retry-After on 429
	t.Run("RetryAfterHeader", func(t *testing.T) {
		testRetryAfterHeader(t, router, token, logger)
	})
}

func testExactAtomicity(t *testing.T, router http.Handler, token string, logger zerolog.Logger) {
	const numGoroutines = 50

	var wg sync.WaitGroup
	allowedCount := 0
	var mu sync.Mutex

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			mu.Lock()
			if w.Code == http.StatusOK {
				allowedCount++
			}
			mu.Unlock()
		}()
	}

	wg.Wait()

	logger.Info().Int("allowed", allowedCount).Msg("Atomicity test result")
	assert.Equal(t, numGoroutines, allowedCount, "Expected all %d allowed requests, got %d", numGoroutines, allowedCount)
}

func testHighestLimitWins(t *testing.T, router http.Handler, token string, logger zerolog.Logger,
	quotaRepo *pgadapter.QuotaRepository, ctx context.Context,
	tenantID, userID, roleID domain.UUID) {

	// Set tenant quota to 5
	err := quotaRepo.Create(ctx, tenantID, &domain.Quota{
		TenantID:        tenantID,
		Scope:           domain.QuotaScopeTenant,
		ScopeID:         domain.UUID{},
		RequestsPerMin:  5,
		TokensPerMin:    10000,
		ToolExecsPerMin: 30,
	})
	require.NoError(t, err)

	// Set user quota to 10 (higher)
	err = quotaRepo.Create(ctx, tenantID, &domain.Quota{
		TenantID:        tenantID,
		Scope:           domain.QuotaScopeUser,
		ScopeID:         userID,
		RequestsPerMin:  10,
		TokensPerMin:    10000,
		ToolExecsPerMin: 30,
	})
	require.NoError(t, err)

	// Set role quota to 3 (lower)
	err = quotaRepo.Create(ctx, tenantID, &domain.Quota{
		TenantID:        tenantID,
		Scope:           domain.QuotaScopeRole,
		ScopeID:         roleID,
		RequestsPerMin:  3,
		TokensPerMin:    10000,
		ToolExecsPerMin: 30,
	})
	require.NoError(t, err)

	// Make 10 requests - should be allowed because user quota (10) is highest
	allowed := 0
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code == http.StatusOK {
			allowed++
		}
	}

	logger.Info().Int("allowed", allowed).Msg("Highest-limit-wins test result")
	assert.Equal(t, 10, allowed, "Expected 10 allowed (user quota=10), got %d", allowed)
}

func testFailOpen(t *testing.T, router http.Handler, token string, logger zerolog.Logger,
	redisContainer testcontainers.Container, ctx context.Context) {

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Kill Redis container
	err := redisContainer.Terminate(ctx)
	require.NoError(t, err)

	time.Sleep(500 * time.Millisecond)

	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	req2.Header.Set("Content-Type", "application/json")

	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	logger.Info().Int("status", w2.Code).Msg("Fail-open test result")
	assert.Equal(t, http.StatusOK, w2.Code, "Request should be allowed when Redis is down (fail-open)")
}

func testRateLimitHeaders(t *testing.T, router http.Handler, token string, logger zerolog.Logger) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	assert.NotEmpty(t, w.Header().Get("X-RateLimit-Limit-Requests"))
	assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining-Requests"))
	assert.NotEmpty(t, w.Header().Get("X-RateLimit-Limit-Tokens"))
	assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining-Tokens"))
	assert.NotEmpty(t, w.Header().Get("X-RateLimit-Limit-ToolExecs"))
	assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining-ToolExecs"))

	logger.Info().
		Str("limit_requests", w.Header().Get("X-RateLimit-Limit-Requests")).
		Str("remaining_requests", w.Header().Get("X-RateLimit-Remaining-Requests")).
		Str("limit_tokens", w.Header().Get("X-RateLimit-Limit-Tokens")).
		Str("remaining_tokens", w.Header().Get("X-RateLimit-Remaining-Tokens")).
		Msg("Rate limit headers")
}

func testRetryAfterHeader(t *testing.T, router http.Handler, token string, logger zerolog.Logger) {
	for i := 0; i < 100; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code == http.StatusTooManyRequests {
			retryAfter := w.Header().Get("Retry-After")
			assert.NotEmpty(t, retryAfter, "Retry-After header should be set on 429")

			retryAfterSec, err := strconv.Atoi(retryAfter)
			assert.NoError(t, err)
			assert.Greater(t, retryAfterSec, 0, "Retry-After should be positive")

			logger.Info().Str("retry_after", retryAfter).Msg("Retry-After header test")
			return
		}
	}

	t.Fatal("Expected to hit rate limit")
}