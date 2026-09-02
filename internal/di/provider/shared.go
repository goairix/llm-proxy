package provider

import (
	"context"
	"fmt"

	appRuntime "github.com/goairix/llm-proxy/internal/application/runtime"
	"github.com/goairix/llm-proxy/internal/infrastructure/config"
	infralogger "github.com/goairix/llm-proxy/internal/infrastructure/logger"
	"github.com/goairix/llm-proxy/internal/infrastructure/observability"
	"github.com/goairix/llm-proxy/internal/infrastructure/proxy/tokenusage"
	credentialsecurity "github.com/goairix/llm-proxy/internal/infrastructure/security/credential"
	"github.com/goairix/llm-proxy/internal/interfaces/http/handler/dashboard"
	"go.uber.org/zap"
)

type CredentialCipherRuntime struct{ Cipher *credentialsecurity.Cipher }

func NewCredentialCipherRuntime(cfg *config.Config) (*CredentialCipherRuntime, error) {
	runtime := &CredentialCipherRuntime{}
	if cfg == nil || !cfg.Gateway.Enabled {
		return runtime, nil
	}
	cipher, err := credentialsecurity.NewCipher(cfg.CredentialEncryption.CurrentKeyVersion, cfg.CredentialEncryption.Keys)
	if err != nil {
		return nil, fmt.Errorf("create shared credential cipher: %w", err)
	}
	runtime.Cipher = cipher
	return runtime, nil
}

// NewLogger creates the process logger from infrastructure configuration.
func NewLogger(cfg *config.Config) (*zap.Logger, error) {
	return infralogger.New(cfg.Log)
}

// NewTelemetry creates the observability runtime.
func NewTelemetry(ctx context.Context, cfg *config.Config, logger *zap.Logger) (*observability.Runtime, error) {
	return observability.New(ctx, cfg.Observability, appRuntime.Version, logger)
}

// NewReadiness creates the shared readiness state.
func NewReadiness() *appRuntime.Readiness {
	return appRuntime.NewReadiness()
}

// NewStats creates the in-memory dashboard counters.
func NewStats() *dashboard.Stats {
	return &dashboard.Stats{}
}

// NewObserverFactory exposes the transparent proxy token observer through its application port.
func NewObserverFactory() appRuntime.UsageObserverFactory {
	return tokenusage.NewObserver
}
