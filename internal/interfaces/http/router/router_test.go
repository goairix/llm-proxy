package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appRuntime "github.com/goairix/llm-proxy/internal/application/runtime"
	"github.com/goairix/llm-proxy/internal/infrastructure/proxy"
	"github.com/goairix/llm-proxy/internal/infrastructure/proxy/tokenusage"
	"github.com/goairix/llm-proxy/internal/interfaces/http/handler/dashboard"
	"github.com/goairix/llm-proxy/internal/interfaces/http/middleware"
	"go.uber.org/zap"
)

type identityInstrumenter struct{}

func (identityInstrumenter) WrapHandler(_ string, next http.Handler) http.Handler { return next }

func TestRouterUnifiedGatewayRoutesAreExactAndOptional(t *testing.T) {
	openAICalls := 0
	responsesCalls := 0
	anthropicCalls := 0
	handler := New(Config{BaseURL: "http://localhost:8080", Version: "1.0.0"}, Dependencies{
		Logger:          zap.NewNop(),
		Instrumenter:    identityInstrumenter{},
		Readiness:       appRuntime.NewReadiness(),
		Stats:           &dashboard.Stats{},
		ObserverFactory: tokenusage.NewObserver,
		OpenAIProxy:     http.NotFoundHandler(),
		AnthropicProxy:  http.NotFoundHandler(),
		OpenAIGateway: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			openAICalls++
			w.WriteHeader(http.StatusCreated)
		}),
		OpenAIResponsesGateway: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			responsesCalls++
			w.WriteHeader(http.StatusNonAuthoritativeInfo)
		}),
		AnthropicGateway: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			anthropicCalls++
			w.WriteHeader(http.StatusAccepted)
		}),
	})

	for _, test := range []struct {
		path       string
		wantStatus int
	}{
		{path: "/v1/chat/completions", wantStatus: http.StatusCreated},
		{path: "/v1/responses", wantStatus: http.StatusNonAuthoritativeInfo},
		{path: "/v1/messages", wantStatus: http.StatusAccepted},
	} {
		request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(`{}`))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != test.wantStatus {
			t.Fatalf("POST %s status=%d, want %d", test.path, recorder.Code, test.wantStatus)
		}
	}
	if openAICalls != 1 || responsesCalls != 1 || anthropicCalls != 1 {
		t.Fatalf("gateway calls openai=%d responses=%d anthropic=%d", openAICalls, responsesCalls, anthropicCalls)
	}

	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != http.MethodPost {
			t.Fatalf("GET %s status=%d allow=%q", path, recorder.Code, recorder.Header().Get("Allow"))
		}
	}
	unknown := httptest.NewRecorder()
	handler.ServeHTTP(unknown, httptest.NewRequest(http.MethodPost, "/v1/chat/completions/extra", nil))
	if unknown.Code != http.StatusOK || !strings.Contains(unknown.Body.String(), "LLM 代理控制台") {
		t.Fatalf("non-exact route status=%d body=%s", unknown.Code, unknown.Body.String())
	}

	disabled := New(Config{BaseURL: "http://localhost:8080", Version: "1.0.0"}, Dependencies{
		Logger: zap.NewNop(), Instrumenter: identityInstrumenter{}, Readiness: appRuntime.NewReadiness(),
		Stats: &dashboard.Stats{}, ObserverFactory: tokenusage.NewObserver,
		OpenAIProxy: http.NotFoundHandler(), AnthropicProxy: http.NotFoundHandler(),
	})
	recorder := httptest.NewRecorder()
	disabled.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "LLM 代理控制台") {
		t.Fatalf("disabled route status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRouterTransparentProviderRoutes(t *testing.T) {
	type upstreamRequest struct {
		path          string
		authorization string
		anthropicKey  string
	}
	received := make(chan upstreamRequest, 3)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- upstreamRequest{
			path:          r.URL.EscapedPath(),
			authorization: r.Header.Get("Authorization"),
			anthropicKey:  r.Header.Get("x-api-key"),
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	openAI, err := proxy.NewOpenAIProxy(upstream.URL, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	anthropic, err := proxy.NewAnthropicProxy(upstream.URL, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	stats := &dashboard.Stats{}
	readiness := appRuntime.NewReadiness()
	handler := New(Config{
		BaseURL:   "http://localhost:8080",
		Version:   "1.0.0",
		RateLimit: middleware.RateLimitConfig{Enabled: false},
		RateView:  dashboard.RateLimitView{Enabled: false},
	}, Dependencies{
		Logger:          zap.NewNop(),
		Instrumenter:    identityInstrumenter{},
		Readiness:       readiness,
		Stats:           stats,
		ObserverFactory: tokenusage.NewObserver,
		OpenAIProxy:     openAI,
		AnthropicProxy:  anthropic,
	})

	tests := []struct {
		name          string
		path          string
		authorization string
		anthropicKey  string
		wantPath      string
	}{
		{name: "openai", path: "/openai/v1/chat/completions", authorization: "Bearer sk-openai", wantPath: "/v1/chat/completions"},
		{name: "openai responses", path: "/openai/v1/responses", authorization: "Bearer sk-openai", wantPath: "/v1/responses"},
		{name: "anthropic", path: "/anthropic/v1/messages", anthropicKey: "sk-ant", wantPath: "/v1/messages"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{}`))
			req.Header.Set("Authorization", tc.authorization)
			req.Header.Set("x-api-key", tc.anthropicKey)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK || recorder.Body.String() != `{"ok":true}` {
				t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
			}
			got := <-received
			if got.path != tc.wantPath || got.authorization != tc.authorization || got.anthropicKey != tc.anthropicKey {
				t.Fatalf("upstream request = %+v", got)
			}
		})
	}

	if got := stats.Total.Load(); got != 3 {
		t.Fatalf("proxy total = %d, want 3", got)
	}
	healthRecorder := httptest.NewRecorder()
	handler.ServeHTTP(healthRecorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if healthRecorder.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", healthRecorder.Code)
	}
	if got := stats.Total.Load(); got != 3 {
		t.Fatalf("health request changed stats total to %d", got)
	}
}

func TestRouterHealthMethodsReadinessAndDashboardFallback(t *testing.T) {
	readiness := appRuntime.NewReadiness()
	handler := New(Config{
		BaseURL: "http://localhost:8080",
		Version: "1.0.0",
	}, Dependencies{
		Logger:          zap.NewNop(),
		Instrumenter:    identityInstrumenter{},
		Readiness:       readiness,
		Stats:           &dashboard.Stats{},
		ObserverFactory: tokenusage.NewObserver,
		OpenAIProxy:     http.NotFoundHandler(),
		AnthropicProxy:  http.NotFoundHandler(),
	})

	methodRecorder := httptest.NewRecorder()
	handler.ServeHTTP(methodRecorder, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if methodRecorder.Code != http.StatusMethodNotAllowed || methodRecorder.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("method response = %d Allow=%q", methodRecorder.Code, methodRecorder.Header().Get("Allow"))
	}

	readyRecorder := httptest.NewRecorder()
	handler.ServeHTTP(readyRecorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if readyRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("not-ready status = %d, want 503", readyRecorder.Code)
	}
	readiness.SetReady(true)
	readyRecorder = httptest.NewRecorder()
	handler.ServeHTTP(readyRecorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if readyRecorder.Code != http.StatusOK {
		t.Fatalf("ready status = %d, want 200", readyRecorder.Code)
	}

	fallbackRecorder := httptest.NewRecorder()
	handler.ServeHTTP(fallbackRecorder, httptest.NewRequest(http.MethodGet, "/unknown", nil))
	if fallbackRecorder.Code != http.StatusOK || !strings.Contains(fallbackRecorder.Body.String(), "LLM 代理控制台") {
		t.Fatalf("fallback response = %d", fallbackRecorder.Code)
	}
}

func TestRouterMountsOnlyKnownControlPlaneResources(t *testing.T) {
	controlCalls := 0
	controlPlane := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		controlCalls++
		w.WriteHeader(http.StatusNoContent)
	})
	handler := New(Config{BaseURL: "http://localhost:8080", Version: "1.0.0"}, Dependencies{
		Logger:          zap.NewNop(),
		Instrumenter:    identityInstrumenter{},
		Readiness:       appRuntime.NewReadiness(),
		Stats:           &dashboard.Stats{},
		ObserverFactory: tokenusage.NewObserver,
		OpenAIProxy:     http.NotFoundHandler(),
		AnthropicProxy:  http.NotFoundHandler(),
		ControlPlane:    controlPlane,
		ControlAuth:     exactTokenAuthorizer("management-token"),
	})

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/organizations", nil))
	if unauthorized.Code != http.StatusUnauthorized || controlCalls != 0 {
		t.Fatalf("unauthorized response=%d calls=%d", unauthorized.Code, controlCalls)
	}

	authorizedRequest := httptest.NewRequest(http.MethodGet, "/v1/organizations", nil)
	authorizedRequest.Header.Set("Authorization", "Bearer management-token")
	authorized := httptest.NewRecorder()
	handler.ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusNoContent || controlCalls != 1 || authorized.Header().Get("x-request-id") == "" {
		t.Fatalf("authorized response=%d calls=%d request_id=%q", authorized.Code, controlCalls, authorized.Header().Get("x-request-id"))
	}

	unknown := httptest.NewRecorder()
	handler.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, "/v1/not-a-control-resource", nil))
	if unknown.Code != http.StatusOK || !strings.Contains(unknown.Body.String(), "LLM 代理控制台") {
		t.Fatalf("unknown response=%d body=%s", unknown.Code, unknown.Body.String())
	}
}

type exactTokenAuthorizer string

func (a exactTokenAuthorizer) Authorize(token string) bool { return token == string(a) }
