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
	defaultOpenAIEndpoint = "https://api.openai.com/v1/moderations"
	defaultOpenAIModel    = "omni-moderation-latest"
)

// OpenAIConfig holds OpenAI moderation configuration
type OpenAIConfig struct {
	APIKey    string            `koanf:"api_key"`
	Endpoint  string            `koanf:"endpoint"`
	Model     string            `koanf:"model"`
	Timeout   time.Duration     `koanf:"timeout"`
	Thresholds map[string]float64 `koanf:"thresholds"`
	Retry     RetryConfig       `koanf:"retry"`
	CircuitBreaker CircuitBreakerConfig `koanf:"circuit_breaker"`
}

// OpenAIModerationResponse represents the OpenAI moderation API response
type OpenAIModerationResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Results []struct {
		Flagged   bool `json:"flagged"`
		Categories map[string]bool `json:"categories"`
		CategoryScores map[string]float64 `json:"category_scores"`
	} `json:"results"`
}

// OpenAIClassifier implements ExternalClassifier for OpenAI Moderation API
type OpenAIClassifier struct {
	config   OpenAIConfig
	httpClient *GuardrailHTTPClient
	logger   zerolog.Logger
}

// NewOpenAIClassifier creates a new OpenAI moderation classifier
func NewOpenAIClassifier(config OpenAIConfig, logger zerolog.Logger) (*OpenAIClassifier, error) {
	if config.APIKey == "" {
		return nil, fmt.Errorf("OpenAI API key is required")
	}

	if config.Endpoint == "" {
		config.Endpoint = defaultOpenAIEndpoint
	}
	if config.Model == "" {
		config.Model = defaultOpenAIModel
	}
	if config.Timeout == 0 {
		config.Timeout = 5 * time.Second
	}

	// Default thresholds
	if config.Thresholds == nil {
		config.Thresholds = map[string]float64{
			"sexual":       0.7,
			"hate":         0.8,
			"violence":     0.7,
			"self-harm":    0.9,
			"harassment":   0.7,
			"sexual/minors": 0.9,
			"violence/graphic": 0.8,
		}
	}

	httpClient := NewGuardrailHTTPClient(HTTPClientConfig{
		Timeout:        config.Timeout,
		Retry:          config.Retry,
		CircuitBreaker: config.CircuitBreaker,
	}, logger)

	return &OpenAIClassifier{
		config:      config,
		httpClient:  httpClient,
		logger:      logger.With().Str("classifier", "openai").Logger(),
	}, nil
}

// Name returns the classifier name
func (c *OpenAIClassifier) Name() string {
	return "openai"
}

// ClassifyInput classifies input text using OpenAI Moderation API
func (c *OpenAIClassifier) ClassifyInput(ctx context.Context, text string) (guardrail.ClassificationResult, error) {
	return c.classify(ctx, text)
}

// ClassifyOutput classifies output text using OpenAI Moderation API
func (c *OpenAIClassifier) ClassifyOutput(ctx context.Context, text string) (guardrail.ClassificationResult, error) {
	return c.classify(ctx, text)
}

// classify performs the actual classification
func (c *OpenAIClassifier) classify(ctx context.Context, text string) (guardrail.ClassificationResult, error) {
	startTime := time.Now()

	// Prepare request
	reqBody := map[string]interface{}{
		"input": text,
		"model": c.config.Model,
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
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)

	// Execute with retry and circuit breaker
	resp, err := c.httpClient.Do(ctx, req)
	if err != nil {
		return guardrail.ClassificationResult{}, err
	}
	defer resp.Body.Close()

	// Read response
	var apiResp OpenAIModerationResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return guardrail.ClassificationResult{}, fmt.Errorf("decode response: %w", err)
	}

	// Check for errors
	if resp.StatusCode != http.StatusOK {
		return guardrail.ClassificationResult{}, fmt.Errorf("API error %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	// Parse results
	result := guardrail.ClassificationResult{
		Provider: "openai",
		Model:    c.config.Model,
		LatencyMs: time.Since(startTime).Milliseconds(),
		RawResponse: "", // Could store raw JSON if needed
	}

	if len(apiResp.Results) > 0 {
		r := apiResp.Results[0]
		result.Violated = r.Flagged

		for cat, score := range r.CategoryScores {
			threshold := c.config.Thresholds[cat]
			if threshold == 0 {
				// Use default threshold if not configured
				threshold = 0.7
			}

			catResult := guardrail.CategoryResult{
				Category:   cat,
				Detected:   r.Categories[cat],
				Confidence: score,
				Threshold:  threshold,
			}
			result.Categories = append(result.Categories, catResult)
		}
	}

	return result, nil
}

// HealthCheck verifies the classifier is reachable
func (c *OpenAIClassifier) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.openai.com/v1/models", nil)
	if err != nil {
		return fmt.Errorf("create health check request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)

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
func (c *OpenAIClassifier) Close() error {
	return c.httpClient.Close()
}