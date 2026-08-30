package guardrail

import (
	"context"
)

type ExternalClassifier interface {
	// Name returns the classifier identifier (e.g., "openai", "anthropic", "llamaguard")
	Name() string

	// ClassifyInput classifies input text for potential violations
	ClassifyInput(ctx context.Context, text string) (ClassificationResult, error)

	// ClassifyOutput classifies output text for potential violations
	ClassifyOutput(ctx context.Context, text string) (ClassificationResult, error)

	// HealthCheck verifies the classifier is reachable
	HealthCheck(ctx context.Context) error

	// Close releases any resources
	Close() error
}

// CategoryResult represents a single category classification result
type CategoryResult struct {
	Category   string  `json:"category"`
	Detected   bool    `json:"detected"`
	Confidence float64 `json:"confidence"`
	Threshold  float64 `json:"threshold"`
}

// ClassificationResult represents the complete classification result
type ClassificationResult struct {
	Violated     bool             `json:"violated"`
	Categories   []CategoryResult `json:"categories"`
	RawResponse  string           `json:"raw_response,omitempty"`
	LatencyMs    int64            `json:"latency_ms"`
	Provider     string           `json:"provider"`
	Model        string           `json:"model"`
}

// CompositeConfig holds configuration for the composite guardrail
type CompositeConfig struct {
	// Mode controls execution mode: "sequential" or "parallel"
	Mode string `koanf:"mode"`

	// FailBehavior controls behavior when external classifier fails
	// "fallback_local" - use local result (default)
	// "fail_open" - allow if external fails
	// "fail_closed" - block if external fails
	FailBehavior string `koanf:"fail_behavior"`

	// MergeLogic controls how to merge violations from multiple classifiers
	// "any_violation" - any classifier detecting violation triggers (default)
	// "all_violation" - all classifiers must detect violation
	// "weighted" - weighted by confidence
	MergeLogic string `koanf:"merge_logic"`

	// ParallelBudgetMs is the timeout budget for parallel execution in milliseconds
	ParallelBudgetMs int `koanf:"parallel_budget_ms"`

	// SendContentExternal controls whether content is sent to external classifiers
	// When false, external classifiers are skipped (data residency)
	SendContentExternal bool `koanf:"send_content_external"`

	// Default thresholds per category
	Thresholds map[string]float64 `koanf:"thresholds"`
}

// DefaultCompositeConfig returns a sensible default configuration
func DefaultCompositeConfig() CompositeConfig {
	return CompositeConfig{
		Mode:               "sequential",
		FailBehavior:       "fallback_local",
		MergeLogic:         "any_violation",
		ParallelBudgetMs:   500,
		SendContentExternal: true,
		Thresholds: map[string]float64{
			"sexual":      0.7,
			"hate":        0.8,
			"violence":    0.7,
			"self-harm":   0.9,
			"harassment":  0.7,
		},
	}
}

// MergeLogicType represents the type of merge logic
type MergeLogicType string

const (
	MergeLogicAnyViolation  MergeLogicType = "any_violation"
	MergeLogicAllViolation  MergeLogicType = "all_violation"
	MergeLogicWeighted      MergeLogicType = "weighted"
)

// FailBehaviorType represents the fail behavior type
type FailBehaviorType string

const (
	FailBehaviorFallbackLocal FailBehaviorType = "fallback_local"
	FailBehaviorFailOpen      FailBehaviorType = "fail_open"
	FailBehaviorFailClosed    FailBehaviorType = "fail_closed"
)