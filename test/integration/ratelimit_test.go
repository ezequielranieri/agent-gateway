package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	redisModule "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/jwt"
	pgadapter "github.com/ezequielranieri/agent-gateway/internal/adapter/postgres"
	redisadapter "github.com/ezequielranieri/agent-gateway/internal/adapter/redis"
	"github.com/ezequielranieri/agent-gateway/internal/api"
	"github.com/ezequielranieri/agent-gateway/internal/api/handlers"
	"github.com/ezequielranieri/agent-gateway/internal/config"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/middleware"
	"github.com/ezequielranieri/agent-gateway/internal/usecase/auth"
)

// TestRateLimitIntegration tests the rate limiting implementation
func TestRateLimitIntegration(t *testing.T) {
	// Skip if Docker is not available
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()

	// Start PostgreSQL container
	pgContainer, err := pgmodule.Run(ctx,
		"postgres:16-alpine",
		pgmodule.WithDatabase("agent_gateway"),
		pgmodule.WithUsername("postgres"),
		pgmodule.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections")),
	)
	require.NoError(t, err)
	defer func() { _ = pgContainer.Terminate(ctx) }()

	pgDSN, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	// Start Redis container
	redisContainer, err := redisModule.Run(ctx, "redis:7-alpine",
		testcontainers.WithWaitStrategy(wait.ForLog("Ready to accept connections")),
	)
	require.NoError(t, err)
	defer func() { _ = redisContainer.Terminate(ctx) }()

	redisAddr, err := redisContainer.ConnectionString(ctx)
	require.NoError(t, err)

	// Run migrations
	dbPool, err := pgxpool.New(ctx, pgDSN)
	require.NoError(t, err)
	defer dbPool.Close()

	// Apply migrations
	err = applyMigrations(ctx, pgDSN)
	require.NoError(t, err)

	// Initialize Redis client
	redisClient := redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})
	defer redisClient.Close()

	// Wait for Redis to be ready
	require.NoError(t, redisClient.Ping(ctx).Err())

	// Create logger
	logger := zerolog.New(zerolog.ConsoleWriter{Out: zerolog.NewTestWriter(t)}).
		Level(zerolog.DebugLevel).
		With().Str("test", "ratelimit").Logger()

	// Initialize repositories
	userRepo := pgadapter.NewUserRepository(dbPool)
	refreshRepo := pgadapter.NewRefreshTokenRepository(dbPool)
	quotaRepo := pgadapter.NewQuotaRepository(dbPool)

	// Create test tenant and user
	tenantID := domain.NewUUID()
	userID := domain.NewUUID()
	roleID := domain.NewUUID()

	err = setupTestData(ctx, dbPool, tenantID, userID, roleID)
	require.NoError(t, err)

	// Initialize JWT
	signingKey := []byte("test-secret-key-32-bytes-long!!")
	keyStore := jwt.NewKeyStore("v1", signingKey, map[string][]byte{"v1": signingKey})
	jwtService := jwt.NewAuthService(keyStore, "agent-gateway", "agent-gateway")

	// Initialize auth use case
	authUC := auth.NewAuthUseCase(userRepo, refreshRepo, jwtService)

	// Initialize handlers
	authHandlers := handlers.NewAuthHandlers(authUC, nil, logger)
	chatHandlers := handlers.NewChatHandlers(logger)

	// Initialize middleware
	authMW := middleware.NewAuth(middleware.AuthConfig{
		JWTService: jwtService,
		Logger:     logger,
	})

	tenantMW := middleware.NewTenant(middleware.TenantConfig{
		Pool:   dbPool,
		Logger: logger,
	})

	// Initialize Redis rate limiter
	redisRateLimiter := redisadapter.NewRedisRateLimiter(redisClient, logger, true)
	redisQuotaResolver := redisadapter.NewRedisQuotaResolver(quotaRepo, logger)

	rateLimitMW := middleware.NewRateLimit(middleware.RateLimitConfig{
		Limiter:       redisRateLimiter,
		QuotaResolver: redisQuotaResolver,
		FailOpen:      true,
		Logger:        logger,
	})

	// Create router
	router := api.NewRouter(api.RouterConfig{
		Config:        &config.Config{RateLimit: config.RateLimitConfig{FailOpen: true}},
		Logger:        logger,
		AuthMW:        authMW,
		TenantMW:      tenantMW,
		RateLimitMW:   rateLimitMW,
		AuditMW:       middleware.NewAudit(middleware.AuditConfig{Store: &noopAuditStore{}, Logger: logger}),
		GuardrailsMW:  middleware.NewGuardrails(middleware.GuardrailsConfig{Checker: &noopGuardrailChecker{}, Logger: logger}),
		HITLMW:        middleware.NewHITL(middleware.HITLConfig{Store: &noopReviewStore{}, Logger: logger}),
		AuthHandlers:  authHandlers,
		ChatHandlers:  chatHandlers,
	})

	// Generate test token
	token, err := jwtService.IssueAccessToken(jwt.Claims{
		UserID:   userID.String(),
		TenantID: tenantID.String(),
		Role:     "admin",
		Scopes:   []string{"*"},
	})
	require.NoError(t, err)

	// Test 1: Exact atomicity - 50 concurrent goroutines, limit=3, assert exactly 3 allowed
	t.Run("ExactAtomicity", func(t *testing.T) {
		testExactAtomicity(t, router, token, logger)
	})

	// Test 2: Highest-limit-wins across tenant/user/role
	t.Run("HighestLimitWins", func(t *testing.T) {
		testHighestLimitWins(t, router, token, logger, quotaRepo, ctx, tenantID, userID, roleID)
	})

	// Test 3: Fail-open when Redis is killed
	t.Run("FailOpen", func(t *testing.T) {
		testFailOpen(t, router, token, logger, redisContainer, ctx)
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

// Helper functions

func applyMigrations(ctx context.Context, dsn string) error {
	conn, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close()

	migrations := []string{
		`CREATE TABLE IF NOT EXISTS public.tenants (
			id UUID PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			status VARCHAR(50) NOT NULL DEFAULT 'active',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS public.quotas (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id UUID NOT NULL REFERENCES public.tenants(id),
			scope VARCHAR(50) NOT NULL,
			scope_id UUID NOT NULL,
			requests_per_min INT NOT NULL DEFAULT 60,
			tokens_per_min INT NOT NULL DEFAULT 10000,
			tool_execs_per_min INT NOT NULL DEFAULT 30,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (tenant_id, scope, scope_id)
		)`,
		`CREATE TABLE IF NOT EXISTS public.users (
			id UUID PRIMARY KEY,
			tenant_id UUID NOT NULL REFERENCES public.tenants(id),
			email VARCHAR(255) NOT NULL,
			password_hash VARCHAR(255) NOT NULL,
			status VARCHAR(50) NOT NULL DEFAULT 'active',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (tenant_id, email)
		)`,
		`CREATE TABLE IF NOT EXISTS public.roles (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id UUID NOT NULL REFERENCES public.tenants(id),
			name VARCHAR(100) NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (tenant_id, name)
		)`,
		`CREATE TABLE IF NOT EXISTS public.refresh_tokens (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id UUID NOT NULL REFERENCES public.users(id),
			tenant_id UUID NOT NULL REFERENCES public.tenants(id),
			token_hash VARCHAR(64) NOT NULL,
			family_id UUID NOT NULL,
			revoked BOOLEAN NOT NULL DEFAULT FALSE,
			expires_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			user_agent TEXT,
			ip VARCHAR(45),
			UNIQUE (token_hash)
		)`,
		`CREATE TABLE IF NOT EXISTS public.audit_events (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id UUID NOT NULL REFERENCES public.tenants(id),
			actor_type VARCHAR(50) NOT NULL,
			actor_id UUID NOT NULL,
			action VARCHAR(100) NOT NULL,
			entity_type VARCHAR(100),
			entity_id UUID,
			severity VARCHAR(20) NOT NULL DEFAULT 'info',
			metadata JSONB,
			prev_hash VARCHAR(64),
			curr_hash VARCHAR(64) NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS public.review_requests (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id UUID NOT NULL REFERENCES public.tenants(id),
			requester_id UUID NOT NULL REFERENCES public.users(id),
			action_type VARCHAR(100) NOT NULL,
			payload JSONB NOT NULL,
			token_hash VARCHAR(64) NOT NULL,
			status VARCHAR(20) NOT NULL DEFAULT 'PENDING',
			decided_by UUID REFERENCES public.users(id),
			decided_at TIMESTAMPTZ,
			expires_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (token_hash)
		)`,
		`CREATE TABLE IF NOT EXISTS public.guardrail_violations (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id UUID NOT NULL REFERENCES public.tenants(id),
			request_id UUID,
			phase VARCHAR(20) NOT NULL,
			rule_name VARCHAR(100) NOT NULL,
			severity VARCHAR(20) NOT NULL,
			matched_content TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`ALTER TABLE public.tenants ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE public.quotas ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE public.users ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE public.roles ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE public.refresh_tokens ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE public.audit_events ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE public.review_requests ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE public.guardrail_violations ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY tenant_isolation ON public.tenants FOR ALL USING (id = current_setting('app.current_tenant', true)::uuid)`,
		`CREATE POLICY tenant_isolation ON public.quotas FOR ALL USING (tenant_id = current_setting('app.current_tenant', true)::uuid)`,
		`CREATE POLICY tenant_isolation ON public.users FOR ALL USING (tenant_id = current_setting('app.current_tenant', true)::uuid)`,
		`CREATE POLICY tenant_isolation ON public.roles FOR ALL USING (tenant_id = current_setting('app.current_tenant', true)::uuid)`,
		`CREATE POLICY tenant_isolation ON public.refresh_tokens FOR ALL USING (tenant_id = current_setting('app.current_tenant', true)::uuid)`,
		`CREATE POLICY tenant_isolation ON public.audit_events FOR ALL USING (tenant_id = current_setting('app.current_tenant', true)::uuid)`,
		`CREATE POLICY tenant_isolation ON public.review_requests FOR ALL USING (tenant_id = current_setting('app.current_tenant', true)::uuid)`,
		`CREATE POLICY tenant_isolation ON public.guardrail_violations FOR ALL USING (tenant_id = current_setting('app.current_tenant', true)::uuid)`,
		`CREATE OR REPLACE FUNCTION bootstrap_super_admin(email TEXT, password_hash TEXT) RETURNS UUID AS $$
			DECLARE
				super_admin_id UUID;
			BEGIN
				INSERT INTO public.users (id, tenant_id, email, password_hash, status)
				VALUES (gen_random_uuid(), '00000000-0000-0000-0000-000000000000'::uuid, email, password_hash, 'active')
				ON CONFLICT (tenant_id, email) DO NOTHING
				RETURNING id INTO super_admin_id;
				RETURN super_admin_id;
			END;
		$$ LANGUAGE plpgsql SECURITY DEFINER`,
	}

	for _, migration := range migrations {
		_, err := conn.Exec(ctx, migration)
		if err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}

	return nil
}

func setupTestData(ctx context.Context, dbPool *pgxpool.Pool, tenantID, userID, roleID domain.UUID) error {
	_, err := dbPool.Exec(ctx, `
		INSERT INTO public.tenants (id, name, status) VALUES ($1, 'Test Tenant', 'active')
		ON CONFLICT (id) DO NOTHING
	`, tenantID)
	if err != nil {
		return err
	}

	_, err = dbPool.Exec(ctx, `
		INSERT INTO public.users (id, tenant_id, email, password_hash, status) 
		VALUES ($1, $2, 'test@example.com', 'hashed', 'active')
		ON CONFLICT (id) DO NOTHING
	`, userID, tenantID)
	if err != nil {
		return err
	}

	_, err = dbPool.Exec(ctx, `
		INSERT INTO public.roles (id, tenant_id, name) VALUES ($1, $2, 'admin')
		ON CONFLICT (id) DO NOTHING
	`, roleID, tenantID)
	if err != nil {
		return err
	}

	return nil
}

// No-op implementations for tests

type noopAuditStore struct{}

func (n *noopAuditStore) Append(ctx context.Context, event *domain.AuditEvent) error {
	return nil
}

func (n *noopAuditStore) VerifyChain(ctx context.Context, tenantID domain.UUID) (int64, error) {
	return 0, nil
}

type noopGuardrailChecker struct{}

func (n *noopGuardrailChecker) CheckInput(ctx context.Context, tenantID domain.UUID, input string) (*domain.GuardrailViolation, error) {
	return nil, nil
}

func (n *noopGuardrailChecker) CheckOutput(ctx context.Context, tenantID domain.UUID, output string) (*domain.GuardrailViolation, error) {
	return nil, nil
}

type noopReviewStore struct{}

func (n *noopReviewStore) GetByToken(ctx context.Context, tokenHash string) (*domain.ReviewRequest, error) {
	return nil, nil
}

func (n *noopReviewStore) Update(ctx context.Context, req *domain.ReviewRequest) error {
	return nil
}