package config

import (
	"os"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"

	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
	"github.com/ezequielranieri/agent-gateway/internal/domain/tool"
)

type (
	// Config holds all configuration for the application
	Config struct {
		Database   DatabaseConfig   `koanf:"database"`
		Redis      RedisConfig      `koanf:"redis"`
		JWT        JWTConfig        `koanf:"jwt"`
		RateLimit  RateLimitConfig  `koanf:"ratelimit"`
		Guardrails GuardrailsConfig `koanf:"guardrails"`
		HITL       HITLConfig       `koanf:"hitl"`
		OTel       OTelConfig       `koanf:"otel"`
		Server     ServerConfig     `koanf:"server"`
		Router     model.RouterConfig `koanf:"router"`
		Pricing    model.PricingConfig `koanf:"pricing"`
		Tool       tool.ToolConfig    `koanf:"tool"`
	}

	// DatabaseConfig holds database connection settings
	DatabaseConfig struct {
		DSN             string        `koanf:"dsn"`
		MaxOpenConns    int           `koanf:"max_open_conns"`
		MaxIdleConns    int           `koanf:"max_idle_conns"`
		ConnMaxLifetime time.Duration `koanf:"conn_max_lifetime"`
		ConnMaxIdleTime time.Duration `koanf:"conn_max_idle_time"`
		ConnectTimeout  time.Duration `koanf:"connect_timeout"`
		QueryTimeout    time.Duration `koanf:"query_timeout"`
	}

	// RedisConfig holds Redis connection settings
	RedisConfig struct {
		Addr           string        `koanf:"addr"`
		Password       string        `koanf:"password"`
		DB             int           `koanf:"db"`
		PoolSize       int           `koanf:"pool_size"`
		MinIdleConns   int           `koanf:"min_idle_conns"`
		DialTimeout    time.Duration `koanf:"dial_timeout"`
		ReadTimeout    time.Duration `koanf:"read_timeout"`
		WriteTimeout   time.Duration `koanf:"write_timeout"`
		PoolTimeout    time.Duration `koanf:"pool_timeout"`
	}

	// JWTConfig holds JWT settings
	JWTConfig struct {
		Secret       string            `koanf:"secret"`
		Issuer       string            `koanf:"issuer"`
		Audience     string            `koanf:"audience"`
		AccessTTL    time.Duration     `koanf:"access_ttl"`
		RefreshTTL   time.Duration     `koanf:"refresh_ttl"`
		KeyRotation  KeyRotationConfig `koanf:"key_rotation"`
	}

	// KeyRotationConfig holds JWT key rotation settings
	KeyRotationConfig struct {
		Enabled   bool              `koanf:"enabled"`
		CurrentKID string           `koanf:"current_kid"`
		Keys      map[string]string `koanf:"keys"`
	}

	// RateLimitConfig holds rate limiting settings
	RateLimitConfig struct {
		DefaultTenantRequestsPerMin  int  `koanf:"default_tenant_requests_per_min"`
		DefaultTenantTokensPerMin    int  `koanf:"default_tenant_tokens_per_min"`
		DefaultTenantToolExecsPerMin int  `koanf:"default_tenant_tool_execs_per_min"`
		DefaultUserRequestsPerMin    int  `koanf:"default_user_requests_per_min"`
		DefaultUserTokensPerMin      int  `koanf:"default_user_tokens_per_min"`
		DefaultUserToolExecsPerMin   int  `koanf:"default_user_tool_execs_per_min"`
		DefaultRoleRequestsPerMin    int  `koanf:"default_role_requests_per_min"`
		DefaultRoleTokensPerMin      int  `koanf:"default_role_tokens_per_min"`
		DefaultRoleToolExecsPerMin   int  `koanf:"default_role_tool_execs_per_min"`
		FailOpen                     bool `koanf:"fail_open"`
	}

	// RetryConfig holds retry configuration
	RetryConfig struct {
		MaxAttempts int           `koanf:"max_attempts"`
		Backoff     time.Duration `koanf:"backoff"`
	}

	// CircuitBreakerConfig holds circuit breaker configuration
	CircuitBreakerConfig struct {
		FailureThreshold int           `koanf:"failure_threshold"`
		Window           time.Duration `koanf:"window"`
		ResetTimeout     time.Duration `koanf:"reset_timeout"`
	}

	// ExternalClassifierConfig holds configuration for external classifier (OpenAI, LlamaGuard, etc.)
	ExternalClassifierConfig struct {
		Enabled        bool              `koanf:"enabled"`
		Type           string            `koanf:"type"`           // openai, anthropic, llamaguard, local
		Config         map[string]string `koanf:"config"`         // api_key, endpoint, model
		Thresholds     map[string]float64 `koanf:"thresholds"`    // per-category thresholds
		Retry          RetryConfig       `koanf:"retry"`
		CircuitBreaker CircuitBreakerConfig `koanf:"circuit_breaker"`
		Timeout        time.Duration     `koanf:"timeout"`
	}

	// CompositeConfig holds configuration for composite guardrail (local + external)
	CompositeConfig struct {
		Mode                string             `koanf:"mode"`                 // sequential, parallel
		FailBehavior        string             `koanf:"fail_behavior"`        // fallback_local, fail_open, fail_closed
		MergeLogic          string             `koanf:"merge_logic"`          // any_violation, all_violation, weighted
		ParallelBudgetMs    int                `koanf:"parallel_budget_ms"`
		SendContentExternal bool               `koanf:"send_content_external"`
		Thresholds          map[string]float64 `koanf:"thresholds"`
	}

	// GuardrailsConfig holds guardrails settings
	GuardrailsConfig struct {
		Enabled              bool                      `koanf:"enabled"`
		PIIPatterns          PIIPatternsConfig         `koanf:"pii_patterns"`
		InjectionPatterns    []string                  `koanf:"injection_patterns"`
		Wordlist             WordlistConfig            `koanf:"wordlist"`
		LengthLimits         LengthLimitsConfig        `koanf:"length_limits"`
		Rules                []GuardrailRule           `koanf:"rules"` // Legacy custom rules
		ExternalClassifier   *ExternalClassifierConfig `koanf:"external_classifier"`
		Composite            CompositeConfig           `koanf:"composite"`
	}

	// PIIPatternsConfig holds PII detection settings
	PIIPatternsConfig struct {
		Email       bool `koanf:"email"`
		CreditCard  bool `koanf:"credit_card"`
		SSN         bool `koanf:"ssn"`
	}

	// WordlistConfig holds wordlist settings
	WordlistConfig struct {
		Enabled     bool   `koanf:"enabled"`
		CustomFile  string `koanf:"custom_file"`
	}

	// LengthLimitsConfig holds length limit settings
	LengthLimitsConfig struct {
		MaxInputChars  int `koanf:"max_input_chars"`
		MaxOutputChars int `koanf:"max_output_chars"`
	}

	// GuardrailRule represents a single guardrail rule
	GuardrailRule struct {
		Name     string   `koanf:"name"`
		Type     string   `koanf:"type"`
		Pattern  string   `koanf:"pattern"`
		Words    []string `koanf:"words"`
		Severity string   `koanf:"severity"`
		Phase    string   `koanf:"phase"`
	}

	// HITLConfig holds HITL settings
	HITLConfig struct {
		DefaultTTL           time.Duration `koanf:"default_ttl"`
		SSEHeartbeatInterval time.Duration `koanf:"sse_heartbeat_interval"`
		CleanupInterval      time.Duration `koanf:"cleanup_interval"`
	}

	// OTelConfig holds OpenTelemetry settings
	OTelConfig struct {
		ServiceName string        `koanf:"service_name"`
		Exporter    ExporterConfig `koanf:"exporter"`
		Tracing     TracingConfig `koanf:"tracing"`
		Metrics     MetricsConfig `koanf:"metrics"`
	}

	// ExporterConfig holds exporter settings
	ExporterConfig struct {
		Type     string `koanf:"type"`
		Endpoint string `koanf:"endpoint"`
	}

	// TracingConfig holds tracing settings
	TracingConfig struct {
		Enabled     bool    `koanf:"enabled"`
		SampleRate  float64 `koanf:"sample_rate"`
	}

	// MetricsConfig holds metrics settings
	MetricsConfig struct {
		Enabled  bool   `koanf:"enabled"`
		Endpoint string `koanf:"endpoint"`
	}

	// ServerConfig holds HTTP server settings
	ServerConfig struct {
		Addr            string        `koanf:"addr"`
		Env             string        `koanf:"env"`
		ReadTimeout     time.Duration `koanf:"read_timeout"`
		WriteTimeout    time.Duration `koanf:"write_timeout"`
		IdleTimeout     time.Duration `koanf:"idle_timeout"`
		ShutdownTimeout time.Duration `koanf:"shutdown_timeout"`
	}
)

// Load loads configuration from YAML file and environment variables
// Environment variables use prefix "AG_" and map to nested keys
// e.g., database.dsn -> AG_DATABASE_DSN, redis.addr -> AG_REDIS_ADDR
func Load(configPath string) (*Config, error) {
	k := koanf.New(".")

	// Load from YAML file
	if err := k.Load(file.Provider(configPath), yaml.Parser()); err != nil {
		return nil, err
	}

	// Load from environment variables with prefix AG_
	// Manually process AG_ env vars to avoid koanf v2 env provider issues
	// AG_DATABASE_DSN -> database.dsn
	// AG_REDIS_ADDR -> redis.addr
	for _, e := range os.Environ() {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		val := parts[1]

		if !strings.HasPrefix(key, "AG_") {
			continue
		}

		// Strip AG_ prefix and convert to lowercase nested key
		// AG_DATABASE_DSN -> database.dsn
		// AG_REDIS_ADDR -> redis.addr
		configKey := strings.ToLower(strings.TrimPrefix(key, "AG_"))
		configKey = strings.ReplaceAll(configKey, "_", ".")
		k.Set(configKey, val)
	}

	// Load from command line flags (for overriding specific values)
	// This allows runtime overrides like: -database.dsn=...
	fs := pflag.NewFlagSet("config", pflag.ContinueOnError)
	fs.String("config", configPath, "config file path")
	// Parse flags but ignore errors (we don't require flags)
	_ = fs.Parse([]string{}) // Empty args, we only use posflag for structure
	if err := k.Load(posflag.Provider(fs, ".", k), nil); err != nil {
		return nil, err
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// MustLoad loads configuration and panics on error
func MustLoad(configPath string) *Config {
	cfg, err := Load(configPath)
	if err != nil {
		panic(err)
	}
	return cfg
}