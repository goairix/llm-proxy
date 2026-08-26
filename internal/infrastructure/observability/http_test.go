package observability

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestTransportForHidesProviderBaseURL(t *testing.T) {
	runtime, spans, _ := newHTTPTestRuntime(t)
	transport := runtime.TransportFor("openai_compatible", "responses", roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://private.internal/prefix/v1/responses" {
			t.Fatalf("url=%s", request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}")), Request: request,
		}, nil
	}))
	ctx, parent := runtime.tracerProvider.Tracer("test").Start(context.Background(), "parent")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://private.internal/prefix/v1/responses", strings.NewReader(`{"model":"gpt-5"}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	parent.End()

	var clientSpan sdktrace.ReadOnlySpan
	for _, span := range spans.Ended() {
		if span.SpanKind() == trace.SpanKindClient {
			clientSpan = span
			break
		}
	}
	if clientSpan == nil {
		t.Fatalf("client span not found: %d spans", len(spans.Ended()))
	}
	assertSpanDoesNotContain(t, clientSpan, "private.internal", "/prefix", "gpt-5")
	var attributes strings.Builder
	for _, attr := range clientSpan.Attributes() {
		attributes.WriteString(attr.Value.Emit())
	}
	if !strings.Contains(attributes.String(), "/openai_compatible/responses") {
		t.Fatalf("static upstream route missing: %s", attributes.String())
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		provider, path, want string
	}{
		{provider: "openai", path: "/openai/v1/responses", want: "responses"},
		{provider: "openai", path: "/openai/v1/responses/compact", want: "responses.compact"},
		{provider: "openai", path: "/openai/v1/chat/completions", want: "chat.completions"},
		{provider: "openai", path: "/v1/chat/completions", want: "chat.completions"},
		{provider: "openai", path: "/v1/responses", want: "responses"},
		{provider: "openai", path: "/openai/v1/completions", want: "completions"},
		{provider: "anthropic", path: "/anthropic/v1/messages", want: "messages"},
		{provider: "anthropic", path: "/v1/messages", want: "messages"},
		{provider: "openai", path: "/openai/v1/responses/resp_secret", want: "other"},
		{provider: "anthropic", path: "/anthropic/v1/messages/msg_secret", want: "other"},
	}
	for _, tc := range tests {
		if got := normalizeEndpoint(tc.provider, tc.path); got != tc.want {
			t.Errorf("normalizeEndpoint(%q, %q) = %q, want %q", tc.provider, tc.path, got, tc.want)
		}
	}
}

func TestRequestOutcome(t *testing.T) {
	tests := []struct {
		status int
		err    error
		want   string
	}{
		{status: 201, want: "success"},
		{status: 400, want: "client_error"},
		{status: 429, want: "rate_limited"},
		{status: 500, want: "server_error"},
		{status: 200, err: context.Canceled, want: "canceled"},
	}
	for _, tc := range tests {
		if got := requestOutcome(tc.status, tc.err); got != tc.want {
			t.Errorf("requestOutcome(%d, %v) = %q, want %q", tc.status, tc.err, got, tc.want)
		}
	}
}

func TestWrapHandlerSanitizesURLAndRecordsMetrics(t *testing.T) {
	runtime, spans, reader := newHTTPTestRuntime(t)
	type seenRequest struct {
		path, query, requestURI, authorization string
	}
	seen := make([]seenRequest, 0, 2)
	status := http.StatusCreated
	handler := runtime.WrapHandler("openai", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, seenRequest{
			path:          r.URL.Path,
			query:         r.URL.RawQuery,
			requestURI:    r.RequestURI,
			authorization: r.Header.Get("Authorization"),
		})
		w.WriteHeader(status)
	}))

	for _, code := range []int{http.StatusCreated, http.StatusTooManyRequests} {
		status = code
		req := httptest.NewRequest(http.MethodGet, "/openai/v1/responses/resp_secret?include=usage", nil)
		req.Header.Set("Authorization", "Bearer sk-secret-value")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != code {
			t.Fatalf("response status = %d, want %d", rec.Code, code)
		}
	}

	for _, got := range seen {
		if got.path != "/openai/v1/responses/resp_secret" || got.query != "include=usage" || got.requestURI != "/openai/v1/responses/resp_secret?include=usage" {
			t.Fatalf("downstream request = %+v, want original URL", got)
		}
		if got.authorization != "Bearer sk-secret-value" {
			t.Fatalf("Authorization = %q, want original value", got.authorization)
		}
	}

	ended := spans.Ended()
	if len(ended) != 2 {
		t.Fatalf("ended spans = %d, want 2", len(ended))
	}
	for _, span := range ended {
		assertSpanDoesNotContain(t, span, "resp_secret", "include=usage", "sk-secret-value")
		if strings.Contains(span.Name(), "resp_secret") {
			t.Fatalf("span name %q contains dynamic ID", span.Name())
		}
	}

	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	assertMetricSum(t, data, "llm_proxy.requests", map[string]string{"provider": "openai", "endpoint": "other", "outcome": "success"}, 1)
	assertMetricSum(t, data, "llm_proxy.requests", map[string]string{"provider": "openai", "endpoint": "other", "outcome": "rate_limited"}, 1)
	assertMetricSum(t, data, "llm_proxy.requests.in_flight", map[string]string{"provider": "openai", "endpoint": "other"}, 0)
	assertMetricSum(t, data, "llm_proxy.rate_limit.rejections", map[string]string{"provider": "openai", "endpoint": "other", "outcome": "rate_limited"}, 1)
}

func TestWrapHandlerSanitizesUnifiedGatewayRoute(t *testing.T) {
	runtime, spans, reader := newHTTPTestRuntime(t)
	handler := runtime.WrapHandler("openai", http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" || request.URL.RawQuery != "project_id=project-secret" {
			t.Fatalf("downstream URL=%s", request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer virtual-key-secret" {
			t.Fatalf("downstream authorization=%q", request.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?project_id=project-secret", strings.NewReader(`{"model":"model-secret"}`))
	request.Header.Set("Authorization", "Bearer virtual-key-secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d", recorder.Code)
	}

	ended := spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans=%d", len(ended))
	}
	assertSpanDoesNotContain(t, ended[0], "project-secret", "virtual-key-secret", "model-secret")

	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	assertMetricSum(t, data, "llm_proxy.requests", map[string]string{
		"provider": "openai", "endpoint": "chat.completions", "outcome": "success",
	}, 1)
}

func TestTransportPropagatesTraceAndRestoresUpstreamURL(t *testing.T) {
	runtime, spans, _ := newHTTPTestRuntime(t)
	type upstreamRequest struct {
		requestURI, traceparent, authorization string
	}
	upstreamSeen := make(chan upstreamRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamSeen <- upstreamRequest{
			requestURI:    r.RequestURI,
			traceparent:   r.Header.Get("traceparent"),
			authorization: r.Header.Get("Authorization"),
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	client := &http.Client{Transport: runtime.Transport(http.DefaultTransport)}
	handler := runtime.WrapHandler("openai", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequest, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream.URL+"/v1/responses/resp_secret?include=usage", nil)
		if err != nil {
			t.Errorf("NewRequestWithContext() error = %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		upstreamRequest.Header.Set("Authorization", "Bearer sk-upstream-secret")
		response, err := client.Do(upstreamRequest)
		if err != nil {
			t.Errorf("client.Do() error = %v", err)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		response.Body.Close()
		w.WriteHeader(response.StatusCode)
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/openai/v1/responses/resp_secret?include=usage", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("response status = %d, want 204", recorder.Code)
	}
	gotUpstream := <-upstreamSeen
	if gotUpstream.requestURI != "/v1/responses/resp_secret?include=usage" {
		t.Fatalf("upstream RequestURI = %q, want original", gotUpstream.requestURI)
	}
	if gotUpstream.traceparent == "" {
		t.Fatal("upstream traceparent is empty")
	}
	if gotUpstream.authorization != "Bearer sk-upstream-secret" {
		t.Fatalf("upstream Authorization = %q", gotUpstream.authorization)
	}

	ended := spans.Ended()
	if len(ended) != 2 {
		t.Fatalf("ended spans = %d, want server and client", len(ended))
	}
	var serverSpan, clientSpan tracetest.SpanStub
	for _, span := range ended {
		switch span.SpanKind() {
		case trace.SpanKindServer:
			serverSpan = tracetest.SpanStubFromReadOnlySpan(span)
		case trace.SpanKindClient:
			clientSpan = tracetest.SpanStubFromReadOnlySpan(span)
		}
		assertSpanDoesNotContain(t, span, "resp_secret", "include=usage", "sk-upstream-secret")
	}
	if !serverSpan.SpanContext.IsValid() || !clientSpan.SpanContext.IsValid() {
		t.Fatalf("server/client span not found: server=%v client=%v", serverSpan.SpanContext, clientSpan.SpanContext)
	}
	if clientSpan.Parent.SpanID() != serverSpan.SpanContext.SpanID() {
		t.Fatalf("client parent = %s, want server span %s", clientSpan.Parent.SpanID(), serverSpan.SpanContext.SpanID())
	}
}

func TestWrapHandlerRecordsInFlightDuringRequest(t *testing.T) {
	runtime, _, reader := newHTTPTestRuntime(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	handler := runtime.WrapHandler("anthropic", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
	}))
	go func() {
		defer close(done)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil))
	}()
	<-entered

	var active metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &active); err != nil {
		t.Fatalf("Collect active metrics: %v", err)
	}
	assertMetricSum(t, active, "llm_proxy.requests.in_flight", map[string]string{"provider": "anthropic", "endpoint": "messages"}, 1)

	close(release)
	<-done
	var completed metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &completed); err != nil {
		t.Fatalf("Collect completed metrics: %v", err)
	}
	assertMetricSum(t, completed, "llm_proxy.requests.in_flight", map[string]string{"provider": "anthropic", "endpoint": "messages"}, 0)
}

func newHTTPTestRuntime(t *testing.T) (*Runtime, *tracetest.SpanRecorder, *sdkmetric.ManualReader) {
	t.Helper()
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(spanRecorder),
	)
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	runtime := &Runtime{
		enabled:        true,
		tracerProvider: tracerProvider,
		meterProvider:  meterProvider,
		propagator: propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	}
	if err := runtime.initHTTPInstruments(); err != nil {
		t.Fatalf("initHTTPInstruments() error = %v", err)
	}
	t.Cleanup(func() {
		_ = tracerProvider.Shutdown(context.Background())
		_ = meterProvider.Shutdown(context.Background())
	})
	return runtime, spanRecorder, reader
}

func assertSpanDoesNotContain(t *testing.T, span sdktrace.ReadOnlySpan, forbidden ...string) {
	t.Helper()
	var content strings.Builder
	content.WriteString(span.Name())
	for _, attr := range span.Attributes() {
		content.WriteString(string(attr.Key))
		content.WriteString(attr.Value.Emit())
	}
	for _, value := range forbidden {
		if strings.Contains(content.String(), value) {
			t.Errorf("span %q contains forbidden value %q: %s", span.Name(), value, content.String())
		}
	}
}

func assertMetricSum(t *testing.T, data metricdata.ResourceMetrics, name string, wantAttrs map[string]string, wantValue int64) {
	t.Helper()
	for _, scope := range data.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name != name {
				continue
			}
			sum, ok := metric.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("metric %s data type = %T, want int64 sum", name, metric.Data)
			}
			for _, point := range sum.DataPoints {
				if attributesContain(point.Attributes, wantAttrs) {
					if point.Value != wantValue {
						t.Fatalf("metric %s value = %d, want %d for %v", name, point.Value, wantValue, wantAttrs)
					}
					return
				}
			}
		}
	}
	t.Fatalf("metric %s with attributes %v not found", name, wantAttrs)
}

func attributesContain(set attribute.Set, want map[string]string) bool {
	got := make(map[string]string)
	for _, attr := range set.ToSlice() {
		got[string(attr.Key)] = attr.Value.AsString()
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}
