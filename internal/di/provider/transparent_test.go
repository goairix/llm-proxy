package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	appRuntime "github.com/goairix/llm-proxy/internal/application/runtime"
	"github.com/goairix/llm-proxy/internal/infrastructure/config"
	"github.com/goairix/llm-proxy/internal/infrastructure/observability"
	"go.uber.org/zap"
)

func TestRateLimitMappingsPreserveValuesAndCopyMutableData(t *testing.T) {
	source := config.RateLimitConfig{
		Enabled:   true,
		Default:   config.RateLimitRule{RequestsPerSecond: 10, Burst: 20},
		Whitelist: []string{"sk-white"},
		Overrides: map[string]config.RateLimitRule{
			"sk-special": {RequestsPerSecond: 3, Burst: 4},
		},
	}
	middlewareConfig := toMiddlewareRateLimit(source)
	dashboardView := toDashboardRateLimit(source)

	source.Whitelist[0] = "changed"
	source.Overrides["sk-special"] = config.RateLimitRule{RequestsPerSecond: 99, Burst: 99}
	if !middlewareConfig.Enabled || middlewareConfig.Default.RequestsPerSecond != 10 || middlewareConfig.Default.Burst != 20 {
		t.Fatalf("default mapping = %+v", middlewareConfig)
	}
	if got := middlewareConfig.Whitelist[0]; got != "sk-white" {
		t.Fatalf("whitelist was not copied: %q", got)
	}
	if got := middlewareConfig.Overrides["sk-special"]; got.RequestsPerSecond != 3 || got.Burst != 4 {
		t.Fatalf("override mapping = %+v", got)
	}
	if !dashboardView.Enabled || dashboardView.RequestsPerSecond != 10 || dashboardView.Burst != 20 {
		t.Fatalf("dashboard mapping = %+v", dashboardView)
	}
}

func TestTransparentHandlerProviders(t *testing.T) {
	paths := make(chan string, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	cfg := &config.Config{Providers: config.ProvidersConfig{
		OpenAI:    config.ProviderConfig{BaseURL: upstream.URL},
		Anthropic: config.ProviderConfig{BaseURL: upstream.URL},
	}}
	telemetry, err := observability.New(context.Background(), config.ObservabilityConfig{}, appRuntime.Version, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	openAI, err := NewOpenAIHandler(cfg, telemetry)
	if err != nil {
		t.Fatal(err)
	}
	anthropic, err := NewAnthropicHandler(cfg, telemetry)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, path, wantPath string
		handler              http.Handler
	}{
		{name: "openai", path: "/openai/v1/models", wantPath: "/v1/models", handler: openAI.Handler},
		{name: "anthropic", path: "/anthropic/v1/messages", wantPath: "/v1/messages", handler: anthropic.Handler},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			tc.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, tc.path, nil))
			if recorder.Code != http.StatusOK || recorder.Body.String() != `{"ok":true}` {
				t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
			}
			if got := <-paths; got != tc.wantPath {
				t.Fatalf("path = %q, want %q", got, tc.wantPath)
			}
		})
	}
}
