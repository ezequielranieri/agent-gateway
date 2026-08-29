package model

import (
	"context"
)

// PricingService provides versioned pricing for model usage.
// Implementations load price tables and compute costs in USD.
type PricingService interface {
	// GetCost returns the estimated cost in USD for the given model and token counts.
	// Returns cost in USD (e.g., 0.0015 for $0.0015) and the pricing version used.
	GetCost(ctx context.Context, model string, promptTokens, completionTokens int) (costUSD float64, version string, err error)

	// GetModelPrice returns the per-1k-token pricing for a model.
	// Returns (inputPricePer1k, outputPricePer1k, version, error).
	GetModelPrice(ctx context.Context, model string) (inputPer1k, outputPer1k float64, version string, err error)

	// ListVersions returns available pricing table versions.
	ListVersions(ctx context.Context) ([]string, error)

	// SetVersion sets the active pricing version.
	SetVersion(version string) error

	// CurrentVersion returns the active pricing version.
	CurrentVersion() string
}

// ModelPrice represents the price for a single model.
type ModelPrice struct {
	// Model is the model identifier.
	Model string `json:"model"`

	// InputPricePer1k is the price per 1000 input tokens in USD.
	InputPricePer1k float64 `json:"input_price_per_1k"`

	// OutputPricePer1k is the price per 1000 output tokens in USD.
	OutputPricePer1k float64 `json:"output_price_per_1k"`
}

// PriceTable represents a versioned pricing table.
type PriceTable struct {
	// Version is the table version identifier.
	Version string `json:"version"`

	// Provider is the provider name (openai, anthropic, etc.).
	Provider string `json:"provider"`

	// Prices is the list of model prices.
	Prices []ModelPrice `json:"prices"`

	// EffectiveDate is when this version becomes effective.
	EffectiveDate string `json:"effective_date"`

	// Description is a human-readable description of this version.
	Description string `json:"description,omitempty"`
}

// PricingConfig holds configuration for the pricing service.
type PricingConfig struct {
	// DefaultVersion is the default pricing table version.
	DefaultVersion string `json:"default_version"`

	// Tables is the list of versioned price tables.
	Tables []PriceTable `json:"tables"`

	// AutoUpdate enables automatic version updates (future feature).
	AutoUpdate bool `json:"auto_update"`
}