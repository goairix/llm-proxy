package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/goairix/llm-proxy/internal/di"
	"github.com/goairix/llm-proxy/internal/infrastructure/config"
)

func main() {
	// Load config from config.yaml in current directory.
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	app, err := di.Initialize(context.Background(), cfg)
	if err != nil {
		log.Fatalf("failed to initialize application: %v", err)
	}
	defer app.Logger.Sync() //nolint:errcheck
	processContext, stopProcess := context.WithCancel(context.Background())
	defer stopProcess()
	app.Gateway.Start(processContext)

	// Start serving in a background goroutine.
	go func() {
		if err := app.Server.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			app.Logger.Error("server error", zap.Error(err))
		}
	}()

	// Block until a termination signal is received.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	// Graceful shutdown with a 30-second outer deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	shutdownApplication(ctx, app.Logger, app.Server, app.Gateway, app.Telemetry)
}

type shutdownServer interface {
	MarkNotReady()
	Shutdown(context.Context) error
}

type shutdownGateway interface {
	Stop(context.Context) error
	CloseDatabase() error
}

type shutdownTelemetry interface {
	Shutdown(context.Context) error
}

func shutdownApplication(ctx context.Context, logger *zap.Logger, server shutdownServer, gateway shutdownGateway, telemetry shutdownTelemetry) {
	server.MarkNotReady()
	if err := gateway.Stop(ctx); err != nil {
		logger.Error("gateway runtime shutdown error", zap.Error(err))
	}
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("shutdown error", zap.Error(err))
	}
	if err := gateway.CloseDatabase(); err != nil {
		logger.Error("database shutdown error", zap.Error(err))
	}
	if err := telemetry.Shutdown(ctx); err != nil {
		logger.Error("telemetry shutdown error", zap.Error(err))
	}
}
