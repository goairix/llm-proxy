package di

import (
	"github.com/goairix/llm-proxy/internal/infrastructure/observability"
	httpserver "github.com/goairix/llm-proxy/internal/infrastructure/server/http"
	"go.uber.org/zap"
)

// App contains the process lifecycle dependencies assembled by Wire.
type App struct {
	Server    *httpserver.Server
	Telemetry *observability.Runtime
	Logger    *zap.Logger
}
