package config

import (
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"
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

	// GuardrailsConfig holds guardrails settings
	GuardrailsConfig struct {
		Enabled bool              `koanf:"enabled"`
		Rules   []GuardrailRule   `koanf:"rules"`
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
// Environment variables use prefix "AG_" and replace dots with underscores
// e.g., database.dsn -> AG_DATABASE_DSN
func Load(configPath string) (*Config, error) {
	k := koanf.New(".")

	// Load from YAML file
	if err := k.Load(file.Provider(configPath), yaml.Parser()); err != nil {
		return nil, err
	}

	// Load from environment variables with prefix AG_
	if err := k.Load(env.Provider("AG_", ".", func(s string) string {
		return s
	}), nil); err != nil {
		return nil, err
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