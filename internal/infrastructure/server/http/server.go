package httpserver

import (
	"context"
	"net/http"
	"time"

	appRuntime "github.com/goairix/llm-proxy/internal/application/runtime"
	"go.uber.org/zap"
)

// Server owns the standard library HTTP server lifecycle.
type Server struct {
	httpServer *http.Server
	logger     *zap.Logger
	readiness  *appRuntime.Readiness
}

// New creates an HTTP server with its runtime dependencies.
func New(addr string, handler http.Handler, logger *zap.Logger, readiness *appRuntime.Readiness) *Server {
	return &Server{
		httpServer: &http.Server{Addr: addr, Handler: handler},
		logger:     logger,
		readiness:  readiness,
	}
}

// Start begins listening and serving. It blocks until the server stops.
func (s *Server) Start() error {
	s.readiness.SetReady(true)
	defer s.readiness.SetReady(false)
	s.logger.Info("server starting", zap.String("addr", s.httpServer.Addr))
	return s.httpServer.ListenAndServe()
}

// Shutdown marks the server not ready and gracefully stops it with a 10-second timeout.
func (s *Server) Shutdown(ctx context.Context) error {
	s.readiness.SetReady(false)
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	s.logger.Info("server shutting down")
	return s.httpServer.Shutdown(shutdownCtx)
}
