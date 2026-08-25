package provider

import (
	"context"
	"net/http"
	"sync"

	controlport "github.com/goairix/llm-proxy/internal/application/controlplane/port"
	controlservice "github.com/goairix/llm-proxy/internal/application/controlplane/service"
	"github.com/goairix/llm-proxy/internal/infrastructure/config"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/database"
	catalogrepository "github.com/goairix/llm-proxy/internal/infrastructure/persistence/repository/catalog"
	tenancyrepository "github.com/goairix/llm-proxy/internal/infrastructure/persistence/repository/tenancy"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/transactions"
	"github.com/goairix/llm-proxy/internal/infrastructure/security/controltoken"
	credentialsecurity "github.com/goairix/llm-proxy/internal/infrastructure/security/credential"
	"github.com/goairix/llm-proxy/internal/infrastructure/security/virtualkey"
	controlhandler "github.com/goairix/llm-proxy/internal/interfaces/http/handler/controlplane"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// ControlPlaneRuntime groups optional control-plane dependencies and their database lifecycle.
type ControlPlaneRuntime struct {
	Handler    http.Handler
	Authorizer controlport.ControlPlaneAuthorizer
	Database   *database.Runtime

	startOnce sync.Once
	stopOnce  sync.Once
	cancel    context.CancelFunc
	wait      sync.WaitGroup
}

// NewControlPlaneRuntime builds the control plane without opening a database connection.
func NewControlPlaneRuntime(cfg *config.Config, logger *zap.Logger) (*ControlPlaneRuntime, error) {
	runtime := &ControlPlaneRuntime{}
	if cfg == nil || !cfg.Gateway.Enabled {
		return runtime, nil
	}

	databaseRuntime := database.NewRuntime(func(ctx context.Context) (*gorm.DB, error) {
		return database.Open(ctx, cfg.Database)
	}, cfg.Gateway.RetryBackoff, logger)
	manager := transactions.NewManager(databaseRuntime)

	organizations := tenancyrepository.NewOrganizationRepository(manager)
	projects := tenancyrepository.NewProjectRepository(manager)
	virtualKeys := tenancyrepository.NewVirtualKeyRepository(manager)
	providers := catalogrepository.NewProviderRepository(manager)
	credentials := catalogrepository.NewProviderCredentialRepository(manager)
	deployments := catalogrepository.NewDeploymentRepository(manager)
	aliases := catalogrepository.NewModelAliasRepository(manager)
	targets := catalogrepository.NewRouteTargetRepository(manager)
	revisions := catalogrepository.NewConfigRevisionRepository(manager)

	cipher, err := credentialsecurity.NewCipher(cfg.CredentialEncryption.CurrentKeyVersion, cfg.CredentialEncryption.Keys)
	if err != nil {
		return nil, err
	}
	tenancy := controlservice.NewTenancyService(organizations, projects, virtualKeys, revisions, manager, virtualkey.NewGenerator())
	catalog := controlservice.NewCatalogService(
		providers, credentials, deployments, aliases, targets, organizations, projects,
		revisions, manager, cipher,
	)

	runtime.Handler = controlhandler.New(controlhandler.Dependencies{Tenancy: tenancy, Catalog: catalog})
	runtime.Authorizer = controltoken.NewAuthorizer(cfg.ControlPlane.Token)
	runtime.Database = databaseRuntime
	return runtime, nil
}

// Start begins the non-blocking database connection loop once.
func (r *ControlPlaneRuntime) Start(ctx context.Context) {
	if r == nil || r.Database == nil {
		return
	}
	r.startOnce.Do(func() {
		var runContext context.Context
		runContext, r.cancel = context.WithCancel(ctx)
		r.wait.Add(1)
		go func() {
			defer r.wait.Done()
			r.Database.Run(runContext)
		}()
	})
}

// Stop cancels and joins the database connection loop without closing active connections.
func (r *ControlPlaneRuntime) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		r.wait.Wait()
	})
}

// Close releases the published database connection after HTTP shutdown.
func (r *ControlPlaneRuntime) Close() error {
	if r == nil || r.Database == nil {
		return nil
	}
	return r.Database.Close()
}
