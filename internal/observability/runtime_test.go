package observability

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	"github.com/goairix/llm-proxy/internal/config"
)

func TestRuntimeExportsOTLPHTTP(t *testing.T) {
	isolateOTelConfigEnvironment(t)
	type exportRequest struct {
		contentType string
		body        []byte
	}
	var mu sync.Mutex
	requests := make(map[string][]exportRequest)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read export request: %v", err)
		}
		mu.Lock()
		requests[r.URL.Path] = append(requests[r.URL.Path], exportRequest{contentType: r.Header.Get("Content-Type"), body: body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	cfg := validObservabilityConfig()
	cfg.OTLPEndpoint = collector.URL
	cfg.TraceSampleRatio = 1
	cfg.MetricsExportIntervalSeconds = 60
	runtime, err := New(context.Background(), cfg, "test-version", zap.NewNop())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, span := runtime.tracerProvider.Tracer("runtime-test").Start(context.Background(), "test-span")
	span.SetAttributes(attribute.String("test.key", "value"))
	span.End()
	counter, err := runtime.meterProvider.Meter("runtime-test").Int64Counter("test.counter")
	if err != nil {
		t.Fatalf("Int64Counter() error = %v", err)
	}
	counter.Add(context.Background(), 1)

	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown() error = %v", err)
	}

	mu.Lock()
	traceRequests := append([]exportRequest(nil), requests["/v1/traces"]...)
	metricRequests := append([]exportRequest(nil), requests["/v1/metrics"]...)
	mu.Unlock()
	if len(traceRequests) == 0 || len(metricRequests) == 0 {
		t.Fatalf("export paths = %+v, want /v1/traces and /v1/metrics", requests)
	}
	if !strings.Contains(traceRequests[0].contentType, "protobuf") || !strings.Contains(metricRequests[0].contentType, "protobuf") {
		t.Fatalf("content types = %q, %q", traceRequests[0].contentType, metricRequests[0].contentType)
	}

	var traces collectortracepb.ExportTraceServiceRequest
	if err := proto.Unmarshal(traceRequests[0].body, &traces); err != nil {
		t.Fatalf("unmarshal traces: %v", err)
	}
	if len(traces.ResourceSpans) == 0 {
		t.Fatal("trace export has no resource spans")
	}
	attrs := traces.ResourceSpans[0].Resource.Attributes
	if got := otlpStringAttribute(attrs, "service.name"); got != "llm-proxy" {
		t.Fatalf("service.name = %q, want llm-proxy", got)
	}
	if got := otlpStringAttribute(attrs, "service.version"); got != "test-version" {
		t.Fatalf("service.version = %q, want test-version", got)
	}

	var metrics collectormetricspb.ExportMetricsServiceRequest
	if err := proto.Unmarshal(metricRequests[0].body, &metrics); err != nil {
		t.Fatalf("unmarshal metrics: %v", err)
	}
	if len(metrics.ResourceMetrics) == 0 {
		t.Fatal("metric export has no resource metrics")
	}
}

func TestRuntimeDisabledDoesNotExport(t *testing.T) {
	isolateOTelConfigEnvironment(t)
	requests := 0
	collector := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer collector.Close()

	runtime, err := New(context.Background(), config.ObservabilityConfig{
		Enabled:      false,
		OTLPEndpoint: collector.URL,
	}, "test-version", zap.NewNop())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if runtime.enabled {
		t.Fatal("runtime.enabled = true, want false")
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown() error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("collector requests = %d, want 0", requests)
	}
}

func otlpStringAttribute(attrs []*commonv1.KeyValue, key string) string {
	for _, attr := range attrs {
		if attr.Key == key {
			return attr.Value.GetStringValue()
		}
	}
	return ""
}
