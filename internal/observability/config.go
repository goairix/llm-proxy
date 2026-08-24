package observability

import (
	"fmt"
	"math"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/goairix/llm-proxy/internal/config"
)

const (
	envOTelServiceName    = "OTEL_SERVICE_NAME"
	envOTLPEndpoint       = "OTEL_EXPORTER_OTLP_ENDPOINT"
	envOTLPTraceEndpoint  = "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"
	envOTLPMetricEndpoint = "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"
	envOTLPProtocol       = "OTEL_EXPORTER_OTLP_PROTOCOL"
	envOTLPTraceProtocol  = "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"
	envOTLPMetricProtocol = "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL"
)

type effectiveConfig struct {
	enabled        bool
	serviceName    string
	traceEndpoint  string
	metricEndpoint string
	sampleRatio    float64
	metricInterval time.Duration
	traceFromEnv   bool
	metricFromEnv  bool
}

func resolveConfig(cfg config.ObservabilityConfig) (effectiveConfig, error) {
	if !cfg.Enabled {
		return effectiveConfig{}, nil
	}

	for _, name := range []string{envOTLPProtocol, envOTLPTraceProtocol, envOTLPMetricProtocol} {
		if value, ok := os.LookupEnv(name); ok {
			value = strings.TrimSpace(value)
			if value != "" && value != "http/protobuf" {
				return effectiveConfig{}, fmt.Errorf("%s must be http/protobuf, got %q", name, value)
			}
		}
	}

	serviceName := strings.TrimSpace(cfg.ServiceName)
	if value, ok := os.LookupEnv(envOTelServiceName); ok {
		serviceName = strings.TrimSpace(value)
	}
	if serviceName == "" {
		return effectiveConfig{}, fmt.Errorf("service name must not be empty")
	}
	if cfg.TraceSampleRatio < 0 || cfg.TraceSampleRatio > 1 {
		return effectiveConfig{}, fmt.Errorf("trace sample ratio must be between 0 and 1")
	}
	if cfg.MetricsExportIntervalSeconds <= 0 || cfg.MetricsExportIntervalSeconds > int(math.MaxInt64/int64(time.Second)) {
		return effectiveConfig{}, fmt.Errorf("metrics export interval must be a positive duration")
	}

	traceEndpoint, traceFromEnv := resolveEndpoint(envOTLPTraceEndpoint, cfg.OTLPEndpoint)
	metricEndpoint, metricFromEnv := resolveEndpoint(envOTLPMetricEndpoint, cfg.OTLPEndpoint)
	for _, endpoint := range []string{traceEndpoint, metricEndpoint} {
		if err := validateEndpoint(endpoint); err != nil {
			return effectiveConfig{}, fmt.Errorf("OTLP endpoint %q: %w", endpoint, err)
		}
	}

	return effectiveConfig{
		enabled:        true,
		serviceName:    serviceName,
		traceEndpoint:  traceEndpoint,
		metricEndpoint: metricEndpoint,
		sampleRatio:    cfg.TraceSampleRatio,
		metricInterval: time.Duration(cfg.MetricsExportIntervalSeconds) * time.Second,
		traceFromEnv:   traceFromEnv,
		metricFromEnv:  metricFromEnv,
	}, nil
}

func resolveEndpoint(signalName, projectEndpoint string) (string, bool) {
	if endpoint, ok := os.LookupEnv(signalName); ok {
		return strings.TrimSpace(endpoint), true
	}
	if endpoint, ok := os.LookupEnv(envOTLPEndpoint); ok {
		return strings.TrimSpace(endpoint), true
	}
	return strings.TrimSpace(projectEndpoint), false
}

func validateEndpoint(endpoint string) error {
	if strings.Contains(endpoint, "#") {
		return fmt.Errorf("fragment is not allowed")
	}
	parsed, err := url.ParseRequestURI(endpoint)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https")
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("hostname must not be empty")
	}
	if parsed.User != nil {
		return fmt.Errorf("userinfo is not allowed")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return fmt.Errorf("query is not allowed")
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("fragment is not allowed")
	}
	return nil
}
