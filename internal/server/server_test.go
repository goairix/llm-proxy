package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/goairix/llm-proxy/internal/infrastructure/config"
	"github.com/goairix/llm-proxy/internal/infrastructure/observability"
	"github.com/goairix/llm-proxy/internal/interfaces/http/handler/dashboard"
	"go.uber.org/zap"
)

func TestStatsMiddleware_TokenJSON(t *testing.T) {
	const body = `{"id":"resp_1","usage":{"input_tokens":12,"output_tokens":4,"input_tokens_details":{"cached_tokens":3}}}`
	stats := &dashboard.Stats{}
	handler := statsMiddleware("openai", stats, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil))

	if got := recorder.Body.String(); got != body {
		t.Fatalf("response body = %q, want %q", got, body)
	}
	assertTokenStats(t, &stats.Tokens.OpenAI, 12, 4, 3, 0, 0, 0)
	assertTokenStats(t, &stats.Tokens.Total, 12, 4, 3, 0, 0, 0)
}

func TestStatsMiddleware_TokenMissing(t *testing.T) {
	t.Run("successful eligible response", func(t *testing.T) {
		stats := &dashboard.Stats{}
		handler := statsMiddleware("openai", stats, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"resp_1"}`)
		}))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil))
		assertTokenStats(t, &stats.Tokens.OpenAI, 0, 0, 0, 0, 0, 1)
	})

	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			stats := &dashboard.Stats{}
			handler := statsMiddleware("openai", stats, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil))
			assertTokenStats(t, &stats.Tokens.OpenAI, 0, 0, 0, 0, 0, 0)
		})
	}

	t.Run("ineligible retrieve", func(t *testing.T) {
		stats := &dashboard.Stats{}
		handler := statsMiddleware("openai", stats, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"usage":{"input_tokens":99,"output_tokens":99}}`)
		}))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/openai/v1/responses/resp_1", nil))
		assertTokenStats(t, &stats.Tokens.OpenAI, 0, 0, 0, 0, 0, 0)
	})
}

func TestStatsMiddleware_TokenStreamingTransparent(t *testing.T) {
	const firstEvent = "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"cache_creation_input_tokens\":2,\"cache_read_input_tokens\":3,\"output_tokens\":1}}}\n\n"
	const rest = "event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":8,\"output_tokens_details\":{\"thinking_tokens\":4}}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	stats := &dashboard.Stats{}
	release := make(chan struct{})
	handlerDone := make(chan struct{})
	handler := statsMiddleware("anthropic", stats, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		defer close(handlerDone)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, firstEvent)
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, rest)
		w.(http.Flusher).Flush()
	}))
	server := httptest.NewServer(handler)
	defer server.Close()

	type clientResult struct {
		body string
		err  error
	}
	clientDone := make(chan clientResult, 1)
	go func() {
		response, err := http.Post(server.URL+"/anthropic/v1/messages", "application/json", strings.NewReader(`{}`))
		if err != nil {
			clientDone <- clientResult{err: err}
			return
		}
		defer response.Body.Close()
		first := make([]byte, len(firstEvent))
		if _, err := io.ReadFull(response.Body, first); err != nil {
			clientDone <- clientResult{err: err}
			return
		}
		if string(first) != firstEvent {
			clientDone <- clientResult{err: assertServerError("first SSE event changed")}
			return
		}
		select {
		case <-handlerDone:
			clientDone <- clientResult{err: assertServerError("handler ended before first event was readable")}
			return
		default:
		}
		close(release)
		tail, err := io.ReadAll(response.Body)
		clientDone <- clientResult{body: string(first) + string(tail), err: err}
	}()

	select {
	case result := <-clientDone:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.body != firstEvent+rest {
			t.Fatalf("stream = %q, want unchanged stream", result.body)
		}
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("timed out waiting for flushed SSE event")
	}

	assertTokenStats(t, &stats.Tokens.Anthropic, 15, 8, 3, 2, 4, 0)
}

func assertTokenStats(t *testing.T, stats *dashboard.TokenStats, input, output, cacheRead, cacheWrite, reasoning, missing int64) {
	t.Helper()
	if got := stats.Input.Load(); got != input {
		t.Errorf("Input = %d, want %d", got, input)
	}
	if got := stats.Output.Load(); got != output {
		t.Errorf("Output = %d, want %d", got, output)
	}
	if got := stats.CacheRead.Load(); got != cacheRead {
		t.Errorf("CacheRead = %d, want %d", got, cacheRead)
	}
	if got := stats.CacheWrite.Load(); got != cacheWrite {
		t.Errorf("CacheWrite = %d, want %d", got, cacheWrite)
	}
	if got := stats.Reasoning.Load(); got != reasoning {
		t.Errorf("Reasoning = %d, want %d", got, reasoning)
	}
	if got := stats.Missing.Load(); got != missing {
		t.Errorf("Missing = %d, want %d", got, missing)
	}
}

type assertServerError string

func (e assertServerError) Error() string { return string(e) }

func TestServerHealth(t *testing.T) {
	telemetry, err := observability.New(context.Background(), config.ObservabilityConfig{}, Version, zap.NewNop())
	if err != nil {
		t.Fatalf("observability.New() error = %v", err)
	}
	cfg := &config.Config{
		Server:    config.ServerConfig{Port: 8080},
		RateLimit: config.RateLimitConfig{Enabled: false},
		Providers: config.ProvidersConfig{
			OpenAI:    config.ProviderConfig{BaseURL: "https://api.openai.com"},
			Anthropic: config.ProviderConfig{BaseURL: "https://api.anthropic.com"},
		},
	}
	srv, err := New(cfg, zap.NewNop(), telemetry)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	tests := []struct {
		name, method, path, wantBody string
		wantStatus                   int
	}{
		{name: "health", method: http.MethodGet, path: "/healthz", wantStatus: http.StatusOK, wantBody: "ok"},
		{name: "not ready", method: http.MethodGet, path: "/readyz", wantStatus: http.StatusServiceUnavailable, wantBody: "not_ready"},
		{name: "health method", method: http.MethodPost, path: "/healthz", wantStatus: http.StatusMethodNotAllowed},
		{name: "ready method", method: http.MethodPost, path: "/readyz", wantStatus: http.StatusMethodNotAllowed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			srv.httpServer.Handler.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, nil))
			if got := recorder.Code; got != tc.wantStatus {
				t.Fatalf("status = %d, want %d", got, tc.wantStatus)
			}
			if tc.wantBody != "" {
				var body struct {
					Status string `json:"status"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode body: %v", err)
				}
				if body.Status != tc.wantBody {
					t.Fatalf("body status = %q, want %q", body.Status, tc.wantBody)
				}
			}
		})
	}

	srv.ready.Store(true)
	readyRecorder := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(readyRecorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if readyRecorder.Code != http.StatusOK || !strings.Contains(readyRecorder.Body.String(), `"status":"ready"`) {
		t.Fatalf("ready response = %d %q", readyRecorder.Code, readyRecorder.Body.String())
	}

	dashboardRecorder := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(dashboardRecorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if dashboardRecorder.Code != http.StatusOK || !strings.Contains(dashboardRecorder.Body.String(), "LLM 代理控制台") {
		t.Fatalf("dashboard response = %d", dashboardRecorder.Code)
	}
	if !strings.Contains(dashboardRecorder.Body.String(), `"stats":{"total":0`) {
		t.Fatalf("health requests changed dashboard stats")
	}
}

func TestServerTransparentProviderRoutes(t *testing.T) {
	type upstreamRequest struct {
		path          string
		authorization string
		anthropicKey  string
	}
	received := make(chan upstreamRequest, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- upstreamRequest{
			path:          r.URL.EscapedPath(),
			authorization: r.Header.Get("Authorization"),
			anthropicKey:  r.Header.Get("x-api-key"),
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	telemetry, err := observability.New(context.Background(), config.ObservabilityConfig{}, Version, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Server:    config.ServerConfig{Port: 8080},
		RateLimit: config.RateLimitConfig{Enabled: false},
		Providers: config.ProvidersConfig{
			OpenAI:    config.ProviderConfig{BaseURL: upstream.URL},
			Anthropic: config.ProviderConfig{BaseURL: upstream.URL},
		},
	}
	srv, err := New(cfg, zap.NewNop(), telemetry)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name          string
		path          string
		authorization string
		anthropicKey  string
		wantPath      string
	}{
		{name: "openai", path: "/openai/v1/chat/completions", authorization: "Bearer sk-openai", wantPath: "/v1/chat/completions"},
		{name: "anthropic", path: "/anthropic/v1/messages", anthropicKey: "sk-ant", wantPath: "/v1/messages"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{}`))
			req.Header.Set("Authorization", tc.authorization)
			req.Header.Set("x-api-key", tc.anthropicKey)
			rec := httptest.NewRecorder()
			srv.httpServer.Handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || rec.Body.String() != `{"ok":true}` {
				t.Fatalf("response = %d %q", rec.Code, rec.Body.String())
			}
			got := <-received
			if got.path != tc.wantPath || got.authorization != tc.authorization || got.anthropicKey != tc.anthropicKey {
				t.Fatalf("upstream request = %+v", got)
			}
		})
	}
}
