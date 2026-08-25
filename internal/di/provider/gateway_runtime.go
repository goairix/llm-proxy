package provider

import (
	"context"

	"go.uber.org/zap"
	"gorm.io/gorm"

	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
	"github.com/goairix/llm-proxy/internal/infrastructure/config"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/database"
	runtimerepository "github.com/goairix/llm-proxy/internal/infrastructure/persistence/repository/runtime"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/transactions"
	snapshotruntime "github.com/goairix/llm-proxy/internal/infrastructure/snapshot"
)

// GatewayRuntime is the composition boundary shared by control-plane writes and gateway reads.
type GatewayRuntime struct {
	runtime  *snapshotruntime.Runtime
	Database *database.Runtime
	Manager  *transactions.Manager
}

// NewGatewayRuntime assembles the optional database and immutable snapshot lifecycle without starting workers.
func NewGatewayRuntime(cfg *config.Config, logger *zap.Logger) *GatewayRuntime {
	store := gatewaysnapshot.NewStore()
	if cfg == nil || !cfg.Gateway.Enabled {
		return &GatewayRuntime{runtime: snapshotruntime.NewRuntime(nil, nil, store)}
	}
	databaseRuntime := database.NewRuntime(func(ctx context.Context) (*gorm.DB, error) {
		return database.Open(ctx, cfg.Database)
	}, cfg.Gateway.RetryBackoff, logger)
	manager := transactions.NewManager(databaseRuntime)
	reader := runtimerepository.NewReader(manager)
	refresher := snapshotruntime.NewRefresher(
		reader,
		gatewaysnapshot.NewCompiler(),
		store,
		snapshotruntime.Options{
			PollInterval: cfg.Gateway.SnapshotInterval,
			LoadTimeout:  cfg.Gateway.SnapshotTimeout,
			RetryBackoff: cfg.Gateway.RetryBackoff,
		},
		logger,
	)
	return &GatewayRuntime{
		runtime:  snapshotruntime.NewRuntime(databaseRuntime, refresher, store),
		Database: databaseRuntime,
		Manager:  manager,
	}
}

func (r *GatewayRuntime) Start(ctx context.Context) {
	if r != nil && r.runtime != nil {
		r.runtime.Start(ctx)
	}
}

func (r *GatewayRuntime) NotifyRefresh() {
	if r != nil && r.runtime != nil {
		r.runtime.NotifyRefresh()
	}
}

func (r *GatewayRuntime) Stop(ctx context.Context) error {
	if r == nil || r.runtime == nil {
		return nil
	}
	return r.runtime.Stop(ctx)
}

func (r *GatewayRuntime) CloseDatabase() error {
	if r == nil || r.runtime == nil {
		return nil
	}
	return r.runtime.CloseDatabase()
}

func (r *GatewayRuntime) Store() *gatewaysnapshot.Store {
	if r == nil || r.runtime == nil {
		return nil
	}
	return r.runtime.Store()
}
