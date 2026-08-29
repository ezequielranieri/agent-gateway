package chat

import (
	"context"
	"fmt"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
	"github.com/rs/zerolog"
)

// ChatUsecaseConfig holds configuration for the chat usecase
type ChatUsecaseConfig struct {
	DefaultTimeout    time.Duration
	EnableCostTracking bool
}

// ChatUsecase orchestrates the chat completion flow: pre-estimate -> route -> post-actual
type ChatUsecase struct {
	fallbackChain   *FallbackChain
	pricing         model.PricingService
	router          *Router
	config          ChatUsecaseConfig
	logger          zerolog.Logger
}

// ChatRequest is the usecase-level chat request
type ChatRequest struct {
	Model       string
	Messages    []model.Message
	Temperature *float64
	MaxTokens   *int
	Stream      bool
	Tools       []model.Tool
	ToolChoice  any
	User        string
}

// ChatResponse is the usecase-level chat response
type ChatResponse struct {
	Completion model.Completion
	CostUSD    float64
	CostVersion string
	Provider    string
	LatencyMs   int64
	Retried     bool
}

// NewChatUsecase creates a new chat usecase
func NewChatUsecase(
	fallbackChain *FallbackChain,
	pricing model.PricingService,
	router *Router,
	config ChatUsecaseConfig,
	logger zerolog.Logger,
) *ChatUsecase {
	if config.DefaultTimeout <= 0 {
		config.DefaultTimeout = 30 * time.Second
	}
	
	return &ChatUsecase{
		fallbackChain:    fallbackChain,
		pricing:          pricing,
		router:           router,
		config:           config,
		logger:           logger.With().Str("component", "chat_usecase").Logger(),
	}
}

// Complete executes a chat completion with full orchestration
func (uc *ChatUsecase) Complete(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	startTime := time.Now()
	
	uc.logger.Info().
		Str("model", req.Model).
		Int("message_count", len(req.Messages)).
		Bool("stream", req.Stream).
		Msg("Starting chat completion")
	
	// Convert to domain request
	domainReq := model.ChatRequest{
		Model:            req.Model,
		Messages:         req.Messages,
		Temperature:      req.Temperature,
		MaxTokens:        req.MaxTokens,
		Stream:           req.Stream,
		Tools:            req.Tools,
		ToolChoice:       req.ToolChoice,
		User:             req.User,
	}
	
	// Pre-estimate cost
	preEstimateCost, preEstimateVersion, err := uc.estimateCost(ctx, req)
	if err != nil {
		uc.logger.Warn().Err(err).Msg("Pre-estimate cost failed")
	}
	uc.logger.Debug().
		Float64("estimated_cost_usd", preEstimateCost).
		Str("pricing_version", preEstimateVersion).
		Msg("Pre-estimate cost")
	
	// Execute with fallback chain
	result, err := uc.fallbackChain.ExecuteWithFallback(ctx, domainReq)
	if err != nil {
		uc.logger.Error().
			Err(err).
			Dur("latency", time.Since(startTime)).
			Msg("Chat completion failed")
		return ChatResponse{}, err
	}
	
	// Post-actual cost calculation
	actualCost, actualVersion, err := uc.calculateActualCost(ctx, result.Completion)
	if err != nil {
		uc.logger.Warn().Err(err).Msg("Actual cost calculation failed")
	}
	
	totalLatency := time.Since(startTime)
	
	response := ChatResponse{
		Completion:   result.Completion,
		CostUSD:      actualCost,
		CostVersion:  actualVersion,
		Provider:     result.Provider,
		LatencyMs:    totalLatency.Milliseconds(),
		Retried:      result.Retried,
	}
	
	uc.logger.Info().
		Str("provider", result.Provider).
		Str("model", result.Completion.Model).
		Int64("latency_ms", totalLatency.Milliseconds()).
		Float64("cost_usd", actualCost).
		Str("cost_version", actualVersion).
		Bool("retried", result.Retried).
		Int("attempt", result.Attempt).
		Msg("Chat completion successful")
	
	return response, nil
}

// estimateCost estimates cost before execution
func (uc *ChatUsecase) estimateCost(ctx context.Context, req ChatRequest) (float64, string, error) {
	promptTokens := estimatePromptTokens(req.Messages)
	maxTokens := 1000
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}
	
	return uc.pricing.GetCost(ctx, req.Model, promptTokens, maxTokens)
}

// calculateActualCost calculates actual cost after execution
func (uc *ChatUsecase) calculateActualCost(ctx context.Context, completion model.Completion) (float64, string, error) {
	usage := completion.Response.Usage
	return uc.pricing.GetCost(ctx, completion.Model, usage.PromptTokens, usage.CompletionTokens)
}

// GetRouter returns the router for external access
func (uc *ChatUsecase) GetRouter() *Router {
	return uc.router
}

// GetFallbackChain returns the fallback chain for external access
func (uc *ChatUsecase) GetFallbackChain() *FallbackChain {
	return uc.fallbackChain
}

// HealthCheck checks the health of the chat usecase
func (uc *ChatUsecase) HealthCheck(ctx context.Context) error {
	// Check if any providers are healthy
	providers := uc.router.GetRegistry().GetHealthyProviders(ctx)
	if len(providers) == 0 {
		return model.ErrNoHealthyProvider
	}
	return nil
}

// GetProviderStats returns provider statistics
func (uc *ChatUsecase) GetProviderStats() map[string]ProviderStats {
	return uc.router.GetRegistry().GetProviderStats()
}

// BuildChatUsecaseFromConfig creates a fully configured ChatUsecase from config
func BuildChatUsecaseFromConfig(
	ctx context.Context,
	cfg model.RouterConfig,
	pricing model.PricingService,
	logger zerolog.Logger,
) (*ChatUsecase, error) {
	// Build registry
	registry, err := BuildRegistryFromConfig(ctx, cfg, logger)
	if err != nil {
		return nil, fmt.Errorf("build registry: %w", err)
	}
	
	// Create router
	router := NewRouter(registry, logger)
	
	// Create fallback chain
	fallbackChain := NewFallbackChain(router, pricing, cfg, logger)
	
	// Create usecase
	usecase := NewChatUsecase(
		fallbackChain,
		pricing,
		router,
		ChatUsecaseConfig{
			DefaultTimeout:    cfg.DefaultTimeout,
			EnableCostTracking: true,
		},
		logger,
	)
	
	// Start periodic health checks
	registry.StartPeriodicHealthChecks(ctx, 30*time.Second)
	
	return usecase, nil
}