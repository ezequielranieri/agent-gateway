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
	PGContainer    testcontainers.Container
	RedisContainer testcontainers.Container
	DBPool         *pgxpool.Pool
	RedisClient    *redis.Client
	Ctx            context.Context
	Cancel         context.CancelFunc
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

	// Get Redis host:port (Endpoint returns "host:port" format)
	redisEndpoint, err := redisContainer.Endpoint(ctx, "")
	require.NoError(t, err)
	redisAddr := redisEndpoint

	// Connect to PostgreSQL
	dbPool, err := pgxpool.New(ctx, pgDSN)
	require.NoError(t, err)

	// Increase pool size for concurrent tests
	dbPool.Config().MaxConns = 50

	// Apply migrations matching production schema
	err = ApplyMigrations(ctx, dbPool)
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

// ApplyMigrations applies database schema matching production migrations
func ApplyMigrations(ctx context.Context, dbPool *pgxpool.Pool) error {
	migrations := []string{
		// 0001_extensions
		`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`,
		`CREATE EXTENSION IF NOT EXISTS "pgcrypto"`,
		`CREATE EXTENSION IF NOT EXISTS "citext"`,

		// 0002_super_admins
		`CREATE TABLE IF NOT EXISTS public.super_admins (
			id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			email        citext NOT NULL UNIQUE,
			password_hash text   NOT NULL,
			created_at   timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_super_admins_email ON public.super_admins (email)`,

		// 0003_tenants
		`CREATE TABLE IF NOT EXISTS public.tenants (
			id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			name      varchar(255) NOT NULL,
			status    varchar(50) NOT NULL DEFAULT 'active',
			created_at timestamptz NOT NULL DEFAULT now()
		)`,

		// 0004_roles
		`CREATE TABLE IF NOT EXISTS public.roles (
			id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			name        varchar(100) NOT NULL,
			description text,
			created_at  timestamptz NOT NULL DEFAULT now()
		)`,

		// 0005_users
		`CREATE TABLE IF NOT EXISTS public.users (
			id            uuid NOT NULL DEFAULT gen_random_uuid(),
			tenant_id     uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
			email         citext NOT NULL,
			password_hash text   NOT NULL,
			status        varchar(50) NOT NULL DEFAULT 'active',
			created_at    timestamptz NOT NULL DEFAULT now(),
			updated_at    timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (id, tenant_id)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_users_tenant_email ON public.users (tenant_id, email)`,
		`ALTER TABLE public.users ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE public.users FORCE ROW LEVEL SECURITY`,
		`CREATE POLICY users_tenant_isolation ON public.users 
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid) 
			WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid)`,

		// 0006_role_permissions
		`CREATE TABLE IF NOT EXISTS public.role_permissions (
			tenant_id  uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
			role_id    uuid NOT NULL REFERENCES public.roles(id) ON DELETE CASCADE,
			permission varchar(100) NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (tenant_id, role_id, permission)
		)`,
		`ALTER TABLE public.role_permissions ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE public.role_permissions FORCE ROW LEVEL SECURITY`,
		`CREATE POLICY role_permissions_tenant_isolation ON public.role_permissions 
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid) 
			WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid)`,

		// 0007_user_roles
		`CREATE TABLE IF NOT EXISTS public.user_roles (
			tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
			user_id   uuid NOT NULL,
			role_id   uuid NOT NULL REFERENCES public.roles(id) ON DELETE CASCADE,
			created_at timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (tenant_id, user_id, role_id)
		)`,
		`ALTER TABLE public.user_roles ADD CONSTRAINT fk_user_roles_user 
			FOREIGN KEY (user_id, tenant_id) REFERENCES public.users (id, tenant_id) ON DELETE CASCADE`,
		`ALTER TABLE public.user_roles ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE public.user_roles FORCE ROW LEVEL SECURITY`,
		`CREATE POLICY user_roles_tenant_isolation ON public.user_roles 
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid) 
			WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid)`,

		// 0008_quotas
		`CREATE TABLE IF NOT EXISTS public.quotas (
			id                 uuid NOT NULL DEFAULT gen_random_uuid(),
			tenant_id          uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
			scope              varchar(20) NOT NULL CHECK (scope IN ('tenant', 'user', 'role')),
			scope_id           uuid NOT NULL,
			requests_per_min   int NOT NULL DEFAULT 60,
			tokens_per_min     int NOT NULL DEFAULT 10000,
			tool_execs_per_min int NOT NULL DEFAULT 30,
			created_at         timestamptz NOT NULL DEFAULT now(),
			updated_at         timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (id, tenant_id)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_quotas_tenant_scope ON public.quotas (tenant_id, scope, scope_id)`,
		`ALTER TABLE public.quotas ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE public.quotas FORCE ROW LEVEL SECURITY`,
		`CREATE POLICY quotas_tenant_isolation ON public.quotas 
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid) 
			WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid)`,

		// 0009_refresh_tokens
		`CREATE TABLE IF NOT EXISTS public.refresh_tokens (
			id           uuid NOT NULL DEFAULT gen_random_uuid(),
			user_id      uuid NOT NULL,
			tenant_id    uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
			token_hash   varchar(64) NOT NULL,
			family_id    uuid NOT NULL,
			revoked      boolean NOT NULL DEFAULT false,
			expires_at   timestamptz NOT NULL,
			user_agent   text,
			ip           varchar(45),
			PRIMARY KEY (id, tenant_id)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_refresh_tokens_hash ON public.refresh_tokens (token_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_refresh_tokens_family ON public.refresh_tokens (tenant_id, family_id)`,
		`CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user ON public.refresh_tokens (tenant_id, user_id)`,
		`ALTER TABLE public.refresh_tokens ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE public.refresh_tokens FORCE ROW LEVEL SECURITY`,
		`CREATE POLICY refresh_tokens_tenant_isolation ON public.refresh_tokens 
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid) 
			WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid)`,

		// 0010_audit_events
		`CREATE TABLE IF NOT EXISTS public.audit_events (
			id              uuid NOT NULL DEFAULT gen_random_uuid(),
			tenant_id       uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
			seq             bigint NOT NULL,
			actor_type      text NOT NULL CHECK (actor_type IN ('user', 'system', 'super_admin')),
			actor_id        uuid,
			action          text NOT NULL,
			entity_type     text,
			entity_id       uuid,
			payload         jsonb NOT NULL,
			severity        text NOT NULL DEFAULT 'info' CHECK (severity IN ('info', 'warn', 'critical')),
			prev_hash       bytea,
			hash            bytea NOT NULL,
			created_at      timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (id, tenant_id)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_audit_events_tenant_seq ON public.audit_events (tenant_id, seq)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_events_tenant_created ON public.audit_events (tenant_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_events_actor ON public.audit_events (tenant_id, actor_type, actor_id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_events_action ON public.audit_events (tenant_id, action)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_events_entity ON public.audit_events (tenant_id, entity_type, entity_id)`,
		`ALTER TABLE public.audit_events ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE public.audit_events FORCE ROW LEVEL SECURITY`,
		`CREATE POLICY audit_events_tenant_isolation ON public.audit_events 
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid) 
			WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid)`,
		`REVOKE UPDATE, DELETE ON public.audit_events FROM PUBLIC`,

		// 0011_review_requests
		`CREATE TABLE IF NOT EXISTS public.review_requests (
			id              uuid NOT NULL DEFAULT gen_random_uuid(),
			tenant_id       uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
			requester_id    uuid NOT NULL,
			reviewer_id     uuid,
			action          text NOT NULL,
			payload         jsonb NOT NULL,
			status          text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'APPROVED', 'REJECTED', 'EXPIRED', 'EXECUTED')),
			token_hash      bytea NOT NULL,
			expires_at      timestamptz NOT NULL,
			decided_at      timestamptz,
			decided_by      uuid,
			decision_reason text,
			created_at      timestamptz NOT NULL DEFAULT now(),
			updated_at      timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (id, tenant_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_review_requests_token_hash ON public.review_requests (token_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_review_requests_tenant_status ON public.review_requests (tenant_id, status)`,
		`CREATE INDEX IF NOT EXISTS idx_review_requests_requester ON public.review_requests (tenant_id, requester_id)`,
		`CREATE INDEX IF NOT EXISTS idx_review_requests_reviewer ON public.review_requests (tenant_id, reviewer_id)`,
		`ALTER TABLE public.review_requests ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE public.review_requests FORCE ROW LEVEL SECURITY`,
		`CREATE POLICY review_requests_tenant_isolation ON public.review_requests 
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid) 
			WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid)`,

		// 0012_guardrail_violations
		`CREATE TABLE IF NOT EXISTS public.guardrail_violations (
			id              uuid NOT NULL DEFAULT gen_random_uuid(),
			tenant_id       uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
			request_id      uuid,
			direction       text NOT NULL CHECK (direction IN ('input', 'output')),
			rule_id         text NOT NULL,
			severity        text NOT NULL DEFAULT 'warn' CHECK (severity IN ('info', 'warn', 'critical')),
			payload_excerpt text NOT NULL,
			metadata        jsonb,
			created_at      timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (id, tenant_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_guardrail_violations_tenant_created ON public.guardrail_violations (tenant_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_guardrail_violations_rule ON public.guardrail_violations (tenant_id, rule_id)`,
		`CREATE INDEX IF NOT EXISTS idx_guardrail_violations_direction ON public.guardrail_violations (tenant_id, direction)`,
		`CREATE INDEX IF NOT EXISTS idx_guardrail_violations_request ON public.guardrail_violations (tenant_id, request_id)`,
		`ALTER TABLE public.guardrail_violations ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE public.guardrail_violations FORCE ROW LEVEL SECURITY`,
		`CREATE POLICY guardrail_violations_tenant_isolation ON public.guardrail_violations 
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid) 
			WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid)`,

		// 0013_seed - default roles
		`INSERT INTO public.roles (id, name, description) VALUES
			('00000000-0000-0000-0000-000000000001', 'admin', 'Full administrative access within tenant'),
			('00000000-0000-0000-0000-000000000002', 'operator', 'Standard operational access within tenant'),
			('00000000-0000-0000-0000-000000000003', 'viewer', 'Read-only access within tenant')
		ON CONFLICT (id) DO NOTHING`,
	}

	for _, migration := range migrations {
		_, err := dbPool.Exec(ctx, migration)
		if err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}

	return nil
}

// Teardown cleans up the test containers
func (tc *TestContainer) Teardown(t *testing.T) {
	_ = tc.PGContainer.Terminate(tc.Ctx)
	_ = tc.RedisContainer.Terminate(tc.Ctx)
	tc.DBPool.Close()
	tc.RedisClient.Close()
	tc.Cancel()
}

// SetupTestData creates test tenant, user, and role assignment
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
		ON CONFLICT (id, tenant_id) DO NOTHING
	`, userID, tenantID)
	if err != nil {
		return err
	}

	// Roles are global catalog - use predefined role IDs from seed migration
	// roleID should be one of: admin, operator, viewer (see 0013_seed)
	// Assign role to user in this tenant via user_roles
	_, err = dbPool.Exec(ctx, `
		INSERT INTO public.user_roles (tenant_id, user_id, role_id) 
		VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id, user_id, role_id) DO NOTHING
	`, tenantID, userID, roleID)
	if err != nil {
		return err
	}

	// Insert high quota for testing (avoid rate limiting in tests)
	_, err = dbPool.Exec(ctx, `
		INSERT INTO public.quotas (tenant_id, scope, scope_id, requests_per_min, tokens_per_min, tool_execs_per_min)
		VALUES ($1, 'tenant', '00000000-0000-0000-0000-000000000000', 10000, 1000000, 1000)
		ON CONFLICT (tenant_id, scope, scope_id) DO UPDATE SET
			requests_per_min = EXCLUDED.requests_per_min,
			tokens_per_min = EXCLUDED.tokens_per_min,
			tool_execs_per_min = EXCLUDED.tool_execs_per_min
	`, tenantID)
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
	// Use predefined admin role ID from seed migration (0013_seed)
	roleID := domain.MustParseUUID("00000000-0000-0000-0000-000000000001") // admin role

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
	// Use background context to avoid test runner deadline propagation
	req := httptest.NewRequest(method, path, nil)
	req = req.WithContext(context.Background())
	if body != "" {
		req = httptest.NewRequest(method, path, nil)
		req = req.WithContext(context.Background())
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