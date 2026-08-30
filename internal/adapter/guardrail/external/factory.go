package external

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/domain/guardrail"
	"github.com/rs/zerolog"
)

// LocalClassifierConfig holds configuration for local rule-based classifier
type LocalClassifierConfig struct {
	Enabled     bool     `koanf:"enabled"`
	Patterns    []string `koanf:"patterns"`
	Threshold   float64  `koanf:"threshold"`
}

// LocalClassifier implements ExternalClassifier using local regex rules
type LocalClassifier struct {
	config  LocalClassifierConfig
	patterns []*regexp.Regexp
	logger  zerolog.Logger
}

// NewLocalClassifier creates a new local rule-based classifier
func NewLocalClassifier(config LocalClassifierConfig, logger zerolog.Logger) (*LocalClassifier, error) {
	patterns := make([]*regexp.Regexp, len(config.Patterns))
	for i, p := range config.Patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("compile pattern %s: %w", p, err)
		}
		patterns[i] = re
	}

	return &LocalClassifier{
		config:   config,
		patterns: patterns,
		logger:   logger.With().Str("classifier", "local").Logger(),
	}, nil
}

// Name returns the classifier name
func (c *LocalClassifier) Name() string {
	return "local"
}

// ClassifyInput classifies input text using local rules
func (c *LocalClassifier) ClassifyInput(ctx context.Context, text string) (guardrail.ClassificationResult, error) {
	return c.classify(ctx, text)
}

// ClassifyOutput classifies output text using local rules
func (c *LocalClassifier) ClassifyOutput(ctx context.Context, text string) (guardrail.ClassificationResult, error) {
	return c.classify(ctx, text)
}

func (c *LocalClassifier) classify(ctx context.Context, text string) (guardrail.ClassificationResult, error) {
	startTime := time.Now()

	if !c.config.Enabled {
		return guardrail.ClassificationResult{
			Provider:  "local",
			LatencyMs: time.Since(startTime).Milliseconds(),
		}, nil
	}

	var categories []guardrail.CategoryResult
	violated := false

	for _, pattern := range c.patterns {
		if pattern.MatchString(text) {
			categories = append(categories, guardrail.CategoryResult{
				Category:   "local_pattern",
				Detected:   true,
				Confidence: c.config.Threshold,
				Threshold:  c.config.Threshold,
			})
			violated = true
		}
	}

	return guardrail.ClassificationResult{
		Violated:    violated,
		Categories:  categories,
		Provider:    "local",
		LatencyMs:   time.Since(startTime).Milliseconds(),
	}, nil
}

// HealthCheck verifies the classifier is ready
func (c *LocalClassifier) HealthCheck(ctx context.Context) error {
	return nil
}

// Close releases resources
func (c *LocalClassifier) Close() error {
	return nil
}

// ClassifierFactory creates classifiers based on configuration
type ClassifierFactory struct {
	logger zerolog.Logger
}

// NewClassifierFactory creates a new classifier factory
func NewClassifierFactory(logger zerolog.Logger) *ClassifierFactory {
	return &ClassifierFactory{
		logger: logger.With().Str("component", "classifier_factory").Logger(),
	}
}

// ClassifierConfig holds configuration for creating a classifier
type ClassifierConfig struct {
	Type       string                 `koanf:"type"`
	Config     map[string]interface{} `koanf:"config"`
	Thresholds map[string]float64     `koanf:"thresholds"`
	Retry      RetryConfig           `koanf:"retry"`
	CircuitBreaker CircuitBreakerConfig `koanf:"circuit_breaker"`
	Timeout    time.Duration         `koanf:"timeout"`
}

// CreateClassifier creates a classifier from configuration
func (f *ClassifierFactory) CreateClassifier(ctx context.Context, config ClassifierConfig, logger zerolog.Logger) (guardrail.ExternalClassifier, error) {
	switch config.Type {
	case "openai":
		return f.createOpenAI(config, logger)
	case "anthropic":
		return f.createAnthropic(config, logger)
	case "llamaguard":
		return f.createLlamaGuard(config, logger)
	case "local":
		return f.createLocal(config, logger)
	default:
		return nil, fmt.Errorf("unknown classifier type: %s", config.Type)
	}
}

func (f *ClassifierFactory) createOpenAI(config ClassifierConfig, logger zerolog.Logger) (guardrail.ExternalClassifier, error) {
	apiKey := getString(config.Config, "api_key")
	if apiKey == "" {
		return nil, fmt.Errorf("OpenAI API key required")
	}

	cfg := OpenAIConfig{
		APIKey:    apiKey,
		Endpoint:  getString(config.Config, "endpoint"),
		Model:     getString(config.Config, "model"),
		Timeout:   config.Timeout,
		Thresholds: config.Thresholds,
		Retry:     config.Retry,
		CircuitBreaker: config.CircuitBreaker,
	}

	return NewOpenAIClassifier(cfg, logger)
}

func (f *ClassifierFactory) createAnthropic(config ClassifierConfig, logger zerolog.Logger) (guardrail.ExternalClassifier, error) {
	apiKey := getString(config.Config, "api_key")
	if apiKey == "" {
		return nil, fmt.Errorf("Anthropic API key required")
	}

	cfg := AnthropicConfig{
		APIKey:    apiKey,
		Endpoint:  getString(config.Config, "endpoint"),
		Model:     getString(config.Config, "model"),
		Timeout:   config.Timeout,
		Thresholds: config.Thresholds,
		Retry:     config.Retry,
		CircuitBreaker: config.CircuitBreaker,
	}

	return NewAnthropicClassifier(cfg, logger)
}

func (f *ClassifierFactory) createLlamaGuard(config ClassifierConfig, logger zerolog.Logger) (guardrail.ExternalClassifier, error) {
	cfg := LlamaGuardConfig{
		Endpoint:    getString(config.Config, "endpoint"),
		Model:       getString(config.Config, "model"),
		Timeout:     config.Timeout,
		Thresholds:  config.Thresholds,
		Retry:       config.Retry,
		CircuitBreaker: config.CircuitBreaker,
	}

	return NewLlamaGuardClassifier(cfg, logger)
}

func (f *ClassifierFactory) createLocal(config ClassifierConfig, logger zerolog.Logger) (guardrail.ExternalClassifier, error) {
	patterns := getStringSlice(config.Config, "patterns")
	if len(patterns) == 0 {
		// Default patterns for basic safety
		patterns = []string{
			`(?i)\b(?:password|secret|api[_-]?key)\s*[:=]\s*\S+`,
			`(?i)\b(?:ssn|social security)\s*\d{3}[-\s]?\d{2}[-\s]?\d{4}`,
			`(?i)\b\d{4}[-\s]?\d{4}[-\s]?\d{4}[-\s]?\d{4}\b`, // credit card
		}
	}

	cfg := LocalClassifierConfig{
		Enabled:   true,
		Patterns:  patterns,
		Threshold: 0.7,
	}

	return NewLocalClassifier(cfg, logger)
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getStringSlice(m map[string]interface{}, key string) []string {
	if v, ok := m[key]; ok {
		if slice, ok := v.([]interface{}); ok {
			result := make([]string, len(slice))
			for i, v := range slice {
				if s, ok := v.(string); ok {
					result[i] = s
				}
			}
			return result
		}
	}
	return nil
}