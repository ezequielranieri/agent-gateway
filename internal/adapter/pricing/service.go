package pricing

import (
	"context"

	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
)

// Compile-time check that Service implements PricingService
var _ model.PricingService = (*Service)(nil)

// NewServiceFromDomainConfig creates a pricing service from domain model config
func NewServiceFromDomainConfig(cfg model.PricingConfig) (*Service, error) {
	return NewService(cfg)
}

// ServiceOption configures the pricing service
type ServiceOption func(*Service)

// WithDefaultVersion sets the default pricing version
func WithDefaultVersion(version string) ServiceOption {
	return func(s *Service) {
		s.defaultVer = version
		s.currentVer = version
	}
}

// WithTable adds a price table to the service
func WithTable(table *PriceTable) ServiceOption {
	return func(s *Service) {
		s.mu.Lock()
		defer s.mu.Unlock()
		key := priceTableKey{version: table.Version, provider: table.Provider}
		s.tables[key] = table
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
	}
}

// NewTestService creates a service with default test pricing
func NewTestService(opts ...ServiceOption) *Service {
	cfg := DefaultPricingConfig()
	s, _ := NewService(cfg)
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// GetCostForModel is a convenience function for getting cost without context
func (s *Service) GetCostForModel(model string, promptTokens, completionTokens int) (float64, string, error) {
	return s.GetCost(context.Background(), model, promptTokens, completionTokens)
}

// GetModelPriceSync is a convenience function for getting model price without context
func (s *Service) GetModelPriceSync(model string) (float64, float64, string, error) {
	return s.GetModelPrice(context.Background(), model)
}