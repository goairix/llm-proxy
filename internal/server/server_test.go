package server

import (
	"context"
	"testing"

	"github.com/goairix/llm-proxy/internal/infrastructure/config"
	"github.com/goairix/llm-proxy/internal/infrastructure/observability"
	"go.uber.org/zap"
)

func TestNewCompatibilityServer(t *testing.T) {
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
	server, err := New(cfg, zap.NewNop(), telemetry)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if server == nil {
		t.Fatal("New() returned nil server")
	}
}
