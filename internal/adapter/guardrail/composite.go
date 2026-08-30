package guardrail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	domainguardrail "github.com/ezequielranieri/agent-gateway/internal/domain/guardrail"
	domainmodel "github.com/ezequielranieri/agent-gateway/internal/domain/model"
	domaintypes "github.com/ezequielranieri/agent-gateway/internal/domain/tool"
	domaintool "github.com/ezequielranieri/agent-gateway/internal/domain/tool"
	"github.com/ezequielranieri/agent-gateway/internal/usecase/chat"
	"github.com/rs/zerolog"
)

var (
	ErrNoClassifiersConfigured = errors.New("no classifiers configured")
	ErrExternalClassifierFailed = errors.New("external classifier failed")
)

type CompositeGuardrail struct {
	local       domainguardrail.ExternalClassifier
	external    domainguardrail.ExternalClassifier
	config      domainguardrail.CompositeConfig
	logger      zerolog.Logger
	mu          sync.RWMutex
	fallbackChain *chat.FallbackChain
	pricing     domainmodel.PricingService
	toolExecutor domaintool.ToolExecutor
	toolConfig   *domaintypes.ToolConfig
}

func NewCompositeGuardrail(local, ext domainguardrail.ExternalClassifier, config domainguardrail.CompositeConfig, logger zerolog.Logger) *CompositeGuardrail {
	if config.Mode == "" {
		config.Mode = "sequential"
	}
	if config.FailBehavior == "" {
		config.FailBehavior = "fallback_local"
	}
	if config.MergeLogic == "" {
		config.MergeLogic = "any_violation"
	}
	if config.ParallelBudgetMs == 0 {
		config.ParallelBudgetMs = 500
	}
	if config.Thresholds == nil {
		config.Thresholds = map[string]float64{
			"sexual":      0.7,
			"hate":        0.8,
			"violence":    0.7,
			"self-harm":   0.9,
			"harassment":  0.7,
		}
	}

	return &CompositeGuardrail{
		local:       local,
		external:    ext,
		config:      config,
		logger:      logger.With().Str("component", "composite_guardrail").Logger(),
	}
}

func (c *CompositeGuardrail) CheckInput(ctx context.Context, tenantID string, input string) (domainguardrail.ClassificationResult, error) {
	return c.classify(ctx, tenantID, input, true)
}

func (c *CompositeGuardrail) CheckOutput(ctx context.Context, tenantID string, output string) (domainguardrail.ClassificationResult, error) {
	return c.classify(ctx, tenantID, output, false)
}

func (c *CompositeGuardrail) classify(ctx context.Context, tenantID, text string, isInput bool) (domainguardrail.ClassificationResult, error) {
	startTime := time.Now()

	var localResult, extResult domainguardrail.ClassificationResult
	var localErr, extErr error
	localAvailable := false
	extAvailable := false

	// Run local classifier if available
	if c.local != nil {
		if isInput {
			localResult, localErr = c.local.ClassifyInput(ctx, text)
		} else {
			localResult, localErr = c.local.ClassifyOutput(ctx, text)
		}
		if localErr == nil {
			localAvailable = true
		} else {
			c.logger.Warn().Err(localErr).Msg("Local classifier failed")
		}
	}

	// Run external classifier if available and enabled
	if c.external != nil && c.config.SendContentExternal {
		if isInput {
			extResult, extErr = c.external.ClassifyInput(ctx, text)
		} else {
			extResult, extErr = c.external.ClassifyOutput(ctx, text)
		}
		if extErr == nil {
			extAvailable = true
		} else {
			c.logger.Warn().Err(extErr).Msg("External classifier failed")
		}
	}

	// Determine result based on availability and fail behavior
	var merged domainguardrail.ClassificationResult

	switch {
	case localAvailable && extAvailable:
		// Both classifiers succeeded
		merged = c.mergeResults(localResult, extResult)
		merged.Provider = "composite"

	case localAvailable && !extAvailable:
		// Local succeeded, external failed or not configured
		if c.external != nil && c.config.SendContentExternal && extErr != nil {
			// External failed - handle fail behavior
			switch c.config.FailBehavior {
			case "fail_open":
				// Allow - return non-violated result
				merged = domainguardrail.ClassificationResult{
					Violated:  false,
					Categories: []domainguardrail.CategoryResult{},
					Provider:  "composite",
				}
			case "fail_closed":
				// Block - return error
				return domainguardrail.ClassificationResult{}, domainguardrail.ErrExternalClassifierUnavailable
			case "fallback_local":
				fallthrough
			default:
				// Use local result
				merged = localResult
				merged.Provider = "composite"
			}
		} else {
			// External not configured or disabled
			merged = localResult
			merged.Provider = "composite"
		}

	case !localAvailable && extAvailable:
		// Only external succeeded
		merged = extResult
		merged.Provider = "composite"

	case !localAvailable && !extAvailable && c.external != nil && c.config.SendContentExternal && extErr != nil:
		// Local not available, external failed
		switch c.config.FailBehavior {
		case "fail_open":
			merged = domainguardrail.ClassificationResult{
				Violated:  false,
				Categories: []domainguardrail.CategoryResult{},
				Provider:  "composite",
			}
		case "fail_closed":
			return domainguardrail.ClassificationResult{}, domainguardrail.ErrExternalClassifierUnavailable
		case "fallback_local":
			fallthrough
		default:
			// No local to fallback to
			return domainguardrail.ClassificationResult{}, domainguardrail.ErrExternalClassifierUnavailable
		}

	default:
		// No classifiers available
		return domainguardrail.ClassificationResult{}, ErrNoClassifiersConfigured
	}

	merged.LatencyMs = time.Since(startTime).Milliseconds()

	c.logger.Info().
		Str("provider", merged.Provider).
		Int64("latency_ms", merged.LatencyMs).
		Bool("violated", merged.Violated).
		Msg("Classification complete")

	return merged, nil
}

func (c *CompositeGuardrail) estimateCost(ctx context.Context, req domainmodel.ChatRequest) (float64, string, error) {
	promptTokens := estimatePromptTokens(req.Messages)
	maxTokens := 1000
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	return c.pricing.GetCost(ctx, req.Model, promptTokens, maxTokens)
}

func (c *CompositeGuardrail) calculateActualCost(ctx context.Context, completion domainmodel.Completion) (float64, string, error) {
	usage := completion.Response.Usage
	return c.pricing.GetCost(ctx, completion.Model, usage.PromptTokens, usage.CompletionTokens)
}

func (c *CompositeGuardrail) executeToolLoop(
	ctx context.Context,
	req domainmodel.ChatRequest,
	result chat.FallbackResult,
) (chat.FallbackResult, error) {
	if c.toolExecutor == nil || c.toolConfig == nil {
		c.logger.Debug().Msg("No tool executor configured, skipping tool loop")
		return result, nil
	}

	maxIterations := 5 // Default, could be added to config later
	if maxIterations <= 0 {
		maxIterations = 5
	}

	currentResult := result
	iterations := 0

	for iterations < maxIterations {
		if len(currentResult.Completion.Response.Choices) == 0 {
			break
		}

		choice := currentResult.Completion.Response.Choices[0]
		toolCalls := choice.Message.ToolCalls
		if len(toolCalls) == 0 {
			break
		}

		iterations++
		c.logger.Info().
			Int("iteration", iterations).
			Int("tool_calls", len(toolCalls)).
			Msg("Executing tool loop iteration")

		var toolResults []domaintool.ToolResult
		for _, tc := range toolCalls {
			if c.toolRequiresApproval(tc.Function.Name) {
				approved, err := c.requestHITLApproval(ctx, tc)
				if err != nil || !approved {
					toolResults = append(toolResults, domaintool.ToolResult{
						CallID: tc.ID,
						Error:  fmt.Sprintf("HITL approval failed: %v", err),
					})
					continue
				}
			}

			call := domaintool.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: parseArguments(tc.Function.Arguments),
			}

			toolResult, err := c.toolExecutor.Execute(ctx, call)
			if err != nil {
				c.logger.Error().
					Err(err).
					Str("tool", tc.Function.Name).
					Msg("Tool execution failed")
				toolResult = domaintool.ToolResult{
					CallID:   call.ID,
					Error:    err.Error(),
					Duration: 0,
				}
			}

			c.recordToolStep(ctx, toolResult, call)

			toolResults = append(toolResults, toolResult)
		}

		var err error
		currentResult, err = c.feedToolResultsAndRecall(ctx, req, currentResult, toolResults)
		if err != nil {
			return chat.FallbackResult{}, fmt.Errorf("feed tool results: %w", err)
		}
	}

	if iterations >= maxIterations {
		c.logger.Warn().
			Int("max_iterations", maxIterations).
			Msg("Max iterations reached")
		if len(currentResult.Completion.Response.Choices) > 0 {
			currentResult.Completion.Response.Choices[0].FinishReason = "max_iterations"
		}
	}

	return currentResult, nil
}

func (c *CompositeGuardrail) toolRequiresApproval(toolName string) bool {
	if c.toolConfig == nil {
		return false
	}
	for _, tc := range c.toolConfig.Tools {
		if tc.Name == toolName {
			return tc.RequiresApproval
		}
	}
	return false
}

func (c *CompositeGuardrail) requestHITLApproval(ctx context.Context, tc domainmodel.ToolCall) (bool, error) {
	c.logger.Info().
		Str("tool", tc.Function.Name).
		Msg("Tool requires HITL approval")

	return false, fmt.Errorf("HITL approval required for %s", tc.Function.Name)
}

func (c *CompositeGuardrail) recordToolStep(ctx context.Context, result domaintool.ToolResult, tc domaintool.ToolCall) {
	c.logger.Debug().
		Str("tool", tc.Name).
		Str("call_id", tc.ID).
		Msg("Recording tool step")

	_ = result
	_ = tc
}

func (c *CompositeGuardrail) feedToolResultsAndRecall(
	ctx context.Context,
	originalReq domainmodel.ChatRequest,
	currentResult chat.FallbackResult,
	toolResults []domaintool.ToolResult,
) (chat.FallbackResult, error) {
	messages := make([]domainmodel.Message, len(originalReq.Messages))
	copy(messages, originalReq.Messages)

	for _, tr := range toolResults {
		messages = append(messages, domainmodel.Message{
			Role:       "tool",
			Content:    fmt.Sprintf("%v", tr.Output),
			ToolCallID: tr.CallID,
			Name:       tr.CallID,
		})
	}

	newReq := domainmodel.ChatRequest{
		Model:       originalReq.Model,
		Messages:    messages,
		Temperature: originalReq.Temperature,
		MaxTokens:   originalReq.MaxTokens,
		Stream:      originalReq.Stream,
		Tools:       originalReq.Tools,
		ToolChoice:  originalReq.ToolChoice,
		User:        originalReq.User,
	}

	return c.fallbackChain.ExecuteWithFallback(ctx, domainmodel.ChatRequest{
		Model:            newReq.Model,
		Messages:         newReq.Messages,
		Temperature:      newReq.Temperature,
		MaxTokens:        newReq.MaxTokens,
		Stream:           newReq.Stream,
		Tools:            newReq.Tools,
		ToolChoice:       newReq.ToolChoice,
		User:             newReq.User,
	})
}

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

func estimatePromptTokens(messages []domainmodel.Message) int {
	totalChars := 0
	for _, msg := range messages {
		totalChars += len(msg.Content)
		for _, tc := range msg.ToolCalls {
			totalChars += len(tc.Function.Name) + len(tc.Function.Arguments)
		}
	}
	tokens := totalChars / 4
	if tokens == 0 {
		tokens = 100
	}
	return tokens
}

func (c *CompositeGuardrail) mergeResults(local, ext domainguardrail.ClassificationResult) domainguardrail.ClassificationResult {
	merged := domainguardrail.ClassificationResult{
		Violated:  false,
		Categories: []domainguardrail.CategoryResult{},
	}

	type categoryInfo struct {
		category   string
		detections []domainguardrail.CategoryResult
		// Track which classifiers provided a result for this category
		hasLocal bool
		hasExt   bool
	}

	categoryInfos := make(map[string]*categoryInfo)

	// Process local categories
	for _, cat := range local.Categories {
		key := cat.Category
		info, exists := categoryInfos[key]
		if !exists {
			info = &categoryInfo{category: cat.Category}
			categoryInfos[key] = info
		}
		info.detections = append(info.detections, cat)
		info.hasLocal = true
	}

	// Process external categories
	for _, cat := range ext.Categories {
		key := cat.Category
		info, exists := categoryInfos[key]
		if !exists {
			info = &categoryInfo{category: cat.Category}
			categoryInfos[key] = info
		}
		info.detections = append(info.detections, cat)
		info.hasExt = true
	}

	// Determine if each classifier ran (has Provider set)
	localRan := local.Provider != ""
	extRan := ext.Provider != ""

	for _, info := range categoryInfos {
		detected := false
		var bestCat domainguardrail.CategoryResult

		switch c.config.MergeLogic {
		case "any_violation":
			for _, det := range info.detections {
				if det.Detected {
					detected = true
					if bestCat.Category == "" || det.Confidence > bestCat.Confidence {
						bestCat = det
					}
				}
			}
		case "all_violation":
			// For all_violation, ALL classifiers that ran must detect the violation
			// If a classifier ran but didn't include this category, it's treated as not detected
			allDetected := true
			for _, det := range info.detections {
				if det.Detected {
					if bestCat.Category == "" || det.Confidence > bestCat.Confidence {
						bestCat = det
					}
				} else {
					allDetected = false
				}
			}
			// Check if any classifier that ran didn't include this category
			if localRan && !info.hasLocal {
				allDetected = false
			}
			if extRan && !info.hasExt {
				allDetected = false
			}
			if allDetected {
				detected = true
			}
		case "weighted":
			// Weighted: use the detection with highest confidence
			// If a classifier didn't include the category, treat as confidence 0 (not detected)
			maxConfidence := -1.0
			for _, det := range info.detections {
				if det.Detected && det.Confidence > maxConfidence {
					maxConfidence = det.Confidence
					bestCat = det
				}
			}
			// If no detections were found but classifiers ran, check if we should consider non-detections
			// For weighted, we only care about positive detections with highest confidence
			if maxConfidence >= 0 {
				detected = true
			}
		default:
			for _, det := range info.detections {
				if det.Detected {
					if bestCat.Category == "" || det.Confidence > bestCat.Confidence {
						bestCat = det
					}
				}
			}
		}

		if detected {
			merged.Violated = true
			merged.Categories = append(merged.Categories, bestCat)
		}
	}

	// Add categories that weren't detected but were present in results
	for _, info := range categoryInfos {
		if len(info.detections) > 0 {
			found := false
			for _, c := range merged.Categories {
				if c.Category == info.category {
					found = true
					break
				}
			}
			if !found {
				bestCat := info.detections[0]
				for _, det := range info.detections {
					if det.Confidence > bestCat.Confidence {
						bestCat = det
					}
				}
				merged.Categories = append(merged.Categories, bestCat)
			}
		}
	}

	return merged
}

func (c *CompositeGuardrail) Name() string {
	return "composite"
}

func (c *CompositeGuardrail) HealthCheck(ctx context.Context) error {
	var errs []error

	if c.local != nil {
		if err := c.local.HealthCheck(ctx); err != nil {
			errs = append(errs, fmt.Errorf("local: %w", err))
		}
	}

	if c.external != nil {
		if err := c.external.HealthCheck(ctx); err != nil {
			errs = append(errs, fmt.Errorf("external: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("health check failed: %v", errs)
	}
	return nil
}

func (c *CompositeGuardrail) Close() error {
	var errs []error

	if c.local != nil {
		if err := c.local.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if c.external != nil {
		if err := c.external.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("close failed: %v", errs)
	}
	return nil
}