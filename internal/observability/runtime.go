package observability

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.uber.org/zap"

	"github.com/goairix/llm-proxy/internal/infrastructure/config"
)

// Runtime owns the OpenTelemetry providers and their lifecycle.
type Runtime struct {
	enabled        bool
	tracerProvider *sdktrace.TracerProvider
	meterProvider  *metric.MeterProvider
	propagator     propagation.TextMapPropagator
	shutdownOnce   sync.Once
	shutdownErr    error
	requests       otelmetric.Int64Counter
	inFlight       otelmetric.Int64UpDownCounter
	rateRejections otelmetric.Int64Counter
}

// New creates an OpenTelemetry runtime. Disabled configuration creates no exporters.
func New(ctx context.Context, cfg config.ObservabilityConfig, serviceVersion string, logger *zap.Logger) (*Runtime, error) {
	effective, err := resolveConfig(cfg)
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{enabled: effective.enabled}
	if !effective.enabled {
		return runtime, nil
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		logger.Error("opentelemetry error", zap.Error(err))
	}))

	res, err := resource.Merge(
		resource.Default(),
		resource.NewSchemaless(
			semconv.ServiceName(effective.serviceName),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create OpenTelemetry resource: %w", err)
	}

	traceOptions := make([]otlptracehttp.Option, 0, 1)
	if !effective.traceFromEnv {
		traceOptions = append(traceOptions, otlptracehttp.WithEndpointURL(appendSignalPath(effective.traceEndpoint, "/v1/traces")))
	}
	traceExporter, err := otlptracehttp.New(ctx, traceOptions...)
	if err != nil {
		return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
	}
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(effective.sampleRatio))),
	)

	metricOptions := make([]otlpmetrichttp.Option, 0, 1)
	if !effective.metricFromEnv {
		metricOptions = append(metricOptions, otlpmetrichttp.WithEndpointURL(appendSignalPath(effective.metricEndpoint, "/v1/metrics")))
	}
	metricExporter, err := otlpmetrichttp.New(ctx, metricOptions...)
	if err != nil {
		_ = tracerProvider.Shutdown(ctx)
		return nil, fmt.Errorf("create OTLP metric exporter: %w", err)
	}
	meterProvider := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(metricExporter, metric.WithInterval(effective.metricInterval))),
		metric.WithResource(res),
	)

	propagator := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
	runtime.tracerProvider = tracerProvider
	runtime.meterProvider = meterProvider
	runtime.propagator = propagator
	if err := runtime.initHTTPInstruments(); err != nil {
		_ = tracerProvider.Shutdown(ctx)
		_ = meterProvider.Shutdown(ctx)
		return nil, err
	}
	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)
	otel.SetTextMapPropagator(propagator)
	return runtime, nil
}

func (r *Runtime) initHTTPInstruments() error {
	meter := r.meterProvider.Meter("github.com/goairix/llm-proxy/internal/observability")
	requests, err := meter.Int64Counter(
		"llm_proxy.requests",
		otelmetric.WithUnit("{request}"),
		otelmetric.WithDescription("Completed LLM proxy requests"),
	)
	if err != nil {
		return fmt.Errorf("create request counter: %w", err)
	}
	inFlight, err := meter.Int64UpDownCounter(
		"llm_proxy.requests.in_flight",
		otelmetric.WithUnit("{request}"),
		otelmetric.WithDescription("LLM proxy requests currently in flight"),
	)
	if err != nil {
		return fmt.Errorf("create in-flight counter: %w", err)
	}
	rateRejections, err := meter.Int64Counter(
		"llm_proxy.rate_limit.rejections",
		otelmetric.WithUnit("{request}"),
		otelmetric.WithDescription("Requests rejected by the proxy rate limiter"),
	)
	if err != nil {
		return fmt.Errorf("create rate-limit rejection counter: %w", err)
	}
	r.requests = requests
	r.inFlight = inFlight
	r.rateRejections = rateRejections
	return nil
}

// Shutdown flushes and closes providers exactly once.
func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil || !r.enabled {
		return nil
	}
	r.shutdownOnce.Do(func() {
		r.shutdownErr = errors.Join(
			r.tracerProvider.ForceFlush(ctx),
			r.meterProvider.ForceFlush(ctx),
			r.tracerProvider.Shutdown(ctx),
			r.meterProvider.Shutdown(ctx),
		)
	})
	return r.shutdownErr
}

func appendSignalPath(endpoint, signalPath string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + signalPath
	parsed.RawPath = ""
	return parsed.String()
}
