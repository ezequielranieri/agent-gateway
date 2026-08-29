package chat

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
	"github.com/rs/zerolog"
)

// ProviderRegistry holds all available providers and their configurations
type ProviderRegistry struct {
	mu          sync.RWMutex
	providers   map[string]*RegisteredProvider
	priority    []string // Provider names in priority order (lower index = higher priority)
	logger      zerolog.Logger
	healthCheck HealthCheckFunc
}

// RegisteredProvider wraps a ModelProvider with its configuration and circuit breaker
type RegisteredProvider struct {
	Config       model.ProviderConfig
	Provider     model.ModelProvider
	CircuitBreaker *model.CircuitBreaker
	HealthStatus bool
	LastHealthCheck time.Time
}

// HealthCheckFunc is a function that checks provider health
type HealthCheckFunc func(ctx context.Context, provider model.ModelProvider) error

// NewProviderRegistry creates a new provider registry
func NewProviderRegistry(logger zerolog.Logger) *ProviderRegistry {
	return &ProviderRegistry{
		providers: make(map[string]*RegisteredProvider),
		priority:  []string{},
		logger:    logger.With().Str("component", "provider_registry").Logger(),
		healthCheck: func(ctx context.Context, p model.ModelProvider) error {
			return p.HealthCheck(ctx)
		},
	}
}

// Register adds a provider to the registry
func (r *ProviderRegistry) Register(config model.ProviderConfig, provider model.ModelProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cb := model.NewCircuitBreaker(config.CircuitBreaker)
	
	registered := &RegisteredProvider{
		Config:          config,
		Provider:        provider,
		CircuitBreaker:  cb,
		HealthStatus:    true, // Assume healthy until proven otherwise
		LastHealthCheck: time.Time{},
	}

	r.providers[config.Name] = registered
	
	// Insert into priority list (lower priority value = higher priority)
	r.insertByPriority(config.Name, config.Priority)
	
	r.logger.Info().
		Str("provider", config.Name).
		Str("type", string(config.Type)).
		Int("priority", config.Priority).
		Strs("models", config.Models).
		Msg("Provider registered")
}

// insertByPriority inserts provider name into priority list based on priority value
func (r *ProviderRegistry) insertByPriority(name string, priority int) {
	// Remove if already exists
	for i, n := range r.priority {
		if n == name {
			r.priority = append(r.priority[:i], r.priority[i+1:]...)
			break
		}
	}

	// Find insertion point
	insertIdx := len(r.priority)
	for i, n := range r.priority {
		if r.providers[n].Config.Priority > priority {
			insertIdx = i
			break
		}
	}

	// Insert at position
	if insertIdx >= len(r.priority) {
		r.priority = append(r.priority, name)
	} else {
		r.priority = append(r.priority[:insertIdx], append([]string{name}, r.priority[insertIdx:]...)...)
	}
}

// GetProvider returns a registered provider by name
func (r *ProviderRegistry) GetProvider(name string) (*RegisteredProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

// GetHealthyProviders returns providers that are healthy and enabled, in priority order
func (r *ProviderRegistry) GetHealthyProviders(ctx context.Context) []*RegisteredProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var healthy []*RegisteredProvider
	for _, name := range r.priority {
		p := r.providers[name]
		if !p.Config.Enabled {
			continue
		}
		// Check circuit breaker
		if err := p.CircuitBreaker.AllowRequest(); err != nil {
			r.logger.Debug().
				Str("provider", p.Config.Name).
				Str("state", p.CircuitBreaker.State().String()).
				Msg("Provider circuit breaker open, skipping")
			continue
		}
		healthy = append(healthy, p)
	}
	return healthy
}

// SelectProvider selects the highest-priority healthy provider that supports the model
func (r *ProviderRegistry) SelectProvider(ctx context.Context, modelName string) (*RegisteredProvider, error) {
	healthy := r.GetHealthyProviders(ctx)
	
	for _, p := range healthy {
		if p.supportsModel(modelName) {
			r.logger.Debug().
				Str("provider", p.Config.Name).
				Str("model", modelName).
				Msg("Selected provider for model")
			return p, nil
		}
	}
	
	return nil, model.ErrNoHealthyProvider
}

// supportsModel checks if the provider supports the given model
func (p *RegisteredProvider) supportsModel(modelName string) bool {
	for _, m := range p.Config.Models {
		if m == modelName {
			return true
		}
	}
	return false
}

// RunHealthChecks performs health checks on all enabled providers
func (r *ProviderRegistry) RunHealthChecks(ctx context.Context) {
	r.mu.RLock()
	providers := make([]*RegisteredProvider, 0, len(r.providers))
	for _, p := range r.providers {
		if p.Config.Enabled {
			providers = append(providers, p)
		}
	}
	r.mu.RUnlock()

	var wg sync.WaitGroup
	for _, p := range providers {
		wg.Add(1)
		go func(prov *RegisteredProvider) {
			defer wg.Done()
			r.checkProviderHealth(ctx, prov)
		}(p)
	}
	wg.Wait()
}

// checkProviderHealth checks a single provider's health
func (r *ProviderRegistry) checkProviderHealth(ctx context.Context, p *RegisteredProvider) {
	checkCtx, cancel := context.WithTimeout(ctx, p.Config.Timeout)
	defer cancel()

	err := r.healthCheck(checkCtx, p.Provider)
	
	r.mu.Lock()
	defer r.mu.Unlock()
	
	p.LastHealthCheck = time.Now()
	if err != nil {
		p.HealthStatus = false
		r.logger.Warn().
			Str("provider", p.Config.Name).
			Err(err).
			Msg("Provider health check failed")
	} else {
		p.HealthStatus = true
		r.logger.Debug().
			Str("provider", p.Config.Name).
			Msg("Provider health check passed")
	}
}

// StartPeriodicHealthChecks starts a goroutine that runs health checks periodically
func (r *ProviderRegistry) StartPeriodicHealthChecks(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.RunHealthChecks(ctx)
			}
		}
	}()
}

// GetProviderStats returns statistics for all providers
func (r *ProviderRegistry) GetProviderStats() map[string]ProviderStats {
	r.mu.RLock()
	defer r.mu.RUnlock()

	stats := make(map[string]ProviderStats)
	for name, p := range r.providers {
		stats[name] = ProviderStats{
			Name:           name,
			Type:           string(p.Config.Type),
			Priority:       p.Config.Priority,
			Enabled:        p.Config.Enabled,
			Healthy:        p.HealthStatus,
			LastHealthCheck: p.LastHealthCheck,
			CircuitBreaker: p.CircuitBreaker.Stats(),
			Models:         p.Config.Models,
		}
	}
	return stats
}

// ProviderStats holds statistics for a provider
type ProviderStats struct {
	Name             string
	Type             string
	Priority         int
	Enabled          bool
	Healthy          bool
	LastHealthCheck  time.Time
	CircuitBreaker   model.CircuitBreakerStats
	Models           []string
}

// Router selects the best provider for a given request
type Router struct {
	registry *ProviderRegistry
	logger   zerolog.Logger
}

// NewRouter creates a new router
func NewRouter(registry *ProviderRegistry, logger zerolog.Logger) *Router {
	return &Router{
		registry: registry,
		logger:   logger.With().Str("component", "router").Logger(),
	}
}

// Route selects the best provider for the given model
func (r *Router) Route(ctx context.Context, modelName string) (*RegisteredProvider, error) {
	provider, err := r.registry.SelectProvider(ctx, modelName)
	if err != nil {
		r.logger.Warn().
			Str("model", modelName).
			Err(err).
			Msg("No provider available for model")
		return nil, err
	}
	
	r.logger.Info().
		Str("model", modelName).
		Str("provider", provider.Config.Name).
		Msg("Routed request to provider")
	
	return provider, nil
}

// GetRegistry returns the underlying provider registry
func (r *Router) GetRegistry() *ProviderRegistry {
	return r.registry
}

// BuildRegistryFromConfig creates a provider registry from router configuration
func BuildRegistryFromConfig(
	ctx context.Context,
	cfg model.RouterConfig,
	logger zerolog.Logger,
) (*ProviderRegistry, error) {
	registry := NewProviderRegistry(logger)
	
	// Create providers based on config
	for _, pc := range cfg.Providers {
		if !pc.Enabled {
			logger.Info().
				Str("provider", pc.Name).
				Msg("Provider disabled, skipping registration")
			continue
		}
		
		provider, err := createProviderFromConfig(pc)
		if err != nil {
			logger.Error().
				Str("provider", pc.Name).
				Err(err).
				Msg("Failed to create provider")
			return nil, fmt.Errorf("create provider %s: %w", pc.Name, err)
		}
		
		// Run initial health check
		checkCtx, cancel := context.WithTimeout(ctx, pc.Timeout)
		if err := provider.HealthCheck(checkCtx); err != nil {
			logger.Warn().
				Str("provider", pc.Name).
				Err(err).
				Msg("Initial health check failed, registering anyway")
		}
		cancel()
		
		registry.Register(pc, provider)
	}
	
	if len(registry.priority) == 0 {
		return nil, fmt.Errorf("no providers registered")
	}
	
	return registry, nil
}

// createProviderFromConfig is implemented in providers.go to avoid import cycles