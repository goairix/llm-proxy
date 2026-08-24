package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/goairix/llm-proxy/internal/config"
	"github.com/goairix/llm-proxy/internal/dashboard"
	"github.com/goairix/llm-proxy/internal/middleware"
	"github.com/goairix/llm-proxy/internal/observability"
	"github.com/goairix/llm-proxy/internal/proxy"
	"github.com/goairix/llm-proxy/internal/tokenusage"
)

// Version is the current server version.
const Version = "1.0.0"

// Server wraps the standard library HTTP server and holds a logger.
type Server struct {
	httpServer *http.Server
	logger     *zap.Logger
}

// New creates and configures the HTTP server with all routes and middleware.
func New(cfg *config.Config, logger *zap.Logger, telemetry *observability.Runtime) (*Server, error) {
	mux := http.NewServeMux()

	// Dashboard
	stats := &dashboard.Stats{}
	baseURL := cfg.Server.ShowBaseURL
	if baseURL == "" {
		baseURL = fmt.Sprintf("http://localhost:%d", cfg.Server.Port)
	}
	dashHandler := dashboard.NewHandler(stats, cfg.RateLimit, Version, baseURL)
	mux.Handle("/", dashHandler)

	// Rate limiter shared across providers
	rateLimiter := middleware.NewRateLimiter(cfg.RateLimit)

	// Logging middleware (outermost layer)
	loggingMiddleware := middleware.Logging(logger)
	transport := telemetry.Transport(http.DefaultTransport)

	// OpenAI proxy
	openaiProxy, err := proxy.NewOpenAIProxy(cfg.Providers.OpenAI.BaseURL, transport)
	if err != nil {
		return nil, fmt.Errorf("failed to create openai proxy: %w", err)
	}
	mux.Handle("/openai/", telemetry.WrapHandler("openai", loggingMiddleware(
		statsMiddleware("openai", stats,
			rateLimiter.Handler("openai", openaiProxy),
		),
	)))

	// Anthropic proxy
	anthropicProxy, err := proxy.NewAnthropicProxy(cfg.Providers.Anthropic.BaseURL, transport)
	if err != nil {
		return nil, fmt.Errorf("failed to create anthropic proxy: %w", err)
	}
	mux.Handle("/anthropic/", telemetry.WrapHandler("anthropic", loggingMiddleware(
		statsMiddleware("anthropic", stats,
			rateLimiter.Handler("anthropic", anthropicProxy),
		),
	)))

	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Server.Port),
		Handler: mux,
	}

	return &Server{
		httpServer: httpServer,
		logger:     logger,
	}, nil
}

// Start begins listening and serving HTTP requests. It blocks until the server
// is closed. Returns http.ErrServerClosed on graceful shutdown.
func (s *Server) Start() error {
	s.logger.Info("server starting", zap.String("addr", s.httpServer.Addr))
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server with a 10-second timeout.
func (s *Server) Shutdown(ctx context.Context) error {
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	s.logger.Info("server shutting down")
	return s.httpServer.Shutdown(shutdownCtx)
}

// statsResponseWriter captures status code and response bytes for metrics.
type statsResponseWriter struct {
	http.ResponseWriter
	status   int
	bytes    int
	observer tokenusage.Observer
	writeErr error
}

func (w *statsResponseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statsResponseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	if n > 0 && w.observer != nil {
		w.observer.Observe(w.Header().Get("Content-Type"), b[:n])
	}
	if err != nil && w.writeErr == nil {
		w.writeErr = err
	}
	return n, err
}

func (w *statsResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// statsMiddleware records per-request metrics into stats.
// Placed outside the rate limiter so every request (including 429s) is counted.
func statsMiddleware(provider string, stats *dashboard.Stats, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats.Total.Add(1)
		switch provider {
		case "openai":
			stats.OpenAI.Add(1)
		case "anthropic":
			stats.Anthropic.Add(1)
		}
		if r.ContentLength > 0 {
			stats.ReqBytes.Add(r.ContentLength)
		}

		start := time.Now()
		observer := tokenusage.NewObserver(provider, r.Method, r.URL.Path)
		srw := &statsResponseWriter{
			ResponseWriter: w,
			status:         http.StatusOK,
			observer:       observer,
		}
		next.ServeHTTP(srw, r)

		if observer != nil && srw.status >= 200 && srw.status < 300 {
			result := observer.Finish(srw.status, srw.writeErr)
			if result.Present {
				stats.AddTokenUsage(provider, result.Usage)
			} else {
				stats.AddMissingUsage(provider)
			}
		}

		stats.TotalLatencyMs.Add(time.Since(start).Milliseconds())
		stats.RespBytes.Add(int64(srw.bytes))

		switch srw.status {
		case http.StatusTooManyRequests:
			stats.RateLimited.Add(1)
		default:
			if srw.status >= 400 {
				stats.Errors.Add(1)
			}
		}
	})
}
