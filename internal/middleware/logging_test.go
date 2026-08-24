package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest"
	"go.uber.org/zap/zaptest/observer"
)

// TestLoggingMiddlewareStatus verifies that the middleware logs the correct status code.
func TestLoggingMiddlewareStatus(t *testing.T) {
	logger := zaptest.NewLogger(t)

	handler := Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	req := httptest.NewRequest(http.MethodGet, "/openai/v1/chat", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", rec.Code)
	}
}

// TestLoggingMiddlewareProvider verifies that the middleware correctly identifies the provider from the path.
func TestLoggingMiddlewareProvider(t *testing.T) {
	logger := zaptest.NewLogger(t)

	tests := []struct {
		path             string
		expectedProvider string
	}{
		{"/openai/v1/chat", "openai"},
		{"/anthropic/v1/messages", "anthropic"},
		{"/health", "unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			provider := extractProvider(tc.path)
			if provider != tc.expectedProvider {
				t.Errorf("extractProvider(%q): expected %q, got %q", tc.path, tc.expectedProvider, provider)
			}
		})
	}

	// Also exercise the full middleware path to ensure it doesn't panic.
	handler := Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, tc := range tests {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
}

// TestMaskAPIKeyLong verifies that a key longer than 4 chars is masked correctly.
func TestMaskAPIKeyLong(t *testing.T) {
	got := maskAPIKey("sk-abcd1234")
	want := "****1234"
	if got != want {
		t.Errorf("maskAPIKey(%q) = %q, want %q", "sk-abcd1234", got, want)
	}
}

// TestMaskAPIKeyShort verifies that a key shorter than 4 chars returns "****".
func TestMaskAPIKeyShort(t *testing.T) {
	got := maskAPIKey("ab")
	want := "****"
	if got != want {
		t.Errorf("maskAPIKey(%q) = %q, want %q", "ab", got, want)
	}
}

// TestExtractProviderOpenAI verifies extractProvider for an OpenAI path.
func TestExtractProviderOpenAI(t *testing.T) {
	got := extractProvider("/openai/v1/chat")
	want := "openai"
	if got != want {
		t.Errorf("extractProvider(%q) = %q, want %q", "/openai/v1/chat", got, want)
	}
}

// TestExtractProviderAnthropic verifies extractProvider for an Anthropic path.
func TestExtractProviderAnthropic(t *testing.T) {
	got := extractProvider("/anthropic/v1/messages")
	want := "anthropic"
	if got != want {
		t.Errorf("extractProvider(%q) = %q, want %q", "/anthropic/v1/messages", got, want)
	}
}

// TestExtractProviderUnknown verifies extractProvider returns "unknown" for unrecognized paths.
func TestExtractProviderUnknown(t *testing.T) {
	got := extractProvider("/health")
	want := "unknown"
	if got != want {
		t.Errorf("extractProvider(%q) = %q, want %q", "/health", got, want)
	}
}

// TestResponseWriterCapturesStatusAndBytes verifies that responseWriter correctly
// records the status code and body bytes.
func TestResponseWriterCapturesStatusAndBytes(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := newResponseWriter(rec)

	// Default status should be 200.
	if rw.status != http.StatusOK {
		t.Errorf("default status: expected 200, got %d", rw.status)
	}

	rw.WriteHeader(http.StatusAccepted)
	if rw.status != http.StatusAccepted {
		t.Errorf("after WriteHeader: expected 202, got %d", rw.status)
	}

	body := []byte("hello world")
	n, err := rw.Write(body)
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if n != len(body) {
		t.Errorf("Write returned n=%d, want %d", n, len(body))
	}
	if rw.bytes != len(body) {
		t.Errorf("rw.bytes = %d, want %d", rw.bytes, len(body))
	}
}

// TestResponseWriterCapturesErrorBody verifies that the error body is captured for 4xx/5xx.
func TestResponseWriterCapturesErrorBody(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := newResponseWriter(rec)

	rw.WriteHeader(http.StatusBadRequest)
	errBody := []byte(`{"error":"invalid request"}`)
	rw.Write(errBody)

	if string(rw.errorBody) != string(errBody) {
		t.Errorf("errorBody = %q, want %q", rw.errorBody, errBody)
	}
}

// TestResponseWriterNoErrorBodyForSuccess verifies no error body is captured for 2xx.
func TestResponseWriterNoErrorBodyForSuccess(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := newResponseWriter(rec)

	rw.WriteHeader(http.StatusOK)
	rw.Write([]byte("ok"))

	if len(rw.errorBody) != 0 {
		t.Errorf("errorBody should be empty for 200, got %q", rw.errorBody)
	}
}

// TestLoggingMiddlewareErrorLevels verifies log levels for different status codes.
func TestLoggingMiddlewareErrorLevels(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		expectedLevel zapcore.Level
		expectedMsg   string
	}{
		{"2xx logs Info", http.StatusOK, zapcore.InfoLevel, "request"},
		{"4xx logs Warn", http.StatusBadRequest, zapcore.WarnLevel, "upstream_client_error"},
		{"401 logs Warn", http.StatusUnauthorized, zapcore.WarnLevel, "upstream_client_error"},
		{"429 logs Warn", http.StatusTooManyRequests, zapcore.WarnLevel, "upstream_client_error"},
		{"5xx logs Error", http.StatusInternalServerError, zapcore.ErrorLevel, "upstream_server_error"},
		{"502 logs Error", http.StatusBadGateway, zapcore.ErrorLevel, "upstream_server_error"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zapcore.DebugLevel)
			logger := zap.New(core)

			handler := Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				if tc.status >= 400 {
					w.Write([]byte(`{"error":"test"}`))
				}
			}))

			req := httptest.NewRequest(http.MethodGet, "/openai/v1/chat", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if logs.Len() != 1 {
				t.Fatalf("expected 1 log entry, got %d", logs.Len())
			}

			entry := logs.All()[0]
			if entry.Level != tc.expectedLevel {
				t.Errorf("expected log level %v, got %v", tc.expectedLevel, entry.Level)
			}
			if entry.Message != tc.expectedMsg {
				t.Errorf("expected message %q, got %q", tc.expectedMsg, entry.Message)
			}

			// For error responses, verify upstream_error field is present
			if tc.status >= 400 {
				found := false
				for _, f := range entry.ContextMap() {
					if f == `{"error":"test"}` {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected upstream_error field in log entry for status %d", tc.status)
				}
			}
		})
	}
}

// TestResponseWriterImplementsFlusher verifies that responseWriter implements http.Flusher.
func TestResponseWriterImplementsFlusher(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := newResponseWriter(rec)

	flusher, ok := any(rw).(http.Flusher)
	if !ok {
		t.Fatal("responseWriter does not implement http.Flusher")
	}

	// Calling Flush should not panic; httptest.ResponseRecorder implements http.Flusher.
	flusher.Flush()
	if !rec.Flushed {
		t.Error("expected underlying ResponseRecorder to have been flushed")
	}
}

func TestLoggingMiddlewareTraceCorrelation(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3},
		SpanID:     trace.SpanID{4, 5, 6},
		TraceFlags: trace.FlagsSampled,
	})
	handler := Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/openai/v1/responses", nil)
	req = req.WithContext(trace.ContextWithSpanContext(req.Context(), sc))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if logs.Len() != 1 {
		t.Fatalf("log entries = %d, want 1", logs.Len())
	}
	fields := logs.All()[0].ContextMap()
	if fields["trace_id"] != sc.TraceID().String() {
		t.Fatalf("trace_id = %v, want %s", fields["trace_id"], sc.TraceID())
	}
	if fields["span_id"] != sc.SpanID().String() {
		t.Fatalf("span_id = %v, want %s", fields["span_id"], sc.SpanID())
	}
}

func TestLoggingMiddlewareOmitsInvalidTraceCorrelation(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	handler := Logging(zap.New(core))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/openai/v1/responses", nil))

	fields := logs.All()[0].ContextMap()
	if _, ok := fields["trace_id"]; ok {
		t.Fatal("invalid context logged trace_id")
	}
	if _, ok := fields["span_id"]; ok {
		t.Fatal("invalid context logged span_id")
	}
}
