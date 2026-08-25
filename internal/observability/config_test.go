package observability

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/goairix/llm-proxy/internal/infrastructure/config"
)

var otelConfigEnvNames = []string{
	"OTEL_SERVICE_NAME",
	"OTEL_EXPORTER_OTLP_ENDPOINT",
	"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
	"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
	"OTEL_EXPORTER_OTLP_PROTOCOL",
	"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
	"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL",
}

func isolateOTelConfigEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range otelConfigEnvNames {
		value, exists := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
		t.Cleanup(func() {
			if exists {
				_ = os.Setenv(name, value)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
}

func validObservabilityConfig() config.ObservabilityConfig {
	return config.ObservabilityConfig{
		Enabled:                      true,
		ServiceName:                  "llm-proxy",
		OTLPEndpoint:                 "http://localhost:4318",
		TraceSampleRatio:             0.1,
		MetricsExportIntervalSeconds: 15,
	}
}

func TestResolveConfig(t *testing.T) {
	t.Run("disabled ignores invalid values", func(t *testing.T) {
		isolateOTelConfigEnvironment(t)
		got, err := resolveConfig(config.ObservabilityConfig{Enabled: false})
		if err != nil {
			t.Fatalf("resolveConfig() error = %v", err)
		}
		if got.enabled {
			t.Fatal("enabled = true, want false")
		}
	})

	tests := []struct {
		name   string
		mutate func(*config.ObservabilityConfig)
		want   string
	}{
		{name: "empty service name", mutate: func(cfg *config.ObservabilityConfig) { cfg.ServiceName = "  " }, want: "service name"},
		{name: "negative sample ratio", mutate: func(cfg *config.ObservabilityConfig) { cfg.TraceSampleRatio = -0.1 }, want: "trace sample ratio"},
		{name: "sample ratio above one", mutate: func(cfg *config.ObservabilityConfig) { cfg.TraceSampleRatio = 1.1 }, want: "trace sample ratio"},
		{name: "zero interval", mutate: func(cfg *config.ObservabilityConfig) { cfg.MetricsExportIntervalSeconds = 0 }, want: "metrics export interval"},
		{name: "relative endpoint", mutate: func(cfg *config.ObservabilityConfig) { cfg.OTLPEndpoint = "/collector" }, want: "OTLP endpoint"},
		{name: "unsupported scheme", mutate: func(cfg *config.ObservabilityConfig) { cfg.OTLPEndpoint = "ftp://collector.example" }, want: "OTLP endpoint"},
		{name: "missing hostname", mutate: func(cfg *config.ObservabilityConfig) { cfg.OTLPEndpoint = "http:///v1" }, want: "OTLP endpoint"},
		{name: "query", mutate: func(cfg *config.ObservabilityConfig) { cfg.OTLPEndpoint = "http://collector.example?secret=1" }, want: "OTLP endpoint"},
		{name: "fragment", mutate: func(cfg *config.ObservabilityConfig) { cfg.OTLPEndpoint = "http://collector.example/#fragment" }, want: "OTLP endpoint"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateOTelConfigEnvironment(t)
			cfg := validObservabilityConfig()
			tc.mutate(&cfg)
			_, err := resolveConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("resolveConfig() error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestResolveConfigEnvironmentPrecedence(t *testing.T) {
	isolateOTelConfigEnvironment(t)
	t.Setenv("OTEL_SERVICE_NAME", "env-service")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://general.example:4318/base")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "https://trace.example/v1/traces")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "https://metric.example/v1/metrics")

	got, err := resolveConfig(validObservabilityConfig())
	if err != nil {
		t.Fatalf("resolveConfig() error = %v", err)
	}
	want := effectiveConfig{
		enabled:        true,
		serviceName:    "env-service",
		traceEndpoint:  "https://trace.example/v1/traces",
		metricEndpoint: "https://metric.example/v1/metrics",
		sampleRatio:    0.1,
		metricInterval: 15 * time.Second,
		traceFromEnv:   true,
		metricFromEnv:  true,
	}
	if got != want {
		t.Fatalf("resolveConfig() = %+v, want %+v", got, want)
	}
}

func TestResolveConfigGeneralEnvironmentPrecedence(t *testing.T) {
	isolateOTelConfigEnvironment(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://general.example:4318/base")

	got, err := resolveConfig(validObservabilityConfig())
	if err != nil {
		t.Fatalf("resolveConfig() error = %v", err)
	}
	if got.traceEndpoint != "http://general.example:4318/base" || got.metricEndpoint != "http://general.example:4318/base" {
		t.Fatalf("endpoints = %q, %q", got.traceEndpoint, got.metricEndpoint)
	}
	if !got.traceFromEnv || !got.metricFromEnv {
		t.Fatalf("from env = %v, %v, want true", got.traceFromEnv, got.metricFromEnv)
	}
}

func TestResolveConfigRejectsGRPCProtocol(t *testing.T) {
	for _, name := range []string{
		"OTEL_EXPORTER_OTLP_PROTOCOL",
		"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
		"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL",
	} {
		t.Run(name, func(t *testing.T) {
			isolateOTelConfigEnvironment(t)
			t.Setenv(name, "grpc")
			_, err := resolveConfig(validObservabilityConfig())
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("resolveConfig() error = %v, want containing %s", err, name)
			}
		})
	}
}

func TestResolveConfigAcceptsHTTPProtobufProtocol(t *testing.T) {
	isolateOTelConfigEnvironment(t)
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
	if _, err := resolveConfig(validObservabilityConfig()); err != nil {
		t.Fatalf("resolveConfig() error = %v", err)
	}
}
