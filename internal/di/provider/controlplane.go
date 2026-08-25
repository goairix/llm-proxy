package provider

import (
	"fmt"
	"net/http"

	controlport "github.com/goairix/llm-proxy/internal/application/controlplane/port"
	controlservice "github.com/goairix/llm-proxy/internal/application/controlplane/service"
	"github.com/goairix/llm-proxy/internal/infrastructure/config"
	catalogrepository "github.com/goairix/llm-proxy/internal/infrastructure/persistence/repository/catalog"
	tenancyrepository "github.com/goairix/llm-proxy/internal/infrastructure/persistence/repository/tenancy"
	"github.com/goairix/llm-proxy/internal/infrastructure/security/controltoken"
	credentialsecurity "github.com/goairix/llm-proxy/internal/infrastructure/security/credential"
	"github.com/goairix/llm-proxy/internal/infrastructure/security/virtualkey"
	controlhandler "github.com/goairix/llm-proxy/internal/interfaces/http/handler/controlplane"
)

// ControlPlaneRuntime groups optional control-plane HTTP dependencies.
type ControlPlaneRuntime struct {
	Handler    http.Handler
	Authorizer controlport.ControlPlaneAuthorizer
}

// NewControlPlaneRuntime builds the control plane on the shared gateway persistence runtime.
func NewControlPlaneRuntime(cfg *config.Config, gateway *GatewayRuntime) (*ControlPlaneRuntime, error) {
	runtime := &ControlPlaneRuntime{}
	if cfg == nil || !cfg.Gateway.Enabled {
		return runtime, nil
	}
	if gateway == nil || gateway.Manager == nil {
		return nil, fmt.Errorf("gateway persistence runtime is unavailable")
	}
	manager := gateway.Manager

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
	tenancy := controlservice.NewTenancyService(organizations, projects, virtualKeys, revisions, manager, virtualkey.NewGenerator(), gateway)
	catalog := controlservice.NewCatalogService(
		providers, credentials, deployments, aliases, targets, organizations, projects,
		revisions, manager, cipher, gateway,
	)

	runtime.Handler = controlhandler.New(controlhandler.Dependencies{Tenancy: tenancy, Catalog: catalog})
	runtime.Authorizer = controltoken.NewAuthorizer(cfg.ControlPlane.Token)
	return runtime, nil
}
