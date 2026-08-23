package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/goairix/llm-proxy/internal/config"
	"github.com/goairix/llm-proxy/internal/logger"
	"github.com/goairix/llm-proxy/internal/server"
)

func main() {
	// Load config from config.yaml in current directory.
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// Initialise structured logger.
	log_, err := logger.New(cfg.Log)
	if err != nil {
		log.Fatalf("failed to init logger: %v", err)
	}
	defer log_.Sync() //nolint:errcheck

	// Build and configure the HTTP server.
	srv, err := server.New(cfg, log_)
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
	}

	// Start serving in a background goroutine.
	go func() {
		if err := srv.Start(); err != nil {
			log_.Error("server error", zap.Error(err))
		}
	}()

	// Block until a termination signal is received.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	// Graceful shutdown with a 30-second outer deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log_.Error("shutdown error", zap.Error(err))
	}
}
