package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
)

// priceTableKey is the key for looking up price tables
type priceTableKey struct {
	version  string
	provider string
}

// ModelPriceEntry represents a model's pricing in a table
type ModelPriceEntry struct {
	Model             string  `json:"model"`
	InputPricePer1k   float64 `json:"input_price_per_1k"`
	OutputPricePer1k  float64 `json:"output_price_per_1k"`
}

// PriceTable is an internal representation of a versioned price table
type PriceTable struct {
	Version      string             `json:"version"`
	Provider     string             `json:"provider"`
	Prices       []ModelPriceEntry  `json:"prices"`
	EffectiveDate string            `json:"effective_date"`
	Description  string             `json:"description,omitempty"`
	loadedAt     time.Time
}

// Service implements the PricingService interface with versioned price tables
type Service struct {
	mu           sync.RWMutex
	tables       map[priceTableKey]*PriceTable
	versions     []string
	currentVer   string
	defaultVer   string
}

// NewService creates a new pricing service with the given config
func NewService(cfg model.PricingConfig) (*Service, error) {
	s := &Service{
		tables:     make(map[priceTableKey]*PriceTable),
		versions:   make([]string, 0),
		defaultVer: cfg.DefaultVersion,
		currentVer: cfg.DefaultVersion,
	}

	// Load all tables from config
	for _, table := range cfg.Tables {
		if err := s.loadTable(table); err != nil {
			return nil, err
		}
	}

	// Validate default version exists
	if _, ok := s.tables[priceTableKey{version: cfg.DefaultVersion, provider: "openai"}]; !ok {
		if _, ok := s.tables[priceTableKey{version: cfg.DefaultVersion, provider: "anthropic"}]; !ok {
			if _, ok := s.tables[priceTableKey{version: cfg.DefaultVersion, provider: "ollama"}]; !ok {
				return nil, errors.New("default pricing version not found in tables")
			}
		}
	}

	return s, nil
}

// loadTable loads a price table from domain config
func (s *Service) loadTable(table model.PriceTable) error {
	entries := make([]ModelPriceEntry, len(table.Prices))
	for i, p := range table.Prices {
		entries[i] = ModelPriceEntry{
			Model:            p.Model,
			InputPricePer1k:  p.InputPricePer1k,
			OutputPricePer1k: p.OutputPricePer1k,
		}
	}

	pt := &PriceTable{
		Version:       table.Version,
		Provider:      table.Provider,
		Prices:        entries,
		EffectiveDate: table.EffectiveDate,
		Description:   table.Description,
		loadedAt:      time.Now(),
	}

	key := priceTableKey{version: table.Version, provider: table.Provider}
	s.tables[key] = pt

	// Track unique versions
	versionExists := false
	for _, v := range s.versions {
		if v == table.Version {
			versionExists = true
			break
		}
	}
	if !versionExists {
		s.versions = append(s.versions, table.Version)
	}

	return nil
}

// GetCost returns the estimated cost in USD for the given model and token counts
func (s *Service) GetCost(ctx context.Context, model string, promptTokens, completionTokens int) (costUSD float64, version string, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Find the model price across all providers in current version
	inputPrice, outputPrice, found := s.findModelPriceUnlocked(s.currentVer, model)
	if !found {
		return 0, s.currentVer, errors.New("model not found in pricing table: " + model)
	}

	costUSD = (float64(promptTokens)/1000.0)*inputPrice + (float64(completionTokens)/1000.0)*outputPrice
	return costUSD, s.currentVer, nil
}

// GetModelPrice returns the per-1k-token pricing for a model
func (s *Service) GetModelPrice(ctx context.Context, model string) (inputPer1k, outputPer1k float64, version string, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	inputPrice, outputPrice, found := s.findModelPriceUnlocked(s.currentVer, model)
	if !found {
		return 0, 0, s.currentVer, errors.New("model not found in pricing table: " + model)
	}

	return inputPrice, outputPrice, s.currentVer, nil
}

// findModelPriceUnlocked finds model price in current version (must hold read lock)
func (s *Service) findModelPriceUnlocked(version, model string) (inputPer1k, outputPer1k float64, found bool) {
	// Check all providers for this version
	providers := []string{"openai", "anthropic", "ollama"}
	for _, provider := range providers {
		key := priceTableKey{version: version, provider: provider}
		if table, ok := s.tables[key]; ok {
			for _, price := range table.Prices {
				if price.Model == model {
					return price.InputPricePer1k, price.OutputPricePer1k, true
				}
			}
		}
	}
	return 0, 0, false
}

// ListVersions returns available pricing table versions
func (s *Service) ListVersions(ctx context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Return copy to avoid external modification
	versions := make([]string, len(s.versions))
	copy(versions, s.versions)
	return versions, nil
}

// SetVersion sets the active pricing version
func (s *Service) SetVersion(version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Verify version exists
	found := false
	for _, v := range s.versions {
		if v == version {
			found = true
			break
		}
	}
	if !found {
		return errors.New("pricing version not found: " + version)
	}

	s.currentVer = version
	return nil
}

// CurrentVersion returns the active pricing version
func (s *Service) CurrentVersion() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentVer
}

// GetTable returns a price table for inspection (testing/debugging)
func (s *Service) GetTable(version, provider string) (*PriceTable, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := priceTableKey{version: version, provider: provider}
	table, ok := s.tables[key]
	if !ok {
		return nil, errors.New("price table not found")
	}
	return table, nil
}

// LoadTableFromJSON loads a price table from JSON data (for dynamic updates)
func (s *Service) LoadTableFromJSON(data []byte) error {
	var table PriceTable
	if err := json.Unmarshal(data, &table); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := priceTableKey{version: table.Version, provider: table.Provider}
	s.tables[key] = &table

	// Track version
	versionExists := false
	for _, v := range s.versions {
		if v == table.Version {
			versionExists = true
			break
		}
	}
	if !versionExists {
		s.versions = append(s.versions, table.Version)
	}

	return nil
}

// DefaultPricingConfig returns a default pricing configuration for testing
func DefaultPricingConfig() model.PricingConfig {
	return model.PricingConfig{
		DefaultVersion: "2024-01",
		AutoUpdate:     false,
		Tables: []model.PriceTable{
			{
				Version:       "2024-01",
				Provider:      "openai",
				EffectiveDate: "2024-01-01",
				Description:   "OpenAI pricing as of January 2024",
				Prices: []model.ModelPrice{
					{Model: "gpt-4", InputPricePer1k: 0.03, OutputPricePer1k: 0.06},
					{Model: "gpt-4-turbo", InputPricePer1k: 0.01, OutputPricePer1k: 0.03},
					{Model: "gpt-3.5-turbo", InputPricePer1k: 0.0005, OutputPricePer1k: 0.0015},
				},
			},
			{
				Version:       "2024-01",
				Provider:      "anthropic",
				EffectiveDate: "2024-01-01",
				Description:   "Anthropic pricing as of January 2024",
				Prices: []model.ModelPrice{
					{Model: "claude-3-opus-20240229", InputPricePer1k: 0.015, OutputPricePer1k: 0.075},
					{Model: "claude-3-sonnet-20240229", InputPricePer1k: 0.003, OutputPricePer1k: 0.015},
					{Model: "claude-3-haiku-20240307", InputPricePer1k: 0.00025, OutputPricePer1k: 0.00125},
				},
			},
			{
				Version:       "2024-01",
				Provider:      "ollama",
				EffectiveDate: "2024-01-01",
				Description:   "Ollama local models (zero cost)",
				Prices: []model.ModelPrice{
					{Model: "llama3", InputPricePer1k: 0.0, OutputPricePer1k: 0.0},
					{Model: "mistral", InputPricePer1k: 0.0, OutputPricePer1k: 0.0},
					{Model: "codellama", InputPricePer1k: 0.0, OutputPricePer1k: 0.0},
				},
			},
		},
	}
}