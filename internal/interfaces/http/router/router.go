package router

import (
	"net/http"

	controlport "github.com/goairix/llm-proxy/internal/application/controlplane/port"
	appRuntime "github.com/goairix/llm-proxy/internal/application/runtime"
	"github.com/goairix/llm-proxy/internal/interfaces/http/handler/controlplane"
	"github.com/goairix/llm-proxy/internal/interfaces/http/handler/dashboard"
	"github.com/goairix/llm-proxy/internal/interfaces/http/handler/health"
	"github.com/goairix/llm-proxy/internal/interfaces/http/middleware"
	"go.uber.org/zap"
)

// Instrumenter wraps provider handlers with server-side telemetry.
type Instrumenter interface {
	WrapHandler(provider string, next http.Handler) http.Handler
}

// Config contains the HTTP adapter configuration.
type Config struct {
	BaseURL   string
	Version   string
	RateLimit middleware.RateLimitConfig
	RateView  dashboard.RateLimitView
}

// Dependencies contains the handlers and shared runtime services assembled by DI.
type Dependencies struct {
	Logger           *zap.Logger
	Instrumenter     Instrumenter
	Readiness        *appRuntime.Readiness
	Stats            *dashboard.Stats
	ObserverFactory  appRuntime.UsageObserverFactory
	OpenAIProxy      http.Handler
	AnthropicProxy   http.Handler
	OpenAIGateway    http.Handler
	AnthropicGateway http.Handler
	ControlPlane     http.Handler
	ControlAuth      controlport.ControlPlaneAuthorizer
}

// New builds the HTTP routing tree without owning the server lifecycle.
func New(cfg Config, deps Dependencies) http.Handler {
	mux := http.NewServeMux()
	healthHandler := health.New(deps.Readiness, deps.Logger)
	mux.HandleFunc("GET /healthz", healthHandler.Health)
	mux.HandleFunc("/healthz", methodNotAllowed(http.MethodGet))
	mux.HandleFunc("GET /readyz", healthHandler.Ready)
	mux.HandleFunc("/readyz", methodNotAllowed(http.MethodGet))
	mux.Handle("/", dashboard.NewHandler(deps.Stats, cfg.RateView, cfg.Version, cfg.BaseURL))

	limiter := middleware.NewRateLimiter(cfg.RateLimit)
	logging := middleware.Logging(deps.Logger)
	openAI := logging(middleware.Stats("openai", deps.Stats, deps.ObserverFactory)(
		limiter.Handler("openai", deps.OpenAIProxy),
	))
	anthropic := logging(middleware.Stats("anthropic", deps.Stats, deps.ObserverFactory)(
		limiter.Handler("anthropic", deps.AnthropicProxy),
	))
	mux.Handle("/openai/", deps.Instrumenter.WrapHandler("openai", openAI))
	mux.Handle("/anthropic/", deps.Instrumenter.WrapHandler("anthropic", anthropic))
	if deps.OpenAIGateway != nil {
		openAIGateway := middleware.RequestID(
			middleware.GatewayLogging(deps.Logger, "openai", "chat.completions")(deps.OpenAIGateway),
		)
		mux.Handle("POST /v1/chat/completions", deps.Instrumenter.WrapHandler("openai", openAIGateway))
		mux.Handle("/v1/chat/completions", methodNotAllowed(http.MethodPost))
	}
	if deps.AnthropicGateway != nil {
		anthropicGateway := middleware.RequestID(
			middleware.GatewayLogging(deps.Logger, "anthropic", "messages")(deps.AnthropicGateway),
		)
		mux.Handle("POST /v1/messages", deps.Instrumenter.WrapHandler("anthropic", anthropicGateway))
		mux.Handle("/v1/messages", methodNotAllowed(http.MethodPost))
	}
	if deps.ControlPlane != nil {
		controlHandler := middleware.RequestID(
			middleware.ControlPlaneLogging(deps.Logger)(
				middleware.ControlPlaneAuth(deps.ControlAuth)(deps.ControlPlane),
			),
		)
		for _, pattern := range controlplane.ResourcePatterns {
			mux.Handle(pattern, controlHandler)
		}
	}
	return mux
}

func methodNotAllowed(allowed string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", allowed)
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}
