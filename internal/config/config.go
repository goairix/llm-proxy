package config

import (
	"github.com/spf13/viper"
)

// Config is the root configuration struct.
type Config struct {
	Server    ServerConfig    `mapstructure:"server"`
	Log       LogConfig       `mapstructure:"log"`
	RateLimit RateLimitConfig `mapstructure:"rate_limit"`
	Providers ProvidersConfig `mapstructure:"providers"`
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
	RequestsPerSecond float64 `mapstructure:"requests_per_second"`
	Burst             int     `mapstructure:"burst"`
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

	return cfg, nil
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
