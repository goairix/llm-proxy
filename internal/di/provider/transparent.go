package provider

import (
	"fmt"
	"net/http"

	appRuntime "github.com/goairix/llm-proxy/internal/application/runtime"
	"github.com/goairix/llm-proxy/internal/infrastructure/config"
	"github.com/goairix/llm-proxy/internal/infrastructure/observability"
	"github.com/goairix/llm-proxy/internal/infrastructure/proxy"
	httpserver "github.com/goairix/llm-proxy/internal/infrastructure/server/http"
	"github.com/goairix/llm-proxy/internal/interfaces/http/handler/dashboard"
	"github.com/goairix/llm-proxy/internal/interfaces/http/middleware"
	"github.com/goairix/llm-proxy/internal/interfaces/http/router"
	"go.uber.org/zap"
)

// OpenAIHandler distinguishes the OpenAI proxy in the Wire dependency graph.
type OpenAIHandler struct{ http.Handler }

// AnthropicHandler distinguishes the Anthropic proxy in the Wire dependency graph.
type AnthropicHandler struct{ http.Handler }

// RootHandler distinguishes the assembled root router in the Wire dependency graph.
type RootHandler struct{ http.Handler }

// NewOpenAIHandler creates the existing transparent OpenAI proxy.
func NewOpenAIHandler(cfg *config.Config, telemetry *observability.Runtime) (OpenAIHandler, error) {
	handler, err := proxy.NewOpenAIProxy(cfg.Providers.OpenAI.BaseURL, telemetry.Transport(http.DefaultTransport))
	if err != nil {
		return OpenAIHandler{}, fmt.Errorf("create openai proxy: %w", err)
	}
	return OpenAIHandler{Handler: handler}, nil
}

// NewAnthropicHandler creates the existing transparent Anthropic proxy.
func NewAnthropicHandler(cfg *config.Config, telemetry *observability.Runtime) (AnthropicHandler, error) {
	handler, err := proxy.NewAnthropicProxy(cfg.Providers.Anthropic.BaseURL, telemetry.Transport(http.DefaultTransport))
	if err != nil {
		return AnthropicHandler{}, fmt.Errorf("create anthropic proxy: %w", err)
	}
	return AnthropicHandler{Handler: handler}, nil
}

// NewRootHandler assembles the HTTP adapter tree.
func NewRootHandler(
	cfg *config.Config,
	logger *zap.Logger,
	telemetry *observability.Runtime,
	readiness *appRuntime.Readiness,
	stats *dashboard.Stats,
	observers appRuntime.UsageObserverFactory,
	openAI OpenAIHandler,
	anthropic AnthropicHandler,
) RootHandler {
	baseURL := cfg.Server.ShowBaseURL
	if baseURL == "" {
		baseURL = fmt.Sprintf("http://localhost:%d", cfg.Server.Port)
	}
	handler := router.New(router.Config{
		BaseURL:   baseURL,
		Version:   appRuntime.Version,
		RateLimit: toMiddlewareRateLimit(cfg.RateLimit),
		RateView:  toDashboardRateLimit(cfg.RateLimit),
	}, router.Dependencies{
		Logger:          logger,
		Instrumenter:    telemetry,
		Readiness:       readiness,
		Stats:           stats,
		ObserverFactory: observers,
		OpenAIProxy:     openAI.Handler,
		AnthropicProxy:  anthropic.Handler,
	})
	return RootHandler{Handler: handler}
}

// NewHTTPServer creates the HTTP lifecycle adapter.
func NewHTTPServer(
	cfg *config.Config,
	root RootHandler,
	logger *zap.Logger,
	readiness *appRuntime.Readiness,
) *httpserver.Server {
	return httpserver.New(fmt.Sprintf(":%d", cfg.Server.Port), root.Handler, logger, readiness)
}

func toMiddlewareRateLimit(cfg config.RateLimitConfig) middleware.RateLimitConfig {
	overrides := make(map[string]middleware.RateLimitRule, len(cfg.Overrides))
	for key, rule := range cfg.Overrides {
		overrides[key] = middleware.RateLimitRule{
			RequestsPerSecond: rule.RequestsPerSecond,
			Burst:             rule.Burst,
		}
	}
	return middleware.RateLimitConfig{
		Enabled: cfg.Enabled,
		Default: middleware.RateLimitRule{
			RequestsPerSecond: cfg.Default.RequestsPerSecond,
			Burst:             cfg.Default.Burst,
		},
		Whitelist: append([]string(nil), cfg.Whitelist...),
		Overrides: overrides,
	}
}

func toDashboardRateLimit(cfg config.RateLimitConfig) dashboard.RateLimitView {
	return dashboard.RateLimitView{
		Enabled:           cfg.Enabled,
		RequestsPerSecond: cfg.Default.RequestsPerSecond,
		Burst:             cfg.Default.Burst,
	}
}
