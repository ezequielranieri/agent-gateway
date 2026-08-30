package external

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/domain/guardrail"
	"github.com/rs/zerolog"
)

const (
	defaultAnthropicEndpoint = "https://api.anthropic.com/v1/messages"
	defaultAnthropicModel    = "claude-3-5-haiku-20241022"
	anthropicVersion         = "2023-06-01"
)

// AnthropicConfig holds Anthropic classifier configuration
type AnthropicConfig struct {
	APIKey    string            `koanf:"api_key"`
	Endpoint  string            `koanf:"endpoint"`
	Model     string            `koanf:"model"`
	Timeout   time.Duration     `koanf:"timeout"`
	Thresholds map[string]float64 `koanf:"thresholds"`
	Retry     RetryConfig       `koanf:"retry"`
	CircuitBreaker CircuitBreakerConfig `koanf:"circuit_breaker"`
}

// AnthropicMessage represents a message in Anthropic API
type AnthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// AnthropicRequest represents the Anthropic API request
type AnthropicRequest struct {
	Model       string            `json:"model"`
	MaxTokens   int               `json:"max_tokens"`
	Temperature float64           `json:"temperature"`
	System      string            `json:"system"`
	Messages    []AnthropicMessage `json:"messages"`
}

// AnthropicResponse represents the Anthropic API response
type AnthropicResponse struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	StopSequence *string `json:"stop_sequence"`
	Usage       struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// AnthropicClassifier implements ExternalClassifier for Anthropic
type AnthropicClassifier struct {
	config      AnthropicConfig
	httpClient  *GuardrailHTTPClient
	logger      zerolog.Logger
}

// NewAnthropicClassifier creates a new Anthropic classifier
func NewAnthropicClassifier(config AnthropicConfig, logger zerolog.Logger) (*AnthropicClassifier, error) {
	if config.APIKey == "" {
		return nil, fmt.Errorf("Anthropic API key is required")
	}

	if config.Endpoint == "" {
		config.Endpoint = defaultAnthropicEndpoint
	}
	if config.Model == "" {
		config.Model = defaultAnthropicModel
	}
	if config.Timeout == 0 {
		config.Timeout = 10 * time.Second // Longer timeout for LLM calls
	}

	// Default thresholds
	if config.Thresholds == nil {
		config.Thresholds = map[string]float64{
			"sexual":     0.7,
			"hate":       0.8,
			"violence":   0.7,
			"self-harm":  0.9,
			"harassment": 0.7,
		}
	}

	httpClient := NewGuardrailHTTPClient(HTTPClientConfig{
		Timeout:        config.Timeout,
		Retry:          config.Retry,
		CircuitBreaker: config.CircuitBreaker,
	}, logger)

	return &AnthropicClassifier{
		config:     config,
		httpClient: httpClient,
		logger:     logger.With().Str("classifier", "anthropic").Logger(),
	}, nil
}

// Name returns the classifier name
func (c *AnthropicClassifier) Name() string {
	return "anthropic"
}

// ClassifyInput classifies input text using Anthropic Classifier
func (c *AnthropicClassifier) ClassifyInput(ctx context.Context, text string) (guardrail.ClassificationResult, error) {
	return c.classify(ctx, text)
}

// ClassifyOutput classifies output text using Anthropic Classifier
func (c *AnthropicClassifier) ClassifyOutput(ctx context.Context, text string) (guardrail.ClassificationResult, error) {
	return c.classify(ctx, text)
}

// classify performs the actual classification using Anthropic
func (c *AnthropicClassifier) classify(ctx context.Context, text string) (guardrail.ClassificationResult, error) {
	startTime := time.Now()

	// Build classifier prompt
	systemPrompt := c.buildClassifierPrompt()
	userPrompt := fmt.Sprintf("Classify the following text for safety violations:\n\n%s", text)

	reqBody := AnthropicRequest{
		Model:       c.config.Model,
		MaxTokens:   1024,
		Temperature: 0.0,
		System:      systemPrompt,
		Messages: []AnthropicMessage{
			{Role: "user", Content: userPrompt},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return guardrail.ClassificationResult{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.config.Endpoint, bytes.NewReader(body))
	if err != nil {
		return guardrail.ClassificationResult{}, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.config.APIKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := c.httpClient.Do(ctx, req)
	if err != nil {
		return guardrail.ClassificationResult{}, err
	}
	defer resp.Body.Close()

	var apiResp AnthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return guardrail.ClassificationResult{}, fmt.Errorf("decode response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return guardrail.ClassificationResult{}, fmt.Errorf("API error %d", resp.StatusCode)
	}

	// Parse response - extract JSON from markdown if needed
	result := guardrail.ClassificationResult{
		Provider:  "anthropic",
		Model:     c.config.Model,
		LatencyMs: time.Since(startTime).Milliseconds(),
	}

	if len(apiResp.Content) > 0 {
		content := apiResp.Content[0].Text
		parsed := c.parseClassifierResponse(content, c.config.Thresholds)
		result.Violated = parsed.Violated
		result.Categories = parsed.Categories
	}

	return result, nil
}

// buildClassifierPrompt builds the system prompt for classification
func (c *AnthropicClassifier) buildClassifierPrompt() string {
	return `You are a content safety classifier. Analyze the given text and classify it for safety violations.

Return a JSON object with the following structure:
{
  "violations": [
    {"category": "sexual", "detected": false, "confidence": 0.1},
    {"category": "hate", "detected": false, "confidence": 0.05},
    {"category": "violence", "detected": false, "confidence": 0.02},
    {"category": "self-harm", "detected": false, "confidence": 0.01},
    {"category": "harassment", "detected": false, "confidence": 0.03}
  ]
}

Categories to check:
- sexual: Sexual content, explicit sexual material
- hate: Hate speech, discrimination
- violence: Violence, physical harm
- self-harm: Self-harm, suicide, eating disorders
- harassment: Harassment, bullying, threats

Only output the JSON object, no additional text.`
}

// parseClassifierResponse parses the Anthropic classifier response
func (c *AnthropicClassifier) parseClassifierResponse(content string, thresholds map[string]float64) guardrail.ClassificationResult {
	result := guardrail.ClassificationResult{
		Provider: "anthropic",
	}

	// Try to extract JSON from response (may be wrapped in markdown)
	jsonStr := c.extractJSON(content)
	if jsonStr == "" {
		return result
	}

	var parsed struct {
		Violations []struct {
			Category   string  `json:"category"`
			Detected   bool     `json:"detected"`
			Confidence float64  `json:"confidence"`
		} `json:"violations"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return result
	}

	var categories []guardrail.CategoryResult
	violated := false

	for _, v := range parsed.Violations {
		threshold := 0.7
		if t, ok := thresholds[v.Category]; ok {
			threshold = t
		}

		detected := v.Detected && v.Confidence >= threshold
		if detected {
			violated = true
		}

		categories = append(categories, guardrail.CategoryResult{
			Category:   v.Category,
			Detected:   detected,
			Confidence: v.Confidence,
			Threshold:  threshold,
		})
	}

	result.Violated = violated
	result.Categories = categories
	return result
}

// extractJSON extracts JSON from content (may be wrapped in markdown code blocks)
func (c *AnthropicClassifier) extractJSON(content string) string {
	// Try to find JSON in markdown code blocks
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end > start {
		return content[start : end+1]
	}
	return ""
}

// HealthCheck verifies the classifier is reachable
func (c *AnthropicClassifier) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.anthropic.com/v1/models", nil)
	if err != nil {
		return fmt.Errorf("create health check request: %w", err)
	}

	req.Header.Set("x-api-key", c.config.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.httpClient.Do(ctx, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check failed: %d", resp.StatusCode)
	}
	return nil
}

// Close releases resources
func (c *AnthropicClassifier) Close() error {
	return c.httpClient.Close()
}