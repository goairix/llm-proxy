package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

const (
	envServerPort                               = "LLM_PROXY_SERVER_PORT"
	envServerShowBaseURL                        = "LLM_PROXY_SERVER_SHOW_BASE_URL"
	envLogLevel                                 = "LLM_PROXY_LOG_LEVEL"
	envLogFile                                  = "LLM_PROXY_LOG_FILE"
	envLogMaxAge                                = "LLM_PROXY_LOG_MAX_AGE"
	envRateLimitEnabled                         = "LLM_PROXY_RATE_LIMIT_ENABLED"
	envRateLimitRequestsPerSecond               = "LLM_PROXY_RATE_LIMIT_DEFAULT_REQUESTS_PER_SECOND"
	envRateLimitBurst                           = "LLM_PROXY_RATE_LIMIT_DEFAULT_BURST"
	envRateLimitWhitelist                       = "LLM_PROXY_RATE_LIMIT_WHITELIST"
	envRateLimitOverrides                       = "LLM_PROXY_RATE_LIMIT_OVERRIDES"
	envOpenAIBaseURL                            = "LLM_PROXY_PROVIDERS_OPENAI_BASE_URL"
	envAnthropicBaseURL                         = "LLM_PROXY_PROVIDERS_ANTHROPIC_BASE_URL"
	envObservabilityEnabled                     = "LLM_PROXY_OBSERVABILITY_ENABLED"
	envObservabilityServiceName                 = "LLM_PROXY_OBSERVABILITY_SERVICE_NAME"
	envObservabilityOTLPEndpoint                = "LLM_PROXY_OBSERVABILITY_OTLP_ENDPOINT"
	envObservabilitySampleRatio                 = "LLM_PROXY_OBSERVABILITY_TRACE_SAMPLE_RATIO"
	envObservabilityMetricSeconds               = "LLM_PROXY_OBSERVABILITY_METRICS_EXPORT_INTERVAL_SECONDS"
	envGatewayEnabled                           = "LLM_PROXY_GATEWAY_ENABLED"
	envGatewaySnapshotInterval                  = "LLM_PROXY_GATEWAY_SNAPSHOT_INTERVAL"
	envGatewaySnapshotTimeout                   = "LLM_PROXY_GATEWAY_SNAPSHOT_TIMEOUT"
	envGatewayRetryBackoff                      = "LLM_PROXY_GATEWAY_RETRY_BACKOFF"
	envGatewayUpstreamConnectTimeout            = "LLM_PROXY_GATEWAY_UPSTREAM_CONNECT_TIMEOUT"
	envGatewayUpstreamTLSHandshakeTimeout       = "LLM_PROXY_GATEWAY_UPSTREAM_TLS_HANDSHAKE_TIMEOUT"
	envGatewayUpstreamResponseHeaderTimeout     = "LLM_PROXY_GATEWAY_UPSTREAM_RESPONSE_HEADER_TIMEOUT"
	envGatewayUpstreamCompleteTimeout           = "LLM_PROXY_GATEWAY_UPSTREAM_COMPLETE_TIMEOUT"
	envGatewayUpstreamStreamIdleTimeout         = "LLM_PROXY_GATEWAY_UPSTREAM_STREAM_IDLE_TIMEOUT"
	envGatewayUpstreamIdleConnectionTimeout     = "LLM_PROXY_GATEWAY_UPSTREAM_IDLE_CONNECTION_TIMEOUT"
	envGatewayUpstreamMaxIdleConnections        = "LLM_PROXY_GATEWAY_UPSTREAM_MAX_IDLE_CONNECTIONS"
	envGatewayUpstreamMaxIdleConnectionsPerHost = "LLM_PROXY_GATEWAY_UPSTREAM_MAX_IDLE_CONNECTIONS_PER_HOST"
	envDatabaseDriver                           = "LLM_PROXY_DATABASE_DRIVER"
	envDatabaseDSN                              = "LLM_PROXY_DATABASE_DSN"
	envDatabaseMaxIdleConnections               = "LLM_PROXY_DATABASE_MAX_IDLE_CONNECTIONS"
	envDatabaseMaxOpenConnections               = "LLM_PROXY_DATABASE_MAX_OPEN_CONNECTIONS"
	envDatabaseConnectionLifetime               = "LLM_PROXY_DATABASE_CONNECTION_LIFETIME"
	envDatabaseConnectTimeout                   = "LLM_PROXY_DATABASE_CONNECT_TIMEOUT"
	envControlPlaneToken                        = "LLM_PROXY_CONTROL_PLANE_TOKEN"
	envCredentialCurrentVersion                 = "LLM_PROXY_CREDENTIAL_ENCRYPTION_CURRENT_KEY_VERSION"
	envCredentialKeys                           = "LLM_PROXY_CREDENTIAL_ENCRYPTION_KEYS"
)

type envBinding struct {
	key      string
	name     string
	validate func(string) error
}

var envBindings = []envBinding{
	{key: "server.port", name: envServerPort, validate: validateInt},
	{key: "server.show_base_url", name: envServerShowBaseURL},
	{key: "log.level", name: envLogLevel},
	{key: "log.file", name: envLogFile},
	{key: "log.max_age", name: envLogMaxAge, validate: validateInt},
	{key: "rate_limit.enabled", name: envRateLimitEnabled, validate: validateBool},
	{key: "rate_limit.default.requests_per_second", name: envRateLimitRequestsPerSecond, validate: validateFloat},
	{key: "rate_limit.default.burst", name: envRateLimitBurst, validate: validateInt},
	{key: "providers.openai.base_url", name: envOpenAIBaseURL},
	{key: "providers.anthropic.base_url", name: envAnthropicBaseURL},
	{key: "observability.enabled", name: envObservabilityEnabled, validate: validateBool},
	{key: "observability.service_name", name: envObservabilityServiceName},
	{key: "observability.otlp_endpoint", name: envObservabilityOTLPEndpoint},
	{key: "observability.trace_sample_ratio", name: envObservabilitySampleRatio, validate: validateFloat},
	{key: "observability.metrics_export_interval_seconds", name: envObservabilityMetricSeconds, validate: validateInt},
	{key: "gateway.enabled", name: envGatewayEnabled, validate: validateBool},
	{key: "gateway.snapshot_interval", name: envGatewaySnapshotInterval, validate: validateDuration},
	{key: "gateway.snapshot_timeout", name: envGatewaySnapshotTimeout, validate: validateDuration},
	{key: "gateway.retry_backoff", name: envGatewayRetryBackoff, validate: validateDuration},
	{key: "gateway.upstream.connect_timeout", name: envGatewayUpstreamConnectTimeout, validate: validateDuration},
	{key: "gateway.upstream.tls_handshake_timeout", name: envGatewayUpstreamTLSHandshakeTimeout, validate: validateDuration},
	{key: "gateway.upstream.response_header_timeout", name: envGatewayUpstreamResponseHeaderTimeout, validate: validateDuration},
	{key: "gateway.upstream.complete_timeout", name: envGatewayUpstreamCompleteTimeout, validate: validateDuration},
	{key: "gateway.upstream.stream_idle_timeout", name: envGatewayUpstreamStreamIdleTimeout, validate: validateDuration},
	{key: "gateway.upstream.idle_connection_timeout", name: envGatewayUpstreamIdleConnectionTimeout, validate: validateDuration},
	{key: "gateway.upstream.max_idle_connections", name: envGatewayUpstreamMaxIdleConnections, validate: validateInt},
	{key: "gateway.upstream.max_idle_connections_per_host", name: envGatewayUpstreamMaxIdleConnectionsPerHost, validate: validateInt},
	{key: "database.driver", name: envDatabaseDriver},
	{key: "database.dsn", name: envDatabaseDSN},
	{key: "database.max_idle_connections", name: envDatabaseMaxIdleConnections, validate: validateInt},
	{key: "database.max_open_connections", name: envDatabaseMaxOpenConnections, validate: validateInt},
	{key: "database.connection_lifetime", name: envDatabaseConnectionLifetime, validate: validateDuration},
	{key: "database.connect_timeout", name: envDatabaseConnectTimeout, validate: validateDuration},
	{key: "control_plane.token", name: envControlPlaneToken},
	{key: "credential_encryption.current_key_version", name: envCredentialCurrentVersion},
}

func loadDotEnv() error {
	err := godotenv.Load()
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("load .env: %w", err)
}

func bindEnvironment(v *viper.Viper) error {
	for _, binding := range envBindings {
		if value, ok := os.LookupEnv(binding.name); ok && binding.validate != nil {
			if err := binding.validate(value); err != nil {
				return fmt.Errorf("%s: %w", binding.name, err)
			}
		}
		if err := v.BindEnv(binding.key, binding.name); err != nil {
			return fmt.Errorf("bind %s: %w", binding.name, err)
		}
	}
	return nil
}

func validateInt(value string) error {
	_, err := strconv.Atoi(value)
	return err
}

func validateBool(value string) error {
	_, err := strconv.ParseBool(value)
	return err
}

func validateFloat(value string) error {
	_, err := strconv.ParseFloat(value, 64)
	return err
}

func validateDuration(value string) error {
	_, err := time.ParseDuration(value)
	return err
}

func applyComplexEnvironment(cfg *Config) error {
	if value, ok := os.LookupEnv(envRateLimitWhitelist); ok {
		if err := json.Unmarshal([]byte(value), &cfg.RateLimit.Whitelist); err != nil {
			return fmt.Errorf("%s: %w", envRateLimitWhitelist, err)
		}
	}
	if value, ok := os.LookupEnv(envRateLimitOverrides); ok {
		if err := json.Unmarshal([]byte(value), &cfg.RateLimit.Overrides); err != nil {
			return fmt.Errorf("%s: %w", envRateLimitOverrides, err)
		}
	}
	if value, ok := os.LookupEnv(envCredentialKeys); ok {
		if err := json.Unmarshal([]byte(value), &cfg.CredentialEncryption.Keys); err != nil {
			return fmt.Errorf("%s: %w", envCredentialKeys, err)
		}
		for version, encoded := range cfg.CredentialEncryption.Keys {
			if _, err := base64.StdEncoding.DecodeString(encoded); err != nil {
				return fmt.Errorf("%s[%s]: %w", envCredentialKeys, version, err)
			}
		}
	}
	return nil
}
