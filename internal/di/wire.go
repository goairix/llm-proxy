//go:build wireinject

package di

import (
	"context"

	"github.com/goairix/llm-proxy/internal/di/modules"
	"github.com/goairix/llm-proxy/internal/infrastructure/config"
	"github.com/google/wire"
)

// Initialize assembles the application dependency graph.
func Initialize(ctx context.Context, cfg *config.Config) (*App, error) {
	wire.Build(modules.AppSet, wire.Struct(new(App), "*"))
	return nil, nil
}
