package config

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config is the root configuration struct.
type Config struct {
	Server               ServerConfig               `mapstructure:"server"`
	Log                  LogConfig                  `mapstructure:"log"`
	RateLimit            RateLimitConfig            `mapstructure:"rate_limit"`
	Providers            ProvidersConfig            `mapstructure:"providers"`
	Observability        ObservabilityConfig        `mapstructure:"observability"`
	Gateway              GatewayConfig              `mapstructure:"gateway"`
	Database             DatabaseConfig             `mapstructure:"database"`
	ControlPlane         ControlPlaneConfig         `mapstructure:"control_plane"`
	CredentialEncryption CredentialEncryptionConfig `mapstructure:"credential_encryption"`
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Port        int    `mapstructure:"port"`
	ShowBaseURL string `mapstructure:"show_base_url"`
}

// LogConfig holds logging settings.
type LogConfig struct {
	Level  string `mapstructure:"level"`
	File   string `mapstructure:"file"`
	MaxAge int    `mapstructure:"max_age"`
}

// RateLimitConfig holds rate limiting settings.
type RateLimitConfig struct {
	Enabled   bool                     `mapstructure:"enabled"`
	Default   RateLimitRule            `mapstructure:"default"`
	Whitelist []string                 `mapstructure:"whitelist"`
	Overrides map[string]RateLimitRule `mapstructure:"overrides"`
}

// RateLimitRule defines a rate limiting rule.
type RateLimitRule struct {
	RequestsPerSecond float64 `mapstructure:"requests_per_second" json:"requests_per_second"`
	Burst             int     `mapstructure:"burst" json:"burst"`
}

// ProvidersConfig holds LLM provider settings.
type ProvidersConfig struct {
	OpenAI    ProviderConfig `mapstructure:"openai"`
	Anthropic ProviderConfig `mapstructure:"anthropic"`
}

// ProviderConfig holds settings for a single LLM provider.
type ProviderConfig struct {
	BaseURL string `mapstructure:"base_url"`
}

// ObservabilityConfig holds OpenTelemetry exporter settings.
type ObservabilityConfig struct {
	Enabled                      bool    `mapstructure:"enabled"`
	ServiceName                  string  `mapstructure:"service_name"`
	OTLPEndpoint                 string  `mapstructure:"otlp_endpoint"`
	TraceSampleRatio             float64 `mapstructure:"trace_sample_ratio"`
	MetricsExportIntervalSeconds int     `mapstructure:"metrics_export_interval_seconds"`
}

// GatewayConfig controls the unified gateway runtime.
type GatewayConfig struct {
	Enabled          bool                    `mapstructure:"enabled"`
	SnapshotInterval time.Duration           `mapstructure:"snapshot_interval"`
	SnapshotTimeout  time.Duration           `mapstructure:"snapshot_timeout"`
	RetryBackoff     time.Duration           `mapstructure:"retry_backoff"`
	Upstream         UpstreamTransportConfig `mapstructure:"upstream"`
}

type UpstreamTransportConfig struct {
	ConnectTimeout            time.Duration `mapstructure:"connect_timeout"`
	TLSHandshakeTimeout       time.Duration `mapstructure:"tls_handshake_timeout"`
	ResponseHeaderTimeout     time.Duration `mapstructure:"response_header_timeout"`
	CompleteTimeout           time.Duration `mapstructure:"complete_timeout"`
	StreamIdleTimeout         time.Duration `mapstructure:"stream_idle_timeout"`
	IdleConnectionTimeout     time.Duration `mapstructure:"idle_connection_timeout"`
	MaxIdleConnections        int           `mapstructure:"max_idle_connections"`
	MaxIdleConnectionsPerHost int           `mapstructure:"max_idle_connections_per_host"`
}

// DatabaseConfig holds the unified gateway persistence settings.
type DatabaseConfig struct {
	Driver             string        `mapstructure:"driver"`
	DSN                string        `mapstructure:"dsn"`
	MaxIdleConnections int           `mapstructure:"max_idle_connections"`
	MaxOpenConnections int           `mapstructure:"max_open_connections"`
	ConnectionLifetime time.Duration `mapstructure:"connection_lifetime"`
	ConnectTimeout     time.Duration `mapstructure:"connect_timeout"`
}

// ControlPlaneConfig holds authentication settings for control plane APIs.
type ControlPlaneConfig struct {
	Token string `mapstructure:"token"`
}

// CredentialEncryptionConfig holds the versioned master-key ring.
type CredentialEncryptionConfig struct {
	CurrentKeyVersion string            `mapstructure:"current_key_version"`
	Keys              map[string]string `mapstructure:"keys"`
}

// Load reads configuration from the YAML file at path and returns a Config.
// If the file does not exist, defaults are still applied and no error is returned.
func Load(path string) (*Config, error) {
	if err := loadDotEnv(); err != nil {
		return nil, err
	}

	v := viper.New()

	// Set defaults.
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.show_base_url", "")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.max_age", 30)
	v.SetDefault("rate_limit.enabled", true)
	v.SetDefault("rate_limit.default.requests_per_second", 10)
	v.SetDefault("rate_limit.default.burst", 20)
	v.SetDefault("providers.openai.base_url", "https://api.openai.com")
	v.SetDefault("providers.anthropic.base_url", "https://api.anthropic.com")
	v.SetDefault("observability.enabled", false)
	v.SetDefault("observability.service_name", "llm-proxy")
	v.SetDefault("observability.otlp_endpoint", "http://localhost:4318")
	v.SetDefault("observability.trace_sample_ratio", 0.1)
	v.SetDefault("observability.metrics_export_interval_seconds", 15)
	v.SetDefault("gateway.enabled", false)
	v.SetDefault("gateway.snapshot_interval", 5*time.Second)
	v.SetDefault("gateway.snapshot_timeout", 3*time.Second)
	v.SetDefault("gateway.retry_backoff", 5*time.Second)
	v.SetDefault("gateway.upstream.connect_timeout", 10*time.Second)
	v.SetDefault("gateway.upstream.tls_handshake_timeout", 10*time.Second)
	v.SetDefault("gateway.upstream.response_header_timeout", 30*time.Second)
	v.SetDefault("gateway.upstream.complete_timeout", 5*time.Minute)
	v.SetDefault("gateway.upstream.stream_idle_timeout", 5*time.Minute)
	v.SetDefault("gateway.upstream.idle_connection_timeout", 90*time.Second)
	v.SetDefault("gateway.upstream.max_idle_connections", 100)
	v.SetDefault("gateway.upstream.max_idle_connections_per_host", 10)
	v.SetDefault("database.driver", "postgres")
	v.SetDefault("database.dsn", "")
	v.SetDefault("database.max_idle_connections", 5)
	v.SetDefault("database.max_open_connections", 20)
	v.SetDefault("database.connection_lifetime", 30*time.Minute)
	v.SetDefault("database.connect_timeout", 3*time.Second)
	v.SetDefault("control_plane.token", "")
	v.SetDefault("credential_encryption.current_key_version", "")
	v.SetDefault("credential_encryption.keys", map[string]string{})
	if err := bindEnvironment(v); err != nil {
		return nil, err
	}

	// Configure the config file location.
	v.SetConfigFile(path)
	v.SetConfigType("yaml")

	// Read the config file; ignore "file not found" errors so defaults apply.
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			// Check for a path-based "not found" error as well.
			// viper.ReadInConfig may return a *os.PathError when SetConfigFile is used.
			// We treat any missing-file scenario as non-fatal.
			if !isNotFoundError(err) {
				return nil, err
			}
		}
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, err
	}
	if err := applyComplexEnvironment(cfg); err != nil {
		return nil, err
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

func validateConfig(cfg *Config) error {
	for version, encoded := range cfg.CredentialEncryption.Keys {
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return fmt.Errorf("%s[%s]: decode base64: %w", envCredentialKeys, version, err)
		}
		if len(key) != 32 {
			return fmt.Errorf("%s[%s]: decoded length is %d, want 32", envCredentialKeys, version, len(key))
		}
	}
	upstream := cfg.Gateway.Upstream
	if upstream.ConnectTimeout <= 0 || upstream.TLSHandshakeTimeout <= 0 || upstream.ResponseHeaderTimeout <= 0 ||
		upstream.CompleteTimeout <= 0 || upstream.StreamIdleTimeout <= 0 || upstream.IdleConnectionTimeout <= 0 {
		return fmt.Errorf("gateway upstream durations must be positive")
	}
	if upstream.MaxIdleConnections <= 0 || upstream.MaxIdleConnectionsPerHost <= 0 || upstream.MaxIdleConnectionsPerHost > upstream.MaxIdleConnections {
		return fmt.Errorf("gateway upstream connection pool limits are invalid")
	}
	if !cfg.Gateway.Enabled {
		return nil
	}
	if cfg.Database.Driver != "postgres" {
		return fmt.Errorf("database driver must be postgres")
	}
	if strings.TrimSpace(cfg.Database.DSN) == "" {
		return fmt.Errorf("database dsn is required when gateway is enabled")
	}
	if len(cfg.ControlPlane.Token) < 24 {
		return fmt.Errorf("control plane token must contain at least 24 characters")
	}
	if cfg.CredentialEncryption.CurrentKeyVersion == "" {
		return fmt.Errorf("credential encryption current key version is required")
	}
	if _, ok := cfg.CredentialEncryption.Keys[cfg.CredentialEncryption.CurrentKeyVersion]; !ok {
		return fmt.Errorf("credential encryption current key version %q is not present in keyring", cfg.CredentialEncryption.CurrentKeyVersion)
	}
	if cfg.Gateway.SnapshotInterval <= 0 || cfg.Gateway.SnapshotTimeout <= 0 || cfg.Gateway.RetryBackoff <= 0 {
		return fmt.Errorf("gateway durations must be positive")
	}
	if cfg.Database.MaxIdleConnections < 0 || cfg.Database.MaxOpenConnections <= 0 || cfg.Database.MaxIdleConnections > cfg.Database.MaxOpenConnections {
		return fmt.Errorf("database connection pool limits are invalid")
	}
	if cfg.Database.ConnectionLifetime <= 0 || cfg.Database.ConnectTimeout <= 0 {
		return fmt.Errorf("database durations must be positive")
	}
	return nil
}

// isNotFoundError reports whether err indicates that the config file was not found.
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	// viper wraps os errors; a simple string check covers both ConfigFileNotFoundError
	// and os.PathError cases returned when using SetConfigFile.
	msg := err.Error()
	return contains(msg, "no such file or directory") ||
		contains(msg, "The system cannot find") ||
		contains(msg, "open ") // path errors from os.Open start with "open "
}

// contains is a simple substring check to avoid importing strings in this small helper.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstr(s, substr)
}

func searchSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
