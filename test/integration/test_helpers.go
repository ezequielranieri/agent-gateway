package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
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
	"github.com/ezequielranieri/agent-gateway/internal/usecase/hitl"
)

// TestContainer holds the test containers and connections
type TestContainer struct {
	PGContainer     testcontainers.Container
	RedisContainer  testcontainers.Container
	DBPool          *pgxpool.Pool
	RedisClient     *redis.Client
	Ctx             context.Context
	Cancel          context.CancelFunc
}

// SetupTestContainers starts PostgreSQL and Redis containers and applies migrations
func SetupTestContainers(t *testing.T) *TestContainer {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)

	// Start PostgreSQL container
	pgContainer, err := pgmodule.Run(ctx,
		"postgres:16-alpine",
		pgmodule.WithDatabase("agent_gateway"),
		pgmodule.WithUsername("postgres"),
		pgmodule.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections")),
	)
	require.NoError(t, err)

	pgDSN, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	// Start Redis container
	redisContainer, err := redisModule.Run(ctx, "redis:7-alpine",
		testcontainers.WithWaitStrategy(wait.ForLog("Ready to accept connections")),
	)
	require.NoError(t, err)

	redisAddr, err := redisContainer.ConnectionString(ctx)
	require.NoError(t, err)

	// Connect to PostgreSQL
	dbPool, err := pgxpool.New(ctx, pgDSN)
	require.NoError(t, err)

	// Apply migrations
	err = ApplyMigrations(ctx, pgDSN)
	require.NoError(t, err)

	// Connect to Redis
	redisClient := redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})
	require.NoError(t, redisClient.Ping(ctx).Err())

	return &TestContainer{
		PGContainer:    pgContainer,
		RedisContainer: redisContainer,
		DBPool:         dbPool,
		RedisClient:    redisClient,
		Ctx:            ctx,
		Cancel:         cancel,
	}
}

// Teardown cleans up the test containers
func (tc *TestContainer) Teardown(t *testing.T) {
	_ = tc.PGContainer.Terminate(tc.Ctx)
	_ = tc.RedisContainer.Terminate(tc.Ctx)
	tc.DBPool.Close()
	tc.RedisClient.Close()
	tc.Cancel()
}

// ApplyMigrations applies all database migrations
func ApplyMigrations(ctx context.Context, dsn string) error {
	conn, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close()

	migrations := []string{
		`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`,
		`CREATE EXTENSION IF NOT EXISTS "pgcrypto"`,
		`CREATE TABLE IF NOT EXISTS public.tenants (
			id UUID PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			status VARCHAR(50) NOT NULL DEFAULT 'active',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
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
			id UUID NOT NULL DEFAULT gen_random_uuid(),
			tenant_id UUID NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
			seq BIGINT NOT NULL,
			actor_type TEXT NOT NULL CHECK (actor_type IN ('user', 'system', 'super_admin')),
			actor_id UUID,
			action TEXT NOT NULL,
			entity_type TEXT,
			entity_id UUID,
			payload JSONB NOT NULL,
			severity TEXT NOT NULL DEFAULT 'info' CHECK (severity IN ('info', 'warn', 'critical')),
			prev_hash BYTEA,
			hash BYTEA NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (id, tenant_id)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_audit_events_tenant_seq ON public.audit_events (tenant_id, seq)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_events_tenant_created ON public.audit_events (tenant_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_events_actor ON public.audit_events (tenant_id, actor_type, actor_id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_events_action ON public.audit_events (tenant_id, action)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_events_entity ON public.audit_events (tenant_id, entity_type, entity_id)`,
		`ALTER TABLE public.audit_events ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE public.audit_events FORCE ROW LEVEL SECURITY`,
		`CREATE POLICY audit_events_tenant_isolation ON public.audit_events USING (tenant_id = current_setting('app.current_tenant', true)::uuid) WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid)`,
		`REVOKE UPDATE, DELETE ON public.audit_events FROM PUBLIC`,
		`CREATE TABLE IF NOT EXISTS public.review_requests (
			id UUID NOT NULL DEFAULT gen_random_uuid(),
			tenant_id UUID NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
			requester_id UUID NOT NULL,
			reviewer_id UUID,
			payload JSONB NOT NULL,
			status TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'APPROVED', 'REJECTED', 'EXPIRED')),
			token_hash BYTEA NOT NULL,
			expires_at TIMESTAMPTZ NOT NULL,
			decided_at TIMESTAMPTZ,
			decided_by UUID,
			decision_reason TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (id, tenant_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_review_requests_token_hash ON public.review_requests (token_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_review_requests_tenant_status ON public.review_requests (tenant_id, status)`,
		`CREATE INDEX IF NOT EXISTS idx_review_requests_requester ON public.review_requests (tenant_id, requester_id)`,
		`CREATE INDEX IF NOT EXISTS idx_review_requests_reviewer ON public.review_requests (tenant_id, reviewer_id)`,
		`ALTER TABLE public.review_requests ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE public.review_requests FORCE ROW LEVEL SECURITY`,
		`CREATE POLICY review_requests_tenant_isolation ON public.review_requests USING (tenant_id = current_setting('app.current_tenant', true)::uuid) WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid)`,
		`CREATE TABLE IF NOT EXISTS public.guardrail_violations (
			id UUID NOT NULL DEFAULT gen_random_uuid(),
			tenant_id UUID NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
			request_id UUID,
			phase TEXT NOT NULL CHECK (phase IN ('input', 'output')),
			rule_id TEXT NOT NULL,
			severity TEXT NOT NULL CHECK (severity IN ('info', 'warn', 'critical')),
			payload_excerpt JSONB,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (id, tenant_id)
		)`,
		`ALTER TABLE public.guardrail_violations ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE public.guardrail_violations FORCE ROW LEVEL SECURITY`,
		`CREATE POLICY guardrail_violations_tenant_isolation ON public.guardrail_violations USING (tenant_id = current_setting('app.current_tenant', true)::uuid) WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid)`,
	}

	for _, migration := range migrations {
		_, err := conn.Exec(ctx, migration)
		if err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}

	return nil
}

// SetupTestData creates test tenant, user, and role
func SetupTestData(ctx context.Context, dbPool *pgxpool.Pool, tenantID, userID, roleID domain.UUID) error {
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

// CreateTestRouter creates a test router with all middleware and handlers
func CreateTestRouter(t *testing.T, tc *TestContainer, logger zerolog.Logger) (*chi.Mux, string, domain.UUID, domain.UUID, domain.UUID) {
	// Create test tenant and user
	tenantID := domain.NewUUID()
	userID := domain.NewUUID()
	roleID := domain.NewUUID()

	err := SetupTestData(tc.Ctx, tc.DBPool, tenantID, userID, roleID)
	require.NoError(t, err)

	// Initialize repositories
	userRepo := pgadapter.NewUserRepository(tc.DBPool)
	refreshRepo := pgadapter.NewRefreshTokenRepository(tc.DBPool)
	quotaRepo := pgadapter.NewQuotaRepository(tc.DBPool)
	auditRepo := pgadapter.NewAuditRepository(tc.DBPool)
	reviewRepo := pgadapter.NewReviewRepository(tc.DBPool)

	// Initialize JWT
	signingKey := []byte("test-secret-key-32-bytes-long!!")
	keyStore := jwt.NewKeyStore("v1", signingKey, map[string][]byte{"v1": signingKey})
	jwtService := jwt.NewAuthService(keyStore, "agent-gateway", "agent-gateway")

	// Initialize auth use case
	authUC := auth.NewAuthUseCase(userRepo, refreshRepo, jwtService)

	// Initialize HITL use case
	hitlUC := hitl.NewHITLUseCase(hitl.HITLConfig{
		ReviewRepo:   reviewRepo,
		AuditRepo:    auditRepo,
		DefaultTTL:   24 * time.Hour,
		Logger:       logger,
	})

	// Initialize handlers
	authHandlers := handlers.NewAuthHandlers(authUC, nil, logger)
	chatHandlers := handlers.NewChatHandlers(logger)
	adminAuditHandlers := handlers.NewAdminAuditHandlers(auditRepo, logger)
	reviewHandlers := handlers.NewReviewHandlers(hitlUC, reviewRepo, string(signingKey), logger)

	// Initialize middleware
	authMW := middleware.NewAuth(middleware.AuthConfig{
		JWTService: jwtService,
		Logger:     logger,
	})

	tenantMW := middleware.NewTenant(middleware.TenantConfig{
		Pool:   tc.DBPool,
		Logger: logger,
	})

	// Initialize Redis rate limiter
	redisRateLimiter := redisadapter.NewRedisRateLimiter(tc.RedisClient, logger, true)
	redisQuotaResolver := redisadapter.NewRedisQuotaResolver(quotaRepo, logger)

	rateLimitMW := middleware.NewRateLimit(middleware.RateLimitConfig{
		Limiter:       redisRateLimiter,
		QuotaResolver: redisQuotaResolver,
		FailOpen:      true,
		Logger:        logger,
	})

	auditMW := middleware.NewAudit(middleware.AuditConfig{
		Store:  auditRepo,
		Logger: logger,
	})

	// Create router
	router := api.NewRouter(api.RouterConfig{
		Config:              &config.Config{RateLimit: config.RateLimitConfig{FailOpen: true}},
		Logger:              logger,
		AuthMW:              authMW,
		TenantMW:            tenantMW,
		RateLimitMW:         rateLimitMW,
		AuditMW:             auditMW,
		GuardrailsMW:        middleware.NewGuardrails(middleware.GuardrailsConfig{Checker: &noopGuardrailChecker{}, Logger: logger}),
		HITLMW:              middleware.NewHITL(middleware.HITLConfig{Logger: logger}),
		AuthHandlers:        authHandlers,
		ReviewHandlers:      reviewHandlers,
		ChatHandlers:        chatHandlers,
		AdminAuditHandlers:  adminAuditHandlers,
	})

	// Generate test token
	token, err := jwtService.IssueAccessToken(jwt.Claims{
		UserID:   userID.String(),
		TenantID: tenantID.String(),
		Role:     "admin",
		Scopes:   []string{"*"},
	})
	require.NoError(t, err)

	return router, token, tenantID, userID, roleID
}

// GenerateTestToken generates a test JWT token
func GenerateTestToken(t *testing.T, jwtService *jwt.AuthService, userID, tenantID domain.UUID) string {
	token, err := jwtService.IssueAccessToken(jwt.Claims{
		UserID:   userID.String(),
		TenantID: tenantID.String(),
		Role:     "admin",
		Scopes:   []string{"*"},
	})
	require.NoError(t, err)
	return token
}

// MakeRequest makes an HTTP request to the router
func MakeRequest(router http.Handler, method, path, token string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if body != "" {
		req = httptest.NewRequest(method, path, nil)
		// We'll handle body separately
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// No-op implementations for tests
type noopGuardrailChecker struct{}

func (n *noopGuardrailChecker) CheckInput(ctx context.Context, tenantID domain.UUID, input string) (*domain.GuardrailViolation, error) {
	return nil, nil
}

func (n *noopGuardrailChecker) CheckOutput(ctx context.Context, tenantID domain.UUID, output string) (*domain.GuardrailViolation, error) {
	return nil, nil
}

func (n *noopGuardrailChecker) SanitizeOutput(output string) string {
	return output
}

type noopReviewStore struct{}

func (n *noopReviewStore) GetByToken(ctx context.Context, tokenHash string) (*domain.ReviewRequest, error) {
	return nil, nil
}

func (n *noopReviewStore) Update(ctx context.Context, req *domain.ReviewRequest) error {
	return nil
}