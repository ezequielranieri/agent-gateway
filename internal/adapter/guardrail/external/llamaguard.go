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
	defaultOllamaEndpoint = "http://localhost:11434"
	defaultLlamaGuardModel = "llama-guard-3"
)

// LlamaGuardConfig holds Llama Guard (Ollama) configuration
type LlamaGuardConfig struct {
	Endpoint    string            `koanf:"endpoint"`
	Model       string            `koanf:"model"`
	Timeout     time.Duration     `koanf:"timeout"`
	Thresholds  map[string]float64 `koanf:"thresholds"`
	Retry       RetryConfig       `koanf:"retry"`
	CircuitBreaker CircuitBreakerConfig `koanf:"circuit_breaker"`
}

// OllamaChatRequest represents the Ollama chat API request
type OllamaChatRequest struct {
	Model    string    `json:"model"`
	Messages []OllamaMessage `json:"messages"`
	Stream   bool      `json:"stream"`
	Options  map[string]interface{} `json:"options,omitempty"`
}

// OllamaMessage represents a message in Ollama API
type OllamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OllamaChatResponse represents the Ollama chat API response
type OllamaChatResponse struct {
	Model     string      `json:"model"`
	CreatedAt string      `json:"created_at"`
	Message   OllamaMessage `json:"message"`
	Done      bool        `json:"done"`
}

// LlamaGuardClassifier implements ExternalClassifier for Llama Guard via Ollama
type LlamaGuardClassifier struct {
	config      LlamaGuardConfig
	httpClient  *GuardrailHTTPClient
	logger      zerolog.Logger
}

// NewLlamaGuardClassifier creates a new Llama Guard classifier
func NewLlamaGuardClassifier(config LlamaGuardConfig, logger zerolog.Logger) (*LlamaGuardClassifier, error) {
	if config.Endpoint == "" {
		config.Endpoint = defaultOllamaEndpoint
	}
	if config.Model == "" {
		config.Model = defaultLlamaGuardModel
	}
	if config.Timeout == 0 {
		config.Timeout = 15 * time.Second // Longer timeout for local LLM
	}

	// Default thresholds
	if config.Thresholds == nil {
		config.Thresholds = map[string]float64{
			"S1": 0.5, // Sexual content
			"S2": 0.5, // Sexual minors
			"S3": 0.5, // Sexual violence
			"V1": 0.5, // Violence
			"V2": 0.5, // Sexual violence
			"H1": 0.5, // Hate
			"H2": 0.5, // Harassment
			"H3": 0.5, // Self-harm
			"SH": 0.5, // Self-harm
		}
	}

	httpClient := NewGuardrailHTTPClient(HTTPClientConfig{
		Timeout:        config.Timeout,
		Retry:          config.Retry,
		CircuitBreaker: config.CircuitBreaker,
	}, logger)

	return &LlamaGuardClassifier{
		config:     config,
		httpClient: httpClient,
		logger:     logger.With().Str("classifier", "llamaguard").Logger(),
	}, nil
}

// Name returns the classifier name
func (c *LlamaGuardClassifier) Name() string {
	return "llamaguard"
}

// ClassifyInput classifies input text using Llama Guard
func (c *LlamaGuardClassifier) ClassifyInput(ctx context.Context, text string) (guardrail.ClassificationResult, error) {
	return c.classify(ctx, text)
}

// ClassifyOutput classifies output text using Llama Guard
func (c *LlamaGuardClassifier) ClassifyOutput(ctx context.Context, text string) (guardrail.ClassificationResult, error) {
	return c.classify(ctx, text)
}

// classify performs the actual classification using Llama Guard
func (c *LlamaGuardClassifier) classify(ctx context.Context, text string) (guardrail.ClassificationResult, error) {
	startTime := time.Now()

	// Build Llama Guard prompt
	prompt := c.buildPrompt(text)

	reqBody := OllamaChatRequest{
		Model: c.config.Model,
		Messages: []OllamaMessage{
			{Role: "user", Content: prompt},
		},
		Stream: false,
		Options: map[string]interface{}{
			"temperature": 0.0,
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return guardrail.ClassificationResult{}, fmt.Errorf("marshal request: %w", err)
	}

	url := strings.TrimRight(c.config.Endpoint, "/") + "/api/chat"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return guardrail.ClassificationResult{}, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(ctx, req)
	if err != nil {
		return guardrail.ClassificationResult{}, err
	}
	defer resp.Body.Close()

	var apiResp OllamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return guardrail.ClassificationResult{}, fmt.Errorf("decode response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return guardrail.ClassificationResult{}, fmt.Errorf("API error %d", resp.StatusCode)
	}

	// Parse response
	result := guardrail.ClassificationResult{
		Provider:  "llamaguard",
		Model:     c.config.Model,
		LatencyMs: time.Since(startTime).Milliseconds(),
	}

	if apiResp.Message.Content != "" {
		parsed := c.parseResponse(apiResp.Message.Content)
		result.Violated = parsed.Violated
		result.Categories = parsed.Categories
	}

	return result, nil
}

// buildPrompt builds the Llama Guard prompt
func (c *LlamaGuardClassifier) buildPrompt(text string) string {
	return fmt.Sprintf(`<BEGIN UNSAFE CONTENT CATEGORIES>
S1: Sexual Content
S2: Sexual Content - Minors
S3: Sexual Violence
V1: Violence
V2: Sexual Violence
H1: Hate
H2: Harassment
H3: Self-Harm
<END UNSAFE CONTENT CATEGORIES>

<BEGIN CONVERSATION>
USER: %s
<END CONVERSATION>

Provide your safety assessment for the USER message above. 
Format your response as a single word: "safe" or "unsafe" followed by a list of violated categories if any.

Example:
safe
or
unsafe
S1, H1, V1`, text)
}

// parseResponse parses the Llama Guard response
func (c *LlamaGuardClassifier) parseResponse(content string) guardrail.ClassificationResult {
	result := guardrail.ClassificationResult{
		Provider: "llamaguard",
	}

	lowerContent := strings.ToLower(strings.TrimSpace(content))
	
	// Check if safe
	if strings.HasPrefix(lowerContent, "safe") {
		result.Violated = false
		return result
	}

	// Check if unsafe
	if strings.HasPrefix(lowerContent, "unsafe") {
		result.Violated = true
		
		// Extract categories from response
		lines := strings.Split(content, "\n")
		if len(lines) > 1 {
			categoriesLine := strings.TrimSpace(lines[1])
			catStrings := strings.Split(categoriesLine, ",")
			
			var catResults []guardrail.CategoryResult
			for _, cat := range catStrings {
				cat = strings.TrimSpace(strings.ToUpper(cat))
				threshold := 0.5
				if t, ok := c.config.Thresholds[cat]; ok {
					threshold = t
				}
				
				catResults = append(catResults, guardrail.CategoryResult{
					Category:   cat,
					Detected:   true,
					Confidence: 0.8, // Llama Guard doesn't provide confidence scores
					Threshold:  threshold,
				})
			}
			result.Categories = catResults
		}
		return result
	}

	// Default to safe if unable to parse
	return result
}

// HealthCheck verifies the classifier is reachable
func (c *LlamaGuardClassifier) HealthCheck(ctx context.Context) error {
	url := strings.TrimRight(c.config.Endpoint, "/") + "/api/tags"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("create health check request: %w", err)
	}

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
func (c *LlamaGuardClassifier) Close() error {
	return c.httpClient.Close()
}