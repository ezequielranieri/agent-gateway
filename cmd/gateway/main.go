package main

import (
	"context"
	"crypto/rand"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/guardrail"
	extguardrail "github.com/ezequielranieri/agent-gateway/internal/adapter/guardrail/external"
	domainguardrail "github.com/ezequielranieri/agent-gateway/internal/domain/guardrail"
	"github.com/ezequielranieri/agent-gateway/internal/adapter/jwt"
	"github.com/ezequielranieri/agent-gateway/internal/adapter/pricing"
	"github.com/ezequielranieri/agent-gateway/internal/adapter/tool/mock"
	"github.com/ezequielranieri/agent-gateway/internal/adapter/postgres"
	redisadapter "github.com/ezequielranieri/agent-gateway/internal/adapter/redis"
	"github.com/ezequielranieri/agent-gateway/internal/api"
	"github.com/ezequielranieri/agent-gateway/internal/api/handlers"
	"github.com/ezequielranieri/agent-gateway/internal/config"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/middleware"
	"github.com/ezequielranieri/agent-gateway/internal/usecase/auth"
	"github.com/ezequielranieri/agent-gateway/internal/usecase/chat"
	"github.com/ezequielranieri/agent-gateway/internal/usecase/hitl"
	"github.com/ezequielranieri/agent-gateway/internal/usecase/quota"
	"github.com/ezequielranieri/agent-gateway/internal/usecase/role"
	"github.com/ezequielranieri/agent-gateway/internal/usecase/tenant"
	"github.com/ezequielranieri/agent-gateway/internal/usecase/user"
)

func main() {
	// Configure logger
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})

	// Load configuration
	cfg := config.MustLoad("configs/config.yaml")

	// Set log level based on environment
	if cfg.Server.Env == "production" {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	} else {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}

	logger := log.With().Str("service", "gateway").Logger()
	logger.Info().Str("env", cfg.Server.Env).Msg("Starting agent-gateway")

	// Initialize database pool
	dbPool, err := initDB(cfg.Database, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to initialize database")
	}
	defer dbPool.Close()

	// Initialize Redis client
	redisClient, err := initRedis(cfg.Redis, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to initialize Redis")
	}
	defer redisClient.Close()

	// Initialize repositories
	userRepo := postgres.NewUserRepository(dbPool)
	refreshRepo := postgres.NewRefreshTokenRepository(dbPool)
	quotaRepo := postgres.NewQuotaRepository(dbPool)
	auditRepo := postgres.NewAuditRepository(dbPool)
	reviewRepo := postgres.NewReviewRepository(dbPool)
	tenantRepo := postgres.NewTenantRepository(dbPool)
	roleRepo := postgres.NewRoleRepository(dbPool)

	// Initialize JWT adapter
	// Generate a random signing key if not provided
	signingKey := []byte(cfg.JWT.Secret)
	if len(signingKey) == 0 {
		signingKey = make([]byte, 32)
		rand.Read(signingKey)
	}
	keyStore := jwt.NewKeyStore("v1", signingKey, map[string][]byte{
		"v1": signingKey,
	})
	jwtService := jwt.NewAuthService(keyStore, cfg.JWT.Issuer, cfg.JWT.Audience)

	// Initialize auth use case
	authUC := auth.NewAuthUseCase(userRepo, refreshRepo, jwtService)
	superAdminLoginUC := auth.NewSuperAdminLoginUseCase(dbPool, jwtService)

	// Initialize tenant use case
	tenantUC := tenant.NewUseCase(tenantRepo)

	// Initialize user use case
	userUC := user.NewUseCase(userRepo)

	// Initialize role use case
	roleUC := role.NewUseCase(roleRepo)

	// Initialize quota use case
	quotaUC := quota.NewUseCase(quotaRepo)

	// Initialize HITL use case
	hitlUC := hitl.NewHITLUseCase(hitl.HITLConfig{
		ReviewRepo:   reviewRepo,
		AuditRepo:    auditRepo,
		DefaultTTL:   cfg.HITL.DefaultTTL,
		Logger:       logger,
	})

	// Initialize auth handlers
	authHandlers := handlers.NewAuthHandlers(authUC, superAdminLoginUC, logger)

	// Initialize admin handlers
	adminTenantsHandler := handlers.NewAdminTenantsHandler(tenantUC, logger)
	adminUsersHandler := handlers.NewAdminUsersHandler(userUC, logger)
	adminRolesHandler := handlers.NewAdminRolesHandler(roleUC, logger)
	adminQuotasHandler := handlers.NewAdminQuotasHandler(quotaUC, logger)

	// Initialize admin audit handlers
	adminAuditHandlers := handlers.NewAdminAuditHandlers(auditRepo, logger)

	// Initialize review handlers
	reviewHandlers := handlers.NewReviewHandlers(hitlUC, reviewRepo, string(signingKey), logger)

	// Initialize pricing service
	pricingService, err := pricing.NewServiceFromDomainConfig(cfg.Pricing)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to initialize pricing service")
	}

	// Initialize tool executor (use mock for now, can switch to wazero)
	toolExecutor := mock.NewMockExecutor(
		mock.WithSupportedTools("echo_tool", "send_email", "query_db"),
		mock.WithLatency(10*time.Millisecond),
	)

	// Initialize chat usecase
	chatUC, err := chat.BuildChatUsecaseFromConfig(
		context.Background(),
		cfg.Router,
		&cfg.Tool,
		toolExecutor,
		pricingService,
		logger,
	)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to initialize chat usecase")
	}

	// Initialize chat handlers
	chatHandlers := handlers.NewChatHandlers(logger, chatUC)

	// Initialize middleware with real JWT service
	authMW := middleware.NewAuth(middleware.AuthConfig{
		JWTService: jwtService,
		Logger:     logger,
	})

	tenantMW := middleware.NewTenant(middleware.TenantConfig{
		Pool:   dbPool,
		Logger: logger,
	})

	// Initialize Redis rate limiter
	redisRateLimiter := redisadapter.NewRedisRateLimiter(redisClient, logger, cfg.RateLimit.FailOpen)
	redisQuotaResolver := redisadapter.NewRedisQuotaResolver(quotaRepo, logger)

	rateLimitMW := middleware.NewRateLimit(middleware.RateLimitConfig{
		Limiter:       redisRateLimiter,
		QuotaResolver: redisQuotaResolver,
		FailOpen:      cfg.RateLimit.FailOpen,
		Logger:        logger,
	})

	auditMW := middleware.NewAudit(middleware.AuditConfig{
		Store:  auditRepo,
		Logger: logger,
	})

	// Initialize LocalGuardrail
	localGuardrail := guardrail.NewLocalGuardrail(cfg.Guardrails, logger)

	// Initialize External Classifier Factory
	extFactory := extguardrail.NewClassifierFactory(logger)

	// Create external classifier if configured
	var externalClassifier domainguardrail.ExternalClassifier
	if cfg.Guardrails.ExternalClassifier != nil && cfg.Guardrails.ExternalClassifier.Enabled {
		// Convert map[string]string to map[string]interface{}
		extConfig := make(map[string]interface{}, len(cfg.Guardrails.ExternalClassifier.Config))
		for k, v := range cfg.Guardrails.ExternalClassifier.Config {
			extConfig[k] = v
		}

		extCfg := extguardrail.ClassifierConfig{
			Type:           cfg.Guardrails.ExternalClassifier.Type,
			Config:         extConfig,
			Thresholds:     cfg.Guardrails.ExternalClassifier.Thresholds,
			Retry:          cfg.Guardrails.ExternalClassifier.Retry,
			CircuitBreaker: cfg.Guardrails.ExternalClassifier.CircuitBreaker,
			Timeout:        cfg.Guardrails.ExternalClassifier.Timeout,
		}
		ext, err := extFactory.CreateClassifier(context.Background(), extCfg, logger)
		if err != nil {
			logger.Warn().Err(err).Str("type", cfg.Guardrails.ExternalClassifier.Type).Msg("Failed to create external classifier, falling back to local only")
		} else {
			externalClassifier = ext
			logger.Info().Str("type", cfg.Guardrails.ExternalClassifier.Type).Msg("External classifier initialized")
		}
	}

	// Create Composite Guardrail (local + external)
	compositeConfig := domainguardrail.CompositeConfig{
		Mode:               cfg.Guardrails.Composite.Mode,
		FailBehavior:       cfg.Guardrails.Composite.FailBehavior,
		MergeLogic:         cfg.Guardrails.Composite.MergeLogic,
		ParallelBudgetMs:   cfg.Guardrails.Composite.ParallelBudgetMs,
		SendContentExternal: cfg.Guardrails.Composite.SendContentExternal,
		Thresholds:         cfg.Guardrails.Composite.Thresholds,
	}
	compositeGuardrail := guardrail.NewCompositeGuardrail(localGuardrail, externalClassifier, compositeConfig, logger)

	// Wrap with adapter to implement domain.Guardrail interface
	compositeAdapter := guardrail.NewCompositeGuardrailAdapter(compositeGuardrail)

	guardrailsMW := middleware.NewGuardrails(middleware.GuardrailsConfig{
		Checker:   compositeAdapter,
		AuditRepo: auditRepo,
		Logger:    logger,
	})

	hitlMW := middleware.NewHITL(middleware.HITLConfig{
		Logger: logger,
	})

	// Create router with auth handlers
	router := api.NewRouter(api.RouterConfig{
		Config:                 cfg,
		Logger:                 logger,
		AuthMW:                 authMW,
		TenantMW:               tenantMW,
		RateLimitMW:            rateLimitMW,
		AuditMW:                auditMW,
		GuardrailsMW:           guardrailsMW,
		HITLMW:                 hitlMW,
		AuthHandlers:           authHandlers,
		ReviewHandlers:         reviewHandlers,
		ChatHandlers:           chatHandlers,
		AdminAuditHandlers:     adminAuditHandlers,
		AdminTenantsHandler:    adminTenantsHandler,
		AdminUsersHandler:      adminUsersHandler,
		AdminRolesHandler:      adminRolesHandler,
		AdminQuotasHandler:     adminQuotasHandler,
	})

	// Create HTTP server
	server := &http.Server{
		Addr:         cfg.Server.Addr,
		Handler:      router,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	// Start server in a goroutine
	go func() {
		logger.Info().Str("addr", cfg.Server.Addr).Msg("Starting HTTP server")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal().Err(err).Msg("HTTP server failed")
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info().Msg("Shutting down server...")

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Error().Err(err).Msg("Server forced to shutdown")
	}

	logger.Info().Msg("Server exited")
}

func initDB(cfg config.DatabaseConfig, logger zerolog.Logger) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, err
	}

	poolConfig.MaxConns = int32(cfg.MaxOpenConns)
	poolConfig.MinConns = int32(cfg.MaxIdleConns)
	poolConfig.MaxConnLifetime = cfg.ConnMaxLifetime
	poolConfig.MaxConnIdleTime = cfg.ConnMaxIdleTime

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ConnectTimeout)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, err
	}

	// Test connection
	if err := pool.Ping(ctx); err != nil {
		return nil, err
	}

	logger.Info().Msg("Database connected")
	return pool, nil
}

func initRedis(cfg config.RedisConfig, logger zerolog.Logger) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		MinIdleConns: cfg.MinIdleConns,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		PoolTimeout:  cfg.PoolTimeout,
	})

	ctx, cancel := context.WithTimeout(context.Background(), cfg.DialTimeout)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, err
	}

	logger.Info().Msg("Redis connected")
	return client, nil
}

// No-op implementations for Phase 0a (to be replaced in later phases)

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