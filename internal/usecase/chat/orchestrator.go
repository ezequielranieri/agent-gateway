package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
	"github.com/ezequielranieri/agent-gateway/internal/domain/tool"
	"github.com/rs/zerolog"
)

// ChatUsecaseConfig holds configuration for the chat usecase
type ChatUsecaseConfig struct {
	DefaultTimeout    time.Duration
	EnableCostTracking bool
	MaxIterations     int
	ToolExecutor      tool.ToolExecutor
	ToolConfig        *tool.ToolConfig
}

// ChatUsecase orchestrates the chat completion flow: pre-estimate -> route -> post-actual
type ChatUsecase struct {
	fallbackChain   *FallbackChain
	pricing         model.PricingService
	router          *Router
	toolExecutor    tool.ToolExecutor
	toolConfig      *tool.ToolConfig
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
	toolExecutor tool.ToolExecutor,
	toolConfig *tool.ToolConfig,
	config ChatUsecaseConfig,
	logger zerolog.Logger,
) *ChatUsecase {
	if config.DefaultTimeout <= 0 {
		config.DefaultTimeout = 30 * time.Second
	}
	if config.MaxIterations <= 0 {
		config.MaxIterations = 5
	}
	
	return &ChatUsecase{
		fallbackChain:   fallbackChain,
		pricing:         pricing,
		router:          router,
		toolExecutor:    toolExecutor,
		toolConfig:      toolConfig,
		config:          config,
		logger:          logger.With().Str("component", "chat_usecase").Logger(),
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
	
	// Execute with fallback chain (initial call)
	result, err := uc.fallbackChain.ExecuteWithFallback(ctx, domainReq)
	if err != nil {
		uc.logger.Error().
			Err(err).
			Dur("latency", time.Since(startTime)).
			Msg("Chat completion failed")
		return ChatResponse{}, err
	}
	
	// Execute tool loop if there are tool calls
	finalResult, err := uc.executeToolLoop(ctx, domainReq, result)
	if err != nil {
		uc.logger.Error().
			Err(err).
			Dur("latency", time.Since(startTime)).
			Msg("Tool loop execution failed")
		return ChatResponse{}, err
	}
	
	// Post-actual cost calculation
	actualCost, actualVersion, err := uc.calculateActualCost(ctx, finalResult.Completion)
	if err != nil {
		uc.logger.Warn().Err(err).Msg("Actual cost calculation failed")
	}
	
	totalLatency := time.Since(startTime)
	
	response := ChatResponse{
		Completion:   finalResult.Completion,
		CostUSD:      actualCost,
		CostVersion:  actualVersion,
		Provider:     finalResult.Provider,
		LatencyMs:    totalLatency.Milliseconds(),
		Retried:      finalResult.Retried,
	}
	
	uc.logger.Info().
		Str("provider", finalResult.Provider).
		Str("model", finalResult.Completion.Model).
		Int64("latency_ms", totalLatency.Milliseconds()).
		Float64("cost_usd", actualCost).
		Str("cost_version", actualVersion).
		Bool("retried", finalResult.Retried).
		Int("attempt", finalResult.Attempt).
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

// executeToolLoop executes tool calls in a bounded loop
func (uc *ChatUsecase) executeToolLoop(
	ctx context.Context,
	req model.ChatRequest,
	result FallbackResult,
) (FallbackResult, error) {
	if uc.toolExecutor == nil || uc.toolConfig == nil {
		uc.logger.Debug().Msg("No tool executor configured, skipping tool loop")
		return result, nil
	}

	maxIterations := uc.config.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 5
	}

	currentResult := result
	iterations := 0

	for iterations < maxIterations {
		// Check for tool calls in the response
		if len(currentResult.Completion.Response.Choices) == 0 {
			break
		}

		choice := currentResult.Completion.Response.Choices[0]
		toolCalls := choice.Message.ToolCalls
		if len(toolCalls) == 0 {
			break // No tool calls, we're done
		}

		iterations++
		uc.logger.Info().
			Int("iteration", iterations).
			Int("tool_calls", len(toolCalls)).
			Msg("Executing tool loop iteration")

		// Execute each tool call
		var toolResults []tool.ToolResult
		for _, tc := range toolCalls {
			// Check HITL gate
			if uc.toolRequiresApproval(tc.Function.Name) {
approved, err := uc.requestHITLApproval(ctx, tc)
				if err != nil || !approved {
					toolResults = append(toolResults, tool.ToolResult{
						CallID: tc.ID,
						Error:  fmt.Sprintf("HITL approval failed: %v", err),
					})
					continue
				}
			}

// Execute tool
		call := tool.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: parseArguments(tc.Function.Arguments),
		}

		var toolResult tool.ToolResult
		var execErr error
		toolResult, execErr = uc.toolExecutor.Execute(ctx, call)
		if execErr != nil {
			uc.logger.Error().
				Err(execErr).
				Str("tool", tc.Function.Name).
				Msg("Tool execution failed")
			toolResult = tool.ToolResult{
				CallID:   call.ID,
				Error:    execErr.Error(),
				Duration: 0,
			}
		}

		// Record per-step: cost, audit, rate-limit
		uc.recordToolStep(ctx, toolResult, tc)

		toolResults = append(toolResults, toolResult)
	}

	// Feed results back and make next call
	var feedErr error
	currentResult, feedErr = uc.feedToolResultsAndRecall(ctx, req, currentResult, toolResults)
	if feedErr != nil {
		return FallbackResult{}, fmt.Errorf("feed tool results: %w", feedErr)
	}
}

	if iterations >= maxIterations {
		uc.logger.Warn().
			Int("max_iterations", maxIterations).
			Msg("Max iterations reached")
		// Add finish reason to last completion
		if len(currentResult.Completion.Response.Choices) > 0 {
			currentResult.Completion.Response.Choices[0].FinishReason = "max_iterations"
		}
	}

	return currentResult, nil
}

// toolRequiresApproval checks if a tool requires HITL approval
func (uc *ChatUsecase) toolRequiresApproval(toolName string) bool {
	if uc.toolConfig == nil {
		return false
	}
	for _, tc := range uc.toolConfig.Tools {
		if tc.Name == toolName {
			return tc.RequiresApproval
		}
	}
	return false
}

// requestHITLApproval requests human approval for a tool call
func (uc *ChatUsecase) requestHITLApproval(ctx context.Context, tc model.ToolCall) (bool, error) {
	uc.logger.Info().
		Str("tool", tc.Function.Name).
		Msg("Tool requires HITL approval")

	// For now, return false to indicate not approved
	// In a real implementation, this would create a review request and wait for approval
	return false, fmt.Errorf("HITL approval required for %s", tc.Function.Name)
}

// recordToolStep records cost, audit, and rate-limit for a tool step
func (uc *ChatUsecase) recordToolStep(ctx context.Context, result tool.ToolResult, tc model.ToolCall) {
	// Record rate-limit ToolExecs (1 per tool call)
	// This would be called on the rate limiter
	uc.logger.Debug().
		Str("tool", tc.Function.Name).
		Str("call_id", tc.ID).
		Msg("Recording tool step")

	// TODO: Integrate with rate limiter, audit, and pricing
	_ = result
	_ = tc
}

// feedToolResultsAndRecall feeds tool results back to the model and makes next call
func (uc *ChatUsecase) feedToolResultsAndRecall(
	ctx context.Context,
	originalReq model.ChatRequest,
	currentResult FallbackResult,
	toolResults []tool.ToolResult,
) (FallbackResult, error) {
	// Build new messages with tool results
	messages := make([]model.Message, len(originalReq.Messages))
	copy(messages, originalReq.Messages)

	for _, tr := range toolResults {
		messages = append(messages, model.Message{
			Role:       "tool",
			Content:    fmt.Sprintf("%v", tr.Output),
			ToolCallID: tr.CallID,
			Name:       tr.CallID, // Using call ID as name for now
		})
	}

	// Create new request with tool results
	newReq := model.ChatRequest{
		Model:       originalReq.Model,
		Messages:    messages,
		Temperature: originalReq.Temperature,
		MaxTokens:   originalReq.MaxTokens,
		Stream:      originalReq.Stream,
		Tools:       originalReq.Tools,
		ToolChoice:  originalReq.ToolChoice,
		User:        originalReq.User,
	}

	// Execute with fallback chain
	return uc.fallbackChain.ExecuteWithFallback(ctx, newReq)
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
	toolCfg *tool.ToolConfig,
	toolExecutor tool.ToolExecutor,
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
		toolExecutor,
		toolCfg,
		ChatUsecaseConfig{
			DefaultTimeout:    cfg.DefaultTimeout,
			EnableCostTracking: true,
			MaxIterations:     toolCfg.MaxIterations,
			ToolExecutor:      toolExecutor,
			ToolConfig:        toolCfg,
		},
		logger,
	)
	
	// Start periodic health checks
	registry.StartPeriodicHealthChecks(ctx, 30*time.Second)
	
	return usecase, nil
}

// parseArguments parses a JSON string into a map
func parseArguments(s string) map[string]any {
	if s == "" {
		return nil
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		return nil
	}
	return result
}