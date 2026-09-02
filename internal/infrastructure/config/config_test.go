package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var configEnvNames = []string{
	"LLM_PROXY_SERVER_PORT",
	"LLM_PROXY_SERVER_SHOW_BASE_URL",
	"LLM_PROXY_LOG_LEVEL",
	"LLM_PROXY_LOG_FILE",
	"LLM_PROXY_LOG_MAX_AGE",
	"LLM_PROXY_RATE_LIMIT_ENABLED",
	"LLM_PROXY_RATE_LIMIT_DEFAULT_REQUESTS_PER_SECOND",
	"LLM_PROXY_RATE_LIMIT_DEFAULT_BURST",
	"LLM_PROXY_RATE_LIMIT_WHITELIST",
	"LLM_PROXY_RATE_LIMIT_OVERRIDES",
	"LLM_PROXY_PROVIDERS_OPENAI_BASE_URL",
	"LLM_PROXY_PROVIDERS_ANTHROPIC_BASE_URL",
	"LLM_PROXY_OBSERVABILITY_ENABLED",
	"LLM_PROXY_OBSERVABILITY_SERVICE_NAME",
	"LLM_PROXY_OBSERVABILITY_OTLP_ENDPOINT",
	"LLM_PROXY_OBSERVABILITY_TRACE_SAMPLE_RATIO",
	"LLM_PROXY_OBSERVABILITY_METRICS_EXPORT_INTERVAL_SECONDS",
	"LLM_PROXY_GATEWAY_ENABLED",
	"LLM_PROXY_GATEWAY_SNAPSHOT_INTERVAL",
	"LLM_PROXY_GATEWAY_SNAPSHOT_TIMEOUT",
	"LLM_PROXY_GATEWAY_RETRY_BACKOFF",
	"LLM_PROXY_GATEWAY_UPSTREAM_CONNECT_TIMEOUT",
	"LLM_PROXY_GATEWAY_UPSTREAM_TLS_HANDSHAKE_TIMEOUT",
	"LLM_PROXY_GATEWAY_UPSTREAM_RESPONSE_HEADER_TIMEOUT",
	"LLM_PROXY_GATEWAY_UPSTREAM_COMPLETE_TIMEOUT",
	"LLM_PROXY_GATEWAY_UPSTREAM_STREAM_IDLE_TIMEOUT",
	"LLM_PROXY_GATEWAY_UPSTREAM_IDLE_CONNECTION_TIMEOUT",
	"LLM_PROXY_GATEWAY_UPSTREAM_MAX_IDLE_CONNECTIONS",
	"LLM_PROXY_GATEWAY_UPSTREAM_MAX_IDLE_CONNECTIONS_PER_HOST",
	"LLM_PROXY_DATABASE_DRIVER",
	"LLM_PROXY_DATABASE_DSN",
	"LLM_PROXY_DATABASE_MAX_IDLE_CONNECTIONS",
	"LLM_PROXY_DATABASE_MAX_OPEN_CONNECTIONS",
	"LLM_PROXY_DATABASE_CONNECTION_LIFETIME",
	"LLM_PROXY_DATABASE_CONNECT_TIMEOUT",
	"LLM_PROXY_CONTROL_PLANE_TOKEN",
	"LLM_PROXY_CREDENTIAL_ENCRYPTION_CURRENT_KEY_VERSION",
	"LLM_PROXY_CREDENTIAL_ENCRYPTION_KEYS",
}

func isolateConfigEnvironment(t *testing.T) string {
	t.Helper()

	workDir := t.TempDir()
	t.Chdir(workDir)

	for _, name := range configEnvNames {
		value, exists := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
		t.Cleanup(func() {
			if exists {
				_ = os.Setenv(name, value)
				return
			}
			_ = os.Unsetenv(name)
		})
	}

	return workDir
}

func writeConfigFile(t *testing.T, dir, contents string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string // empty string means no file (use a path that does not exist)
		wantErr bool
		check   func(t *testing.T, cfg *Config)
	}{
		{
			name: "valid config file",
			yaml: `
server:
  port: 9090

log:
  level: debug
  file: /tmp/proxy.log
  max_age: 7

rate_limit:
  enabled: false
  default:
    requests_per_second: 5
    burst: 10
  whitelist:
    - "sk-abc"
    - "sk-def"
  overrides:
    "sk-override":
      requests_per_second: 50
      burst: 100

providers:
  openai:
    base_url: "https://custom.openai.com"
  anthropic:
    base_url: "https://custom.anthropic.com"

observability:
  enabled: true
  service_name: proxy-test
  otlp_endpoint: http://collector.example:4318
  trace_sample_ratio: 0.5
  metrics_export_interval_seconds: 30
`,
			wantErr: false,
			check: func(t *testing.T, cfg *Config) {
				t.Helper()

				// Server
				if cfg.Server.Port != 9090 {
					t.Errorf("Server.Port = %d, want 9090", cfg.Server.Port)
				}

				// Log
				if cfg.Log.Level != "debug" {
					t.Errorf("Log.Level = %q, want %q", cfg.Log.Level, "debug")
				}
				if cfg.Log.File != "/tmp/proxy.log" {
					t.Errorf("Log.File = %q, want %q", cfg.Log.File, "/tmp/proxy.log")
				}
				if cfg.Log.MaxAge != 7 {
					t.Errorf("Log.MaxAge = %d, want 7", cfg.Log.MaxAge)
				}

				// RateLimit
				if cfg.RateLimit.Enabled {
					t.Errorf("RateLimit.Enabled = true, want false")
				}
				if cfg.RateLimit.Default.RequestsPerSecond != 5 {
					t.Errorf("RateLimit.Default.RequestsPerSecond = %f, want 5", cfg.RateLimit.Default.RequestsPerSecond)
				}
				if cfg.RateLimit.Default.Burst != 10 {
					t.Errorf("RateLimit.Default.Burst = %d, want 10", cfg.RateLimit.Default.Burst)
				}
				if len(cfg.RateLimit.Whitelist) != 2 {
					t.Errorf("RateLimit.Whitelist length = %d, want 2", len(cfg.RateLimit.Whitelist))
				} else {
					if cfg.RateLimit.Whitelist[0] != "sk-abc" {
						t.Errorf("RateLimit.Whitelist[0] = %q, want %q", cfg.RateLimit.Whitelist[0], "sk-abc")
					}
					if cfg.RateLimit.Whitelist[1] != "sk-def" {
						t.Errorf("RateLimit.Whitelist[1] = %q, want %q", cfg.RateLimit.Whitelist[1], "sk-def")
					}
				}
				if len(cfg.RateLimit.Overrides) != 1 {
					t.Errorf("RateLimit.Overrides length = %d, want 1", len(cfg.RateLimit.Overrides))
				} else {
					override, ok := cfg.RateLimit.Overrides["sk-override"]
					if !ok {
						t.Error("RateLimit.Overrides missing key \"sk-override\"")
					} else {
						if override.RequestsPerSecond != 50 {
							t.Errorf("override.RequestsPerSecond = %f, want 50", override.RequestsPerSecond)
						}
						if override.Burst != 100 {
							t.Errorf("override.Burst = %d, want 100", override.Burst)
						}
					}
				}

				// Providers
				if cfg.Providers.OpenAI.BaseURL != "https://custom.openai.com" {
					t.Errorf("Providers.OpenAI.BaseURL = %q, want %q", cfg.Providers.OpenAI.BaseURL, "https://custom.openai.com")
				}
				if cfg.Providers.Anthropic.BaseURL != "https://custom.anthropic.com" {
					t.Errorf("Providers.Anthropic.BaseURL = %q, want %q", cfg.Providers.Anthropic.BaseURL, "https://custom.anthropic.com")
				}

				wantObservability := ObservabilityConfig{
					Enabled:                      true,
					ServiceName:                  "proxy-test",
					OTLPEndpoint:                 "http://collector.example:4318",
					TraceSampleRatio:             0.5,
					MetricsExportIntervalSeconds: 30,
				}
				if cfg.Observability != wantObservability {
					t.Fatalf("Observability = %+v, want %+v", cfg.Observability, wantObservability)
				}
			},
		},
		{
			name:    "missing config file uses defaults",
			yaml:    "", // no file written; a non-existent path will be used
			wantErr: false,
			check: func(t *testing.T, cfg *Config) {
				t.Helper()

				// Defaults
				if cfg.Server.Port != 8080 {
					t.Errorf("Server.Port = %d, want 8080 (default)", cfg.Server.Port)
				}
				if cfg.Log.Level != "info" {
					t.Errorf("Log.Level = %q, want %q (default)", cfg.Log.Level, "info")
				}
				if cfg.Log.MaxAge != 30 {
					t.Errorf("Log.MaxAge = %d, want 30 (default)", cfg.Log.MaxAge)
				}
				if !cfg.RateLimit.Enabled {
					t.Error("RateLimit.Enabled = false, want true (default)")
				}
				if cfg.RateLimit.Default.RequestsPerSecond != 10 {
					t.Errorf("RateLimit.Default.RequestsPerSecond = %f, want 10 (default)", cfg.RateLimit.Default.RequestsPerSecond)
				}
				if cfg.RateLimit.Default.Burst != 20 {
					t.Errorf("RateLimit.Default.Burst = %d, want 20 (default)", cfg.RateLimit.Default.Burst)
				}
				if cfg.Providers.OpenAI.BaseURL != "https://api.openai.com" {
					t.Errorf("Providers.OpenAI.BaseURL = %q, want %q (default)", cfg.Providers.OpenAI.BaseURL, "https://api.openai.com")
				}
				if cfg.Providers.Anthropic.BaseURL != "https://api.anthropic.com" {
					t.Errorf("Providers.Anthropic.BaseURL = %q, want %q (default)", cfg.Providers.Anthropic.BaseURL, "https://api.anthropic.com")
				}
				wantObservability := ObservabilityConfig{
					Enabled:                      false,
					ServiceName:                  "llm-proxy",
					OTLPEndpoint:                 "http://localhost:4318",
					TraceSampleRatio:             0.1,
					MetricsExportIntervalSeconds: 15,
				}
				if cfg.Observability != wantObservability {
					t.Fatalf("Observability = %+v, want %+v", cfg.Observability, wantObservability)
				}
			},
		},
		{
			name: "partial config merges with defaults",
			yaml: `
server:
  port: 7070
`,
			wantErr: false,
			check: func(t *testing.T, cfg *Config) {
				t.Helper()

				// Overridden value
				if cfg.Server.Port != 7070 {
					t.Errorf("Server.Port = %d, want 7070", cfg.Server.Port)
				}
				// Remaining defaults
				if cfg.Log.Level != "info" {
					t.Errorf("Log.Level = %q, want %q (default)", cfg.Log.Level, "info")
				}
				if cfg.RateLimit.Default.RequestsPerSecond != 10 {
					t.Errorf("RateLimit.Default.RequestsPerSecond = %f, want 10 (default)", cfg.RateLimit.Default.RequestsPerSecond)
				}
				if cfg.Providers.OpenAI.BaseURL != "https://api.openai.com" {
					t.Errorf("Providers.OpenAI.BaseURL = %q, want %q (default)", cfg.Providers.OpenAI.BaseURL, "https://api.openai.com")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateConfigEnvironment(t)

			var configPath string

			if tc.yaml != "" {
				// Write to a temp file.
				f, err := os.CreateTemp(t.TempDir(), "config-*.yaml")
				if err != nil {
					t.Fatalf("failed to create temp config file: %v", err)
				}
				if _, err := f.WriteString(tc.yaml); err != nil {
					t.Fatalf("failed to write temp config file: %v", err)
				}
				if err := f.Close(); err != nil {
					t.Fatalf("failed to close temp config file: %v", err)
				}
				configPath = f.Name()
			} else {
				// Use a path that does not exist.
				configPath = t.TempDir() + "/nonexistent-config.yaml"
			}

			cfg, err := Load(configPath)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg == nil {
				t.Fatal("cfg is nil")
			}

			tc.check(t, cfg)
		})
	}
}

func TestLoadScalarEnvironmentOverrides(t *testing.T) {
	workDir := isolateConfigEnvironment(t)
	configPath := writeConfigFile(t, workDir, `
server:
  port: 7070
log:
  level: info
rate_limit:
  enabled: true
providers:
  openai:
    base_url: https://yaml.openai.example
observability:
  enabled: false
  service_name: yaml-service
  otlp_endpoint: http://yaml-collector.example:4318
  trace_sample_ratio: 0.2
  metrics_export_interval_seconds: 20
`)

	t.Setenv("LLM_PROXY_SERVER_PORT", "9090")
	t.Setenv("LLM_PROXY_SERVER_SHOW_BASE_URL", "https://proxy.example")
	t.Setenv("LLM_PROXY_LOG_LEVEL", "debug")
	t.Setenv("LLM_PROXY_LOG_FILE", "/tmp/env-proxy.log")
	t.Setenv("LLM_PROXY_LOG_MAX_AGE", "14")
	t.Setenv("LLM_PROXY_RATE_LIMIT_ENABLED", "false")
	t.Setenv("LLM_PROXY_RATE_LIMIT_DEFAULT_REQUESTS_PER_SECOND", "12.5")
	t.Setenv("LLM_PROXY_RATE_LIMIT_DEFAULT_BURST", "25")
	t.Setenv("LLM_PROXY_PROVIDERS_OPENAI_BASE_URL", "https://env.openai.example")
	t.Setenv("LLM_PROXY_PROVIDERS_ANTHROPIC_BASE_URL", "https://env.anthropic.example")
	t.Setenv("LLM_PROXY_OBSERVABILITY_ENABLED", "true")
	t.Setenv("LLM_PROXY_OBSERVABILITY_SERVICE_NAME", "env-service")
	t.Setenv("LLM_PROXY_OBSERVABILITY_OTLP_ENDPOINT", "http://env-collector.example:4318")
	t.Setenv("LLM_PROXY_OBSERVABILITY_TRACE_SAMPLE_RATIO", "0.75")
	t.Setenv("LLM_PROXY_OBSERVABILITY_METRICS_EXPORT_INTERVAL_SECONDS", "45")

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.Port != 9090 || cfg.Server.ShowBaseURL != "https://proxy.example" {
		t.Fatalf("server config = %+v", cfg.Server)
	}
	if cfg.Log.Level != "debug" || cfg.Log.File != "/tmp/env-proxy.log" || cfg.Log.MaxAge != 14 {
		t.Fatalf("log config = %+v", cfg.Log)
	}
	if cfg.RateLimit.Enabled || cfg.RateLimit.Default.RequestsPerSecond != 12.5 || cfg.RateLimit.Default.Burst != 25 {
		t.Fatalf("rate limit config = %+v", cfg.RateLimit)
	}
	if cfg.Providers.OpenAI.BaseURL != "https://env.openai.example" || cfg.Providers.Anthropic.BaseURL != "https://env.anthropic.example" {
		t.Fatalf("providers config = %+v", cfg.Providers)
	}
	wantObservability := ObservabilityConfig{
		Enabled:                      true,
		ServiceName:                  "env-service",
		OTLPEndpoint:                 "http://env-collector.example:4318",
		TraceSampleRatio:             0.75,
		MetricsExportIntervalSeconds: 45,
	}
	if cfg.Observability != wantObservability {
		t.Fatalf("observability config = %+v, want %+v", cfg.Observability, wantObservability)
	}
}

func TestLoadInvalidScalarEnvironment(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "LLM_PROXY_SERVER_PORT", value: "not-an-int"},
		{name: "LLM_PROXY_RATE_LIMIT_ENABLED", value: "not-a-bool"},
		{name: "LLM_PROXY_RATE_LIMIT_DEFAULT_REQUESTS_PER_SECOND", value: "not-a-float"},
		{name: "LLM_PROXY_OBSERVABILITY_ENABLED", value: "not-a-bool"},
		{name: "LLM_PROXY_OBSERVABILITY_TRACE_SAMPLE_RATIO", value: "not-a-float"},
		{name: "LLM_PROXY_OBSERVABILITY_METRICS_EXPORT_INTERVAL_SECONDS", value: "not-an-int"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workDir := isolateConfigEnvironment(t)
			t.Setenv(tc.name, tc.value)
			_, err := Load(filepath.Join(workDir, "missing.yaml"))
			if err == nil || !strings.Contains(err.Error(), tc.name) {
				t.Fatalf("Load() error = %v, want error containing %s", err, tc.name)
			}
		})
	}
}

func TestLoadDotEnv(t *testing.T) {
	workDir := isolateConfigEnvironment(t)
	dotEnv := "LLM_PROXY_SERVER_PORT=7070\nLLM_PROXY_LOG_LEVEL=warn\n"
	if err := os.WriteFile(filepath.Join(workDir, ".env"), []byte(dotEnv), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	cfg, err := Load(filepath.Join(workDir, "missing.yaml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.Port != 7070 || cfg.Log.Level != "warn" {
		t.Fatalf("config from .env = %+v", cfg)
	}
}

func TestLoadProcessEnvironmentOverridesDotEnv(t *testing.T) {
	workDir := isolateConfigEnvironment(t)
	if err := os.WriteFile(filepath.Join(workDir, ".env"), []byte("LLM_PROXY_SERVER_PORT=7070\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Setenv("LLM_PROXY_SERVER_PORT", "9090")

	cfg, err := Load(filepath.Join(workDir, "missing.yaml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.Port != 9090 {
		t.Fatalf("Server.Port = %d, want 9090", cfg.Server.Port)
	}
}

func TestLoadMalformedDotEnv(t *testing.T) {
	workDir := isolateConfigEnvironment(t)
	if err := os.WriteFile(filepath.Join(workDir, ".env"), []byte("BROKEN=\"unterminated\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	_, err := Load(filepath.Join(workDir, "missing.yaml"))
	if err == nil || !strings.Contains(err.Error(), "load .env") {
		t.Fatalf("Load() error = %v, want .env parse error", err)
	}
}

func TestLoadComplexEnvironmentOverrides(t *testing.T) {
	workDir := isolateConfigEnvironment(t)
	t.Setenv("LLM_PROXY_RATE_LIMIT_WHITELIST", `["sk-env-a","sk-env-b"]`)
	t.Setenv("LLM_PROXY_RATE_LIMIT_OVERRIDES", `{"sk-env":{"requests_per_second":42.5,"burst":84}}`)

	cfg, err := Load(filepath.Join(workDir, "missing.yaml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(cfg.RateLimit.Whitelist, []string{"sk-env-a", "sk-env-b"}) {
		t.Fatalf("Whitelist = %#v", cfg.RateLimit.Whitelist)
	}
	want := RateLimitRule{RequestsPerSecond: 42.5, Burst: 84}
	if got := cfg.RateLimit.Overrides["sk-env"]; got != want {
		t.Fatalf("override = %+v, want %+v", got, want)
	}
}

func TestLoadInvalidComplexEnvironment(t *testing.T) {
	for _, name := range []string{
		"LLM_PROXY_RATE_LIMIT_WHITELIST",
		"LLM_PROXY_RATE_LIMIT_OVERRIDES",
	} {
		t.Run(name, func(t *testing.T) {
			workDir := isolateConfigEnvironment(t)
			t.Setenv(name, "not-json")
			_, err := Load(filepath.Join(workDir, "missing.yaml"))
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("Load() error = %v, want error containing %s", err, name)
			}
		})
	}
}

func TestLoadGatewayDefaultsDisabled(t *testing.T) {
	workDir := isolateConfigEnvironment(t)

	cfg, err := Load(filepath.Join(workDir, "missing.yaml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Gateway.Enabled {
		t.Fatal("Gateway.Enabled = true, want false")
	}
	if cfg.Database.Driver != "postgres" {
		t.Fatalf("Database.Driver = %q, want postgres", cfg.Database.Driver)
	}
	upstream := cfg.Gateway.Upstream
	if upstream.ConnectTimeout != 10*time.Second || upstream.TLSHandshakeTimeout != 10*time.Second ||
		upstream.ResponseHeaderTimeout != 30*time.Second || upstream.CompleteTimeout != 5*time.Minute ||
		upstream.StreamIdleTimeout != 5*time.Minute || upstream.IdleConnectionTimeout != 90*time.Second ||
		upstream.MaxIdleConnections != 100 || upstream.MaxIdleConnectionsPerHost != 10 {
		t.Fatalf("Gateway.Upstream=%+v", upstream)
	}
}

func TestLoadGatewayUpstreamEnvironment(t *testing.T) {
	workDir := isolateConfigEnvironment(t)
	values := map[string]string{
		"LLM_PROXY_GATEWAY_UPSTREAM_CONNECT_TIMEOUT":               "11s",
		"LLM_PROXY_GATEWAY_UPSTREAM_TLS_HANDSHAKE_TIMEOUT":         "12s",
		"LLM_PROXY_GATEWAY_UPSTREAM_RESPONSE_HEADER_TIMEOUT":       "13s",
		"LLM_PROXY_GATEWAY_UPSTREAM_COMPLETE_TIMEOUT":              "6m",
		"LLM_PROXY_GATEWAY_UPSTREAM_STREAM_IDLE_TIMEOUT":           "7m",
		"LLM_PROXY_GATEWAY_UPSTREAM_IDLE_CONNECTION_TIMEOUT":       "2m",
		"LLM_PROXY_GATEWAY_UPSTREAM_MAX_IDLE_CONNECTIONS":          "120",
		"LLM_PROXY_GATEWAY_UPSTREAM_MAX_IDLE_CONNECTIONS_PER_HOST": "12",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	cfg, err := Load(filepath.Join(workDir, "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	upstream := cfg.Gateway.Upstream
	if upstream.ConnectTimeout != 11*time.Second || upstream.TLSHandshakeTimeout != 12*time.Second ||
		upstream.ResponseHeaderTimeout != 13*time.Second || upstream.CompleteTimeout != 6*time.Minute ||
		upstream.StreamIdleTimeout != 7*time.Minute || upstream.IdleConnectionTimeout != 2*time.Minute ||
		upstream.MaxIdleConnections != 120 || upstream.MaxIdleConnectionsPerHost != 12 {
		t.Fatalf("Gateway.Upstream=%+v", upstream)
	}
}

func TestLoadRejectsInvalidGatewayUpstreamEnvironment(t *testing.T) {
	for _, test := range []struct{ name, value string }{
		{name: "LLM_PROXY_GATEWAY_UPSTREAM_CONNECT_TIMEOUT", value: "invalid"},
		{name: "LLM_PROXY_GATEWAY_UPSTREAM_TLS_HANDSHAKE_TIMEOUT", value: "invalid"},
		{name: "LLM_PROXY_GATEWAY_UPSTREAM_RESPONSE_HEADER_TIMEOUT", value: "invalid"},
		{name: "LLM_PROXY_GATEWAY_UPSTREAM_COMPLETE_TIMEOUT", value: "invalid"},
		{name: "LLM_PROXY_GATEWAY_UPSTREAM_STREAM_IDLE_TIMEOUT", value: "invalid"},
		{name: "LLM_PROXY_GATEWAY_UPSTREAM_IDLE_CONNECTION_TIMEOUT", value: "invalid"},
		{name: "LLM_PROXY_GATEWAY_UPSTREAM_MAX_IDLE_CONNECTIONS", value: "invalid"},
		{name: "LLM_PROXY_GATEWAY_UPSTREAM_MAX_IDLE_CONNECTIONS_PER_HOST", value: "invalid"},
		{name: "LLM_PROXY_GATEWAY_UPSTREAM_CONNECT_TIMEOUT", value: "0s"},
		{name: "LLM_PROXY_GATEWAY_UPSTREAM_MAX_IDLE_CONNECTIONS", value: "0"},
	} {
		t.Run(test.name+"="+test.value, func(t *testing.T) {
			workDir := isolateConfigEnvironment(t)
			t.Setenv(test.name, test.value)
			_, err := Load(filepath.Join(workDir, "missing.yaml"))
			if err == nil {
				t.Fatalf("Load() accepted %s=%s", test.name, test.value)
			}
		})
	}
}

func TestLoadGatewayEnvironment(t *testing.T) {
	workDir := isolateConfigEnvironment(t)
	t.Setenv("LLM_PROXY_GATEWAY_ENABLED", "true")
	t.Setenv("LLM_PROXY_GATEWAY_SNAPSHOT_INTERVAL", "7s")
	t.Setenv("LLM_PROXY_GATEWAY_SNAPSHOT_TIMEOUT", "4s")
	t.Setenv("LLM_PROXY_GATEWAY_RETRY_BACKOFF", "9s")
	t.Setenv("LLM_PROXY_DATABASE_DRIVER", "postgres")
	t.Setenv("LLM_PROXY_DATABASE_DSN", "postgres://user:pass@127.0.0.1:5432/test?sslmode=disable")
	t.Setenv("LLM_PROXY_DATABASE_MAX_IDLE_CONNECTIONS", "7")
	t.Setenv("LLM_PROXY_DATABASE_MAX_OPEN_CONNECTIONS", "31")
	t.Setenv("LLM_PROXY_DATABASE_CONNECTION_LIFETIME", "45m")
	t.Setenv("LLM_PROXY_DATABASE_CONNECT_TIMEOUT", "6s")
	t.Setenv("LLM_PROXY_CONTROL_PLANE_TOKEN", "test-control-token-with-enough-entropy")
	t.Setenv("LLM_PROXY_CREDENTIAL_ENCRYPTION_CURRENT_KEY_VERSION", "v1")
	t.Setenv("LLM_PROXY_CREDENTIAL_ENCRYPTION_KEYS", `{"v1":"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}`)

	cfg, err := Load(filepath.Join(workDir, "missing.yaml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Gateway.Enabled || cfg.Gateway.SnapshotInterval.String() != "7s" || cfg.Gateway.SnapshotTimeout.String() != "4s" || cfg.Gateway.RetryBackoff.String() != "9s" {
		t.Fatalf("Gateway = %+v", cfg.Gateway)
	}
	if cfg.Database.Driver != "postgres" || cfg.Database.MaxIdleConnections != 7 || cfg.Database.MaxOpenConnections != 31 {
		t.Fatalf("Database = %+v", cfg.Database)
	}
	if cfg.Database.ConnectionLifetime.String() != "45m0s" || cfg.Database.ConnectTimeout.String() != "6s" {
		t.Fatalf("Database durations = %+v", cfg.Database)
	}
	if cfg.ControlPlane.Token != "test-control-token-with-enough-entropy" {
		t.Fatalf("ControlPlane.Token was not loaded")
	}
	if cfg.CredentialEncryption.CurrentKeyVersion != "v1" || len(cfg.CredentialEncryption.Keys) != 1 {
		t.Fatalf("CredentialEncryption = %+v", cfg.CredentialEncryption)
	}
}

func TestLoadRejectsInvalidCredentialKeyring(t *testing.T) {
	tests := []struct {
		name    string
		keyring string
	}{
		{name: "invalid json", keyring: "not-json"},
		{name: "invalid base64", keyring: `{"v1":"%%%"}`},
		{name: "short key", keyring: `{"v1":"c2hvcnQ="}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workDir := isolateConfigEnvironment(t)
			t.Setenv("LLM_PROXY_CREDENTIAL_ENCRYPTION_KEYS", tc.keyring)
			_, err := Load(filepath.Join(workDir, "missing.yaml"))
			if err == nil || !strings.Contains(err.Error(), "LLM_PROXY_CREDENTIAL_ENCRYPTION_KEYS") {
				t.Fatalf("Load() error = %v", err)
			}
		})
	}
}

func TestLoadRejectsEnabledGatewayWithoutRequiredSecrets(t *testing.T) {
	workDir := isolateConfigEnvironment(t)
	t.Setenv("LLM_PROXY_GATEWAY_ENABLED", "true")

	_, err := Load(filepath.Join(workDir, "missing.yaml"))
	if err == nil || !strings.Contains(err.Error(), "database dsn") {
		t.Fatalf("Load() error = %v, want missing database dsn", err)
	}
}
