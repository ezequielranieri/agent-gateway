package integration

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // register "pgx/v5" driver for wait.ForSQL
	"github.com/moby/moby/api/types/network"
	"github.com/pressly/goose/v3"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	redisModule "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/jwt"
	pgadapter "github.com/ezequielranieri/agent-gateway/internal/adapter/postgres"
	"github.com/ezequielranieri/agent-gateway/internal/adapter/pricing"
	"github.com/ezequielranieri/agent-gateway/internal/adapter/provider/mock"
	redisadapter "github.com/ezequielranieri/agent-gateway/internal/adapter/redis"
	toolMock "github.com/ezequielranieri/agent-gateway/internal/adapter/tool/mock"
	"github.com/ezequielranieri/agent-gateway/internal/api"
	"github.com/ezequielranieri/agent-gateway/internal/api/handlers"
	"github.com/ezequielranieri/agent-gateway/internal/config"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
	"github.com/ezequielranieri/agent-gateway/internal/middleware"
	"github.com/ezequielranieri/agent-gateway/internal/usecase/auth"
	"github.com/ezequielranieri/agent-gateway/internal/usecase/chat"
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

// checkTestRole verifies the current database role is not superuser/bypassrls
func checkTestRole(t *testing.T, dbPool *pgxpool.Pool) {
	ctx := context.Background()
	var privileged bool
	err := dbPool.QueryRow(ctx,
		`SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&privileged)
	if err != nil || privileged {
		t.Fatalf("test role must be NOSUPERUSER NOBYPASSRLS (err=%v, privileged=%v)", err, privileged)
	}
}

// SetupTestContainers starts PostgreSQL and Redis containers and applies migrations
func SetupTestContainers(t *testing.T) *TestContainer {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)

	// Start PostgreSQL container.
	// Use wait.ForSQL (SELECT 1 + retries) instead of a log-message wait:
	// the "ready to accept connections" log line can appear before Postgres
	// actually accepts connections, causing SQLSTATE 57P03 "database system is
	// starting up" races on slow runners.
	pgContainer, err := pgmodule.Run(ctx,
		"postgres:16-alpine",
		pgmodule.WithDatabase("agent_gateway"),
		pgmodule.WithUsername("postgres"),
		pgmodule.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(wait.ForSQL("5432/tcp", "pgx/v5",
			func(host string, port network.Port) string {
				return fmt.Sprintf("postgres://postgres:postgres@%s:%s/agent_gateway?sslmode=disable", host, port.Port())
			},
		)),
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

	// Connect to PostgreSQL as superuser (postgres) for setup
	adminPool, err := pgxpool.New(ctx, pgDSN)
	require.NoError(t, err)

	// Create gateway role (mirrors CI step "Create test role and set test DSN")
	// This role is referenced by migration 0014_pricing_tables.sql GRANT statements
	_, err = adminPool.Exec(ctx, `
		CREATE ROLE gateway WITH LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD 'gateway';
		GRANT USAGE ON SCHEMA public TO gateway;
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO gateway;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO gateway;
	`)
	require.NoError(t, err, "Failed to create gateway role")

	// Run goose migrations to create schema and goose_db_version table
	migrationsPath, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	require.NoError(t, err)

	// goose.UpContext requires *sql.DB, not DSN string
	sqlDB, err := sql.Open("pgx", pgDSN)
	require.NoError(t, err)
	defer sqlDB.Close()

	err = goose.UpContext(ctx, sqlDB, migrationsPath)
	require.NoError(t, err, "goose migrations failed")

	// Create test role (gateway_test) matching CI - NOSUPERUSER NOBYPASSRLS
	// Use same password as CI workflow for consistency
	testRolePassword := "testrolepass"
	_, err = adminPool.Exec(ctx, `
		CREATE ROLE gateway_test WITH LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD 'testrolepass';
		GRANT USAGE ON SCHEMA public TO gateway_test;
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO gateway_test;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO gateway_test;
	`)
	require.NoError(t, err, "Failed to create gateway_test role")

	// Close admin pool and connect as test role for actual tests
	adminPool.Close()

	// Build DSN for test role
	testDSN := strings.Replace(pgDSN, "postgres:postgres", "gateway_test:testrolepass", 1)
	dbPool, err := pgxpool.New(ctx, testDSN)
	require.NoError(t, err)

	// Increase pool size for concurrent tests
	dbPool.Config().MaxConns = 50

	// Verify test role is NOSUPERUSER NOBYPASSRLS
	checkTestRole(t, dbPool)

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
		ReviewRepo: reviewRepo,
		AuditRepo:  auditRepo,
		DefaultTTL: 24 * time.Hour,
		Logger:     logger,
	})

	// Initialize handlers
	authHandlers := handlers.NewAuthHandlers(authUC, nil, logger)

	// Initialize mock chat usecase for tests
	mockProvider := mock.NewProvider(
		mock.WithName("test-mock"),
		mock.WithModels([]string{"gpt-4o-mini", "gpt-4", "test-model"}),
		mock.WithEnabled(true),
	)
	mockProvider.SetFixedLatency(10 * time.Millisecond)

	// Create test pricing service
	testPriceTable := &pricing.PriceTable{
		Version:  "test",
		Provider: "mock",
		Prices: []pricing.ModelPriceEntry{
			{Model: "gpt-4o-mini", InputPricePer1k: 0.00015, OutputPricePer1k: 0.0006},
			{Model: "gpt-4", InputPricePer1k: 0.03, OutputPricePer1k: 0.06},
			{Model: "test-model", InputPricePer1k: 0.001, OutputPricePer1k: 0.002},
		},
		EffectiveDate: "2024-01-01",
		Description:   "Test pricing",
	}
	mockPricing := pricing.NewTestService(pricing.WithTable(testPriceTable))

	// Build registry with mock provider
	mockRegistry := chat.NewProviderRegistry(logger)
	mockRegistry.Register(model.ProviderConfig{
		Name:       "test-mock",
		Type:       model.ProviderTypeMock,
		Priority:   1,
		Enabled:    true,
		Models:     []string{"gpt-4o-mini", "gpt-4", "test-model"},
		Timeout:    30 * time.Second,
		MaxRetries: 2,
	}, mockProvider)

	mockRouter := chat.NewRouter(mockRegistry, logger)
	mockFallbackChain := chat.NewFallbackChain(mockRouter, mockPricing, model.RouterConfig{}, logger)

	// Create mock tool executor for tests
	mockToolExecutor := toolMock.NewMockExecutor(
		toolMock.WithSupportedTools("echo_tool", "send_email", "query_db"),
		toolMock.WithLatency(10*time.Millisecond),
	)

	mockChatUC := chat.NewChatUsecase(
		mockFallbackChain,
		mockPricing,
		mockRouter,
		mockToolExecutor,
		nil, // tool config (nil for tests)
		nil, // tool repo (nil for tests)
		chat.ChatUsecaseConfig{
			DefaultTimeout:     30 * time.Second,
			EnableCostTracking: true,
			MaxIterations:      5,
		},
		logger,
	)

	chatHandlers := handlers.NewChatHandlers(logger, mockChatUC, nil)
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

	// Initialize delegation middleware with real Redis budget decrementor
	delegationRepo := pgadapter.NewDelegationRepository(tc.DBPool)
	delegationBudgetDec := redisadapter.NewRedisBudgetDecrementor(tc.RedisClient, logger)
	delegationMW := middleware.NewDelegation(middleware.DelegationConfig{
		Repository: delegationRepo,
		BudgetDec:  delegationBudgetDec,
		MaxDepth:   5,
		MaxFanOut:  10,
		Logger:     logger,
		FailOpen:   true,
	})

	// Create router
	router := api.NewRouter(api.RouterConfig{
		Config:             &config.Config{RateLimit: config.RateLimitConfig{FailOpen: true}},
		Logger:             logger,
		AuthMW:             authMW,
		TenantMW:           tenantMW,
		DelegationMW:       delegationMW,
		RateLimitMW:        rateLimitMW,
		AuditMW:            auditMW,
		GuardrailsMW:       middleware.NewGuardrails(middleware.GuardrailsConfig{Checker: &noopGuardrailChecker{}, Logger: logger}),
		HITLMW:             middleware.NewHITL(middleware.HITLConfig{Logger: logger}),
		AuthHandlers:       authHandlers,
		ReviewHandlers:     reviewHandlers,
		ChatHandlers:       chatHandlers,
		AdminAuditHandlers: adminAuditHandlers,
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
