// Package server is a temporary compatibility facade for the legacy main package.
// It is removed after the Wire composition root takes ownership of assembly.
package server

import (
	"context"
	"fmt"
	"net/http"

	appRuntime "github.com/goairix/llm-proxy/internal/application/runtime"
	"github.com/goairix/llm-proxy/internal/infrastructure/config"
	"github.com/goairix/llm-proxy/internal/infrastructure/observability"
	"github.com/goairix/llm-proxy/internal/infrastructure/proxy"
	"github.com/goairix/llm-proxy/internal/infrastructure/proxy/tokenusage"
	httpserver "github.com/goairix/llm-proxy/internal/infrastructure/server/http"
	"github.com/goairix/llm-proxy/internal/interfaces/http/handler/dashboard"
	"github.com/goairix/llm-proxy/internal/interfaces/http/middleware"
	"github.com/goairix/llm-proxy/internal/interfaces/http/router"
	"go.uber.org/zap"
)

// Version is kept for compatibility until main switches to application/runtime.Version.
const Version = appRuntime.Version

// Server delegates lifecycle operations to the infrastructure HTTP server.
type Server struct {
	delegate *httpserver.Server
}

// New assembles the legacy transparent proxy through the new adapters.
func New(cfg *config.Config, logger *zap.Logger, telemetry *observability.Runtime) (*Server, error) {
	readiness := appRuntime.NewReadiness()
	stats := &dashboard.Stats{}
	baseURL := cfg.Server.ShowBaseURL
	if baseURL == "" {
		baseURL = fmt.Sprintf("http://localhost:%d", cfg.Server.Port)
	}

	transport := telemetry.Transport(http.DefaultTransport)
	openAIProxy, err := proxy.NewOpenAIProxy(cfg.Providers.OpenAI.BaseURL, transport)
	if err != nil {
		return nil, fmt.Errorf("failed to create openai proxy: %w", err)
	}
	anthropicProxy, err := proxy.NewAnthropicProxy(cfg.Providers.Anthropic.BaseURL, transport)
	if err != nil {
		return nil, fmt.Errorf("failed to create anthropic proxy: %w", err)
	}

	handler := router.New(router.Config{
		BaseURL:   baseURL,
		Version:   Version,
		RateLimit: toMiddlewareRateLimit(cfg.RateLimit),
		RateView:  toDashboardRateLimit(cfg.RateLimit),
	}, router.Dependencies{
		Logger:          logger,
		Instrumenter:    telemetry,
		Readiness:       readiness,
		Stats:           stats,
		ObserverFactory: tokenusage.NewObserver,
		OpenAIProxy:     openAIProxy,
		AnthropicProxy:  anthropicProxy,
	})
	delegate := httpserver.New(fmt.Sprintf(":%d", cfg.Server.Port), handler, logger, readiness)
	return &Server{delegate: delegate}, nil
}

// Start delegates to the infrastructure HTTP server.
func (s *Server) Start() error {
	return s.delegate.Start()
}

// Shutdown delegates to the infrastructure HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.delegate.Shutdown(ctx)
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
