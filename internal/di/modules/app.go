package modules

import (
	"github.com/goairix/llm-proxy/internal/di/provider"
	"github.com/google/wire"
)

// AppSet contains the providers required by the process composition root.
var AppSet = wire.NewSet(
	provider.NewLogger,
	provider.NewTelemetry,
	provider.NewReadiness,
	provider.NewStats,
	provider.NewObserverFactory,
	provider.NewOpenAIHandler,
	provider.NewAnthropicHandler,
	provider.NewRootHandler,
	provider.NewHTTPServer,
)
