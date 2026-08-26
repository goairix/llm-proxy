package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/goairix/llm-proxy/internal/application/controlplane/dto"
	controlerrors "github.com/goairix/llm-proxy/internal/application/controlplane/errors"
	controlport "github.com/goairix/llm-proxy/internal/application/controlplane/port"
	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	catalogrepository "github.com/goairix/llm-proxy/internal/domain/catalog/repository"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
	sharedport "github.com/goairix/llm-proxy/internal/domain/shared/port"
	tenantmodel "github.com/goairix/llm-proxy/internal/domain/tenancy/model"
	tenantrepository "github.com/goairix/llm-proxy/internal/domain/tenancy/repository"
)

type CatalogService struct {
	providers        catalogrepository.ProviderRepository
	credentials      catalogrepository.ProviderCredentialRepository
	deployments      catalogrepository.DeploymentRepository
	aliases          catalogrepository.ModelAliasRepository
	targets          catalogrepository.RouteTargetRepository
	organizations    tenantrepository.OrganizationRepository
	projects         tenantrepository.ProjectRepository
	revisions        catalogrepository.ConfigRevisionRepository
	transactions     sharedport.TransactionManager
	credentialCipher controlport.CredentialCipher
	notifier         gatewayport.RefreshNotifier
}

func NewCatalogService(
	providers catalogrepository.ProviderRepository,
	credentials catalogrepository.ProviderCredentialRepository,
	deployments catalogrepository.DeploymentRepository,
	aliases catalogrepository.ModelAliasRepository,
	targets catalogrepository.RouteTargetRepository,
	organizations tenantrepository.OrganizationRepository,
	projects tenantrepository.ProjectRepository,
	revisions catalogrepository.ConfigRevisionRepository,
	transactions sharedport.TransactionManager,
	credentialCipher controlport.CredentialCipher,
	notifier gatewayport.RefreshNotifier,
) *CatalogService {
	return &CatalogService{
		providers: providers, credentials: credentials, deployments: deployments, aliases: aliases, targets: targets,
		organizations: organizations, projects: projects, revisions: revisions, transactions: transactions,
		credentialCipher: credentialCipher, notifier: notifier,
	}
}

func (s *CatalogService) CreateProvider(ctx context.Context, command dto.CreateProvider) (dto.ProviderResult, error) {
	provider, err := catalogmodel.NewProvider(command.Name, command.ConnectorType, command.BaseURL)
	if err != nil {
		return dto.ProviderResult{}, mapApplicationError(err)
	}
	var revision int64
	err = s.mutate(ctx, func(txCtx context.Context) error {
		if err := s.providers.Save(txCtx, provider); err != nil {
			return err
		}
		var err error
		revision, err = s.revisions.Next(txCtx)
		return err
	})
	if err != nil {
		return dto.ProviderResult{}, mapApplicationError(err)
	}
	return dto.ProviderResult{Provider: *provider, Revision: revision}, nil
}

func (s *CatalogService) CreateProviderCredential(ctx context.Context, command dto.CreateProviderCredential) (dto.ProviderCredentialResult, error) {
	var credential *catalogmodel.ProviderCredential
	var revision int64
	err := s.mutate(ctx, func(txCtx context.Context) error {
		provider, err := s.providers.FindByID(txCtx, command.ProviderID)
		if err != nil {
			return err
		}
		if provider == nil {
			return notFound("供应商")
		}
		if provider.Status != sharedmodel.StatusActive {
			return disabled("供应商")
		}
		if _, err := s.validateScope(txCtx, command.Scope); err != nil {
			return err
		}
		if err := validateCredentialPayload(provider.ConnectorType, command.Credential); err != nil {
			return err
		}
		identity, err := sharedmodel.NewEntity()
		if err != nil {
			return err
		}
		sealed, err := s.credentialCipher.Seal(txCtx, identity.ID, command.ProviderID, command.Scope, command.Credential)
		if err != nil {
			return err
		}
		credential, err = catalogmodel.NewProviderCredentialWithEntity(identity, command.ProviderID, command.Scope, sealed)
		if err != nil {
			return err
		}
		if err := s.credentials.Save(txCtx, credential); err != nil {
			return err
		}
		revision, err = s.revisions.Next(txCtx)
		return err
	})
	if err != nil {
		return dto.ProviderCredentialResult{}, mapApplicationError(err)
	}
	return credentialResult(credential, revision), nil
}

func credentialResult(credential *catalogmodel.ProviderCredential, revision int64) dto.ProviderCredentialResult {
	return dto.ProviderCredentialResult{
		ID: credential.ID, ProviderID: credential.ProviderID, Scope: credential.Scope,
		Status:    credential.Status,
		CreatedAt: credential.CreatedAt, UpdatedAt: credential.UpdatedAt, Revision: revision,
	}
}

func (s *CatalogService) CreateDeployment(ctx context.Context, command dto.CreateDeployment) (dto.DeploymentResult, error) {
	var deployment *catalogmodel.Deployment
	var revision int64
	err := s.mutate(ctx, func(txCtx context.Context) error {
		provider, err := s.providers.FindByID(txCtx, command.ProviderID)
		if err != nil {
			return err
		}
		if provider == nil {
			return notFound("供应商")
		}
		if provider.Status != sharedmodel.StatusActive {
			return disabled("供应商")
		}
		if !provider.Supports(command.UpstreamProtocol) {
			return controlerrors.New(controlerrors.InvalidRequest, "供应商不支持指定的上游协议", "upstream_protocol", nil)
		}
		_, err = s.validateScope(txCtx, command.Scope)
		if err != nil {
			return err
		}
		deployment, err = catalogmodel.NewDeployment(command.ProviderID, command.Name, command.UpstreamModel, command.UpstreamProtocol, command.Scope, command.Capabilities)
		if err != nil {
			return err
		}
		if err := s.deployments.Save(txCtx, deployment); err != nil {
			return err
		}
		revision, err = s.revisions.Next(txCtx)
		return err
	})
	if err != nil {
		return dto.DeploymentResult{}, mapApplicationError(err)
	}
	return dto.DeploymentResult{Deployment: *deployment, Revision: revision}, nil
}

func (s *CatalogService) CreateModelAlias(ctx context.Context, command dto.CreateModelAlias) (dto.ModelAliasResult, error) {
	var alias *catalogmodel.ModelAlias
	var revision int64
	err := s.mutate(ctx, func(txCtx context.Context) error {
		project, err := s.activeProject(txCtx, command.ProjectID)
		if err != nil {
			return err
		}
		if _, err := s.activeOrganization(txCtx, project.OrganizationID); err != nil {
			return err
		}
		existing, err := s.aliases.FindByProjectAndName(txCtx, command.ProjectID, command.Name)
		if err != nil {
			return err
		}
		if existing != nil {
			return controlerrors.New(controlerrors.Conflict, "项目内模型别名已存在", "name", nil)
		}
		alias, err = catalogmodel.NewModelAlias(command.ProjectID, command.Name)
		if err != nil {
			return err
		}
		if err := s.aliases.Save(txCtx, alias); err != nil {
			return err
		}
		revision, err = s.revisions.Next(txCtx)
		return err
	})
	if err != nil {
		return dto.ModelAliasResult{}, mapApplicationError(err)
	}
	return dto.ModelAliasResult{ModelAlias: *alias, Revision: revision}, nil
}

func (s *CatalogService) CreateRouteTarget(ctx context.Context, command dto.CreateRouteTarget) (dto.RouteTargetResult, error) {
	var target *catalogmodel.RouteTarget
	var revision int64
	err := s.mutate(ctx, func(txCtx context.Context) error {
		alias, err := s.aliases.FindByID(txCtx, command.ModelAliasID)
		if err != nil {
			return err
		}
		if alias == nil {
			return notFound("模型别名")
		}
		project, err := s.activeProject(txCtx, alias.ProjectID)
		if err != nil {
			return err
		}
		if _, err := s.activeOrganization(txCtx, project.OrganizationID); err != nil {
			return err
		}
		deployment, err := s.deployments.FindByID(txCtx, command.DeploymentID)
		if err != nil {
			return err
		}
		if deployment == nil {
			return notFound("Deployment")
		}
		if deployment.Status != sharedmodel.StatusActive {
			return disabled("Deployment")
		}
		if !deploymentVisibleToProject(deployment.Scope, project.ID, project.OrganizationID) {
			return controlerrors.New(controlerrors.PermissionDenied, "Deployment 对当前项目不可见", "deployment_id", nil)
		}
		existing, err := s.targets.ListByModelAlias(txCtx, alias.ID)
		if err != nil {
			return err
		}
		for _, current := range existing {
			if current.Status == sharedmodel.StatusActive {
				return controlerrors.New(controlerrors.Conflict, "当前版本每个模型别名只能有一个启用的 RouteTarget", "model_alias_id", nil)
			}
		}
		target, err = catalogmodel.NewRouteTarget(command.ModelAliasID, command.DeploymentID, command.Priority, command.Weight)
		if err != nil {
			return err
		}
		if err := s.targets.Save(txCtx, target); err != nil {
			return err
		}
		revision, err = s.revisions.Next(txCtx)
		return err
	})
	if err != nil {
		return dto.RouteTargetResult{}, mapApplicationError(err)
	}
	return dto.RouteTargetResult{RouteTarget: *target, Revision: revision}, nil
}

func (s *CatalogService) mutate(ctx context.Context, fn func(context.Context) error) error {
	if err := s.transactions.Transaction(ctx, func(txCtx context.Context) error {
		if err := s.revisions.Lock(txCtx); err != nil {
			return err
		}
		return fn(txCtx)
	}); err != nil {
		return err
	}
	if s.notifier != nil {
		s.notifier.NotifyRefresh()
	}
	return nil
}

type resolvedScope struct {
	organizationID uuid.UUID
	projectID      uuid.UUID
}

func (s *CatalogService) validateScope(ctx context.Context, scope catalogmodel.Scope) (resolvedScope, error) {
	if err := scope.Validate(); err != nil {
		return resolvedScope{}, err
	}
	switch scope.Kind {
	case catalogmodel.ScopePlatform:
		return resolvedScope{}, nil
	case catalogmodel.ScopeOrganization:
		organization, err := s.activeOrganization(ctx, scope.OrganizationID)
		if err != nil {
			return resolvedScope{}, err
		}
		return resolvedScope{organizationID: organization.ID}, nil
	case catalogmodel.ScopeProject:
		project, err := s.activeProject(ctx, scope.ProjectID)
		if err != nil {
			return resolvedScope{}, err
		}
		if _, err := s.activeOrganization(ctx, project.OrganizationID); err != nil {
			return resolvedScope{}, err
		}
		return resolvedScope{organizationID: project.OrganizationID, projectID: project.ID}, nil
	default:
		return resolvedScope{}, fmt.Errorf("unsupported scope %q", scope.Kind)
	}
}

func (s *CatalogService) activeOrganization(ctx context.Context, id uuid.UUID) (*tenantmodel.Organization, error) {
	organization, err := s.organizations.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if organization == nil {
		return nil, notFound("组织")
	}
	if organization.Status != sharedmodel.StatusActive {
		return nil, disabled("组织")
	}
	return organization, nil
}

func (s *CatalogService) activeProject(ctx context.Context, id uuid.UUID) (*tenantmodel.Project, error) {
	project, err := s.projects.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, notFound("项目")
	}
	if project.Status != sharedmodel.StatusActive {
		return nil, disabled("项目")
	}
	return project, nil
}

func deploymentVisibleToProject(scope catalogmodel.Scope, projectID, organizationID uuid.UUID) bool {
	switch scope.Kind {
	case catalogmodel.ScopePlatform:
		return true
	case catalogmodel.ScopeOrganization:
		return scope.OrganizationID == organizationID
	case catalogmodel.ScopeProject:
		return scope.ProjectID == projectID
	default:
		return false
	}
}

func (s *CatalogService) validateDeploymentAssociations(ctx context.Context, deployment *catalogmodel.Deployment) error {
	provider, err := s.providers.FindByID(ctx, deployment.ProviderID)
	if err != nil {
		return err
	}
	if provider == nil {
		return notFound("供应商")
	}
	if provider.Status != sharedmodel.StatusActive {
		return disabled("供应商")
	}
	if !provider.Supports(deployment.UpstreamProtocol) {
		return controlerrors.New(controlerrors.InvalidRequest, "供应商不支持指定的上游协议", "upstream_protocol", nil)
	}
	_, err = s.validateScope(ctx, deployment.Scope)
	return err
}

func (s *CatalogService) GetProvider(ctx context.Context, id uuid.UUID) (*catalogmodel.Provider, error) {
	provider, err := s.providers.FindByID(ctx, id)
	if err != nil {
		return nil, mapApplicationError(err)
	}
	if provider == nil {
		return nil, notFound("供应商")
	}
	return provider, nil
}

func (s *CatalogService) ListProviders(ctx context.Context, pagination dto.Pagination) ([]catalogmodel.Provider, error) {
	providers, err := s.providers.List(ctx, pagination.Limit, pagination.Offset)
	return providers, mapApplicationError(err)
}

func (s *CatalogService) UpdateProvider(ctx context.Context, command dto.UpdateProvider) (dto.ProviderResult, error) {
	var provider *catalogmodel.Provider
	var revision int64
	err := s.mutate(ctx, func(txCtx context.Context) error {
		var err error
		provider, err = s.providers.FindByID(txCtx, command.ID)
		if err != nil {
			return err
		}
		if provider == nil {
			return notFound("供应商")
		}
		if disabling(provider.Status, command.Status) {
			referenced, err := s.deployments.HasActiveByProvider(txCtx, provider.ID)
			if err != nil {
				return err
			}
			if referenced {
				return controlerrors.New(controlerrors.Conflict, "供应商仍被启用的 Deployment 引用", "status", nil)
			}
		}
		if command.Name != nil {
			provider.Name = *command.Name
		}
		if command.BaseURL != nil {
			if err := provider.SetBaseURL(*command.BaseURL); err != nil {
				return err
			}
		}
		if command.Status != nil {
			provider.Status = *command.Status
		}
		provider.UpdatedAt = time.Now().UTC()
		if err := provider.Validate(); err != nil {
			return err
		}
		if err := s.providers.Save(txCtx, provider); err != nil {
			return err
		}
		revision, err = s.revisions.Next(txCtx)
		return err
	})
	if err != nil {
		return dto.ProviderResult{}, mapApplicationError(err)
	}
	return dto.ProviderResult{Provider: *provider, Revision: revision}, nil
}

func (s *CatalogService) GetProviderCredential(ctx context.Context, id uuid.UUID) (dto.ProviderCredentialResult, error) {
	credential, err := s.credentials.FindByID(ctx, id)
	if err != nil {
		return dto.ProviderCredentialResult{}, mapApplicationError(err)
	}
	if credential == nil {
		return dto.ProviderCredentialResult{}, notFound("供应商凭据")
	}
	return credentialResult(credential, 0), nil
}

func (s *CatalogService) ListProviderCredentials(ctx context.Context, pagination dto.Pagination) ([]dto.ProviderCredentialResult, error) {
	credentials, err := s.credentials.List(ctx, pagination.Limit, pagination.Offset)
	if err != nil {
		return nil, mapApplicationError(err)
	}
	result := make([]dto.ProviderCredentialResult, 0, len(credentials))
	for i := range credentials {
		result = append(result, credentialResult(&credentials[i], 0))
	}
	return result, nil
}

func (s *CatalogService) UpdateProviderCredential(ctx context.Context, command dto.UpdateProviderCredential) (dto.ProviderCredentialResult, error) {
	var credential *catalogmodel.ProviderCredential
	var revision int64
	err := s.mutate(ctx, func(txCtx context.Context) error {
		var err error
		credential, err = s.credentials.FindByID(txCtx, command.ID)
		if err != nil {
			return err
		}
		if credential == nil {
			return notFound("供应商凭据")
		}
		provider, err := s.providers.FindByID(txCtx, credential.ProviderID)
		if err != nil {
			return err
		}
		if provider == nil {
			return notFound("供应商")
		}
		if len(command.Credential) > 0 {
			if err := validateCredentialPayload(provider.ConnectorType, command.Credential); err != nil {
				return err
			}
			sealed, err := s.credentialCipher.Seal(txCtx, credential.ID, credential.ProviderID, credential.Scope, command.Credential)
			if err != nil {
				return err
			}
			credential.Sealed = sealed
		}
		if command.Status != nil {
			credential.Status = *command.Status
		}
		credential.UpdatedAt = time.Now().UTC()
		if err := credential.Validate(); err != nil {
			return err
		}
		if err := s.credentials.Save(txCtx, credential); err != nil {
			return err
		}
		revision, err = s.revisions.Next(txCtx)
		return err
	})
	if err != nil {
		return dto.ProviderCredentialResult{}, mapApplicationError(err)
	}
	return credentialResult(credential, revision), nil
}

func (s *CatalogService) GetDeployment(ctx context.Context, id uuid.UUID) (*catalogmodel.Deployment, error) {
	deployment, err := s.deployments.FindByID(ctx, id)
	if err != nil {
		return nil, mapApplicationError(err)
	}
	if deployment == nil {
		return nil, notFound("Deployment")
	}
	return deployment, nil
}

func (s *CatalogService) ListDeployments(ctx context.Context, pagination dto.Pagination) ([]catalogmodel.Deployment, error) {
	deployments, err := s.deployments.List(ctx, pagination.Limit, pagination.Offset)
	return deployments, mapApplicationError(err)
}

func (s *CatalogService) UpdateDeployment(ctx context.Context, command dto.UpdateDeployment) (dto.DeploymentResult, error) {
	var deployment *catalogmodel.Deployment
	var revision int64
	err := s.mutate(ctx, func(txCtx context.Context) error {
		var err error
		deployment, err = s.deployments.FindByID(txCtx, command.ID)
		if err != nil {
			return err
		}
		if deployment == nil {
			return notFound("Deployment")
		}
		if disabling(deployment.Status, command.Status) {
			referenced, err := s.targets.HasActiveByDeployment(txCtx, deployment.ID)
			if err != nil {
				return err
			}
			if referenced {
				return controlerrors.New(controlerrors.Conflict, "Deployment 仍被启用的 RouteTarget 引用", "status", nil)
			}
		}
		if command.Name != nil {
			deployment.Name = *command.Name
		}
		if command.UpstreamModel != nil {
			deployment.UpstreamModel = *command.UpstreamModel
		}
		if command.UpstreamProtocol != nil {
			deployment.UpstreamProtocol = *command.UpstreamProtocol
		}
		if command.Capabilities != nil {
			deployment.Capabilities = *command.Capabilities
		}
		if command.Status != nil {
			deployment.Status = *command.Status
		}
		deployment.UpdatedAt = time.Now().UTC()
		if err := deployment.Validate(); err != nil {
			return err
		}
		if err := s.validateDeploymentAssociations(txCtx, deployment); err != nil {
			return err
		}
		if err := s.deployments.Save(txCtx, deployment); err != nil {
			return err
		}
		revision, err = s.revisions.Next(txCtx)
		return err
	})
	if err != nil {
		return dto.DeploymentResult{}, mapApplicationError(err)
	}
	return dto.DeploymentResult{Deployment: *deployment, Revision: revision}, nil
}

func (s *CatalogService) GetModelAlias(ctx context.Context, id uuid.UUID) (*catalogmodel.ModelAlias, error) {
	alias, err := s.aliases.FindByID(ctx, id)
	if err != nil {
		return nil, mapApplicationError(err)
	}
	if alias == nil {
		return nil, notFound("模型别名")
	}
	return alias, nil
}

func (s *CatalogService) ListModelAliases(ctx context.Context, pagination dto.Pagination) ([]catalogmodel.ModelAlias, error) {
	aliases, err := s.aliases.List(ctx, pagination.Limit, pagination.Offset)
	return aliases, mapApplicationError(err)
}

func (s *CatalogService) UpdateModelAlias(ctx context.Context, command dto.UpdateModelAlias) (dto.ModelAliasResult, error) {
	var alias *catalogmodel.ModelAlias
	var revision int64
	err := s.mutate(ctx, func(txCtx context.Context) error {
		var err error
		alias, err = s.aliases.FindByID(txCtx, command.ID)
		if err != nil {
			return err
		}
		if alias == nil {
			return notFound("模型别名")
		}
		project, err := s.activeProject(txCtx, alias.ProjectID)
		if err != nil {
			return err
		}
		if _, err := s.activeOrganization(txCtx, project.OrganizationID); err != nil {
			return err
		}
		if command.Name != nil {
			existing, err := s.aliases.FindByProjectAndName(txCtx, alias.ProjectID, *command.Name)
			if err != nil {
				return err
			}
			if existing != nil && existing.ID != alias.ID {
				return controlerrors.New(controlerrors.Conflict, "项目内模型别名已存在", "name", nil)
			}
			alias.Name = *command.Name
		}
		if command.Status != nil {
			if alias.Status != sharedmodel.StatusActive && *command.Status == sharedmodel.StatusActive {
				if err := s.validateAliasActivation(txCtx, alias, project); err != nil {
					return err
				}
			}
			alias.Status = *command.Status
		}
		alias.UpdatedAt = time.Now().UTC()
		if err := alias.Validate(); err != nil {
			return err
		}
		if err := s.aliases.Save(txCtx, alias); err != nil {
			return err
		}
		revision, err = s.revisions.Next(txCtx)
		return err
	})
	if err != nil {
		return dto.ModelAliasResult{}, mapApplicationError(err)
	}
	return dto.ModelAliasResult{ModelAlias: *alias, Revision: revision}, nil
}

func (s *CatalogService) GetRouteTarget(ctx context.Context, id uuid.UUID) (*catalogmodel.RouteTarget, error) {
	target, err := s.targets.FindByID(ctx, id)
	if err != nil {
		return nil, mapApplicationError(err)
	}
	if target == nil {
		return nil, notFound("RouteTarget")
	}
	return target, nil
}

func (s *CatalogService) ListRouteTargets(ctx context.Context, modelAliasID uuid.UUID) ([]catalogmodel.RouteTarget, error) {
	if _, err := s.GetModelAlias(ctx, modelAliasID); err != nil {
		return nil, err
	}
	targets, err := s.targets.ListByModelAlias(ctx, modelAliasID)
	return targets, mapApplicationError(err)
}

func (s *CatalogService) UpdateRouteTarget(ctx context.Context, command dto.UpdateRouteTarget) (dto.RouteTargetResult, error) {
	var target *catalogmodel.RouteTarget
	var revision int64
	err := s.mutate(ctx, func(txCtx context.Context) error {
		var err error
		target, err = s.targets.FindByID(txCtx, command.ID)
		if err != nil {
			return err
		}
		if target == nil {
			return notFound("RouteTarget")
		}
		alias, err := s.aliases.FindByID(txCtx, target.ModelAliasID)
		if err != nil {
			return err
		}
		if alias == nil {
			return notFound("模型别名")
		}
		project, err := s.projects.FindByID(txCtx, alias.ProjectID)
		if err != nil {
			return err
		}
		if project == nil {
			return notFound("项目")
		}
		organization, err := s.organizations.FindByID(txCtx, project.OrganizationID)
		if err != nil {
			return err
		}
		if organization == nil {
			return notFound("组织")
		}
		deployment, err := s.deployments.FindByID(txCtx, target.DeploymentID)
		if err != nil {
			return err
		}
		if deployment == nil {
			return notFound("Deployment")
		}
		if command.Priority != nil {
			target.Priority = *command.Priority
		}
		if command.Weight != nil {
			target.Weight = *command.Weight
		}
		if command.Status != nil {
			if disabling(target.Status, command.Status) && alias.Status == sharedmodel.StatusActive {
				return controlerrors.New(controlerrors.Conflict, "请先停用模型别名，再停用其启用的 RouteTarget", "status", nil)
			}
			if *command.Status == sharedmodel.StatusActive && target.Status != sharedmodel.StatusActive {
				existing, err := s.targets.ListByModelAlias(txCtx, target.ModelAliasID)
				if err != nil {
					return err
				}
				for _, current := range existing {
					if current.ID != target.ID && current.Status == sharedmodel.StatusActive {
						return controlerrors.New(controlerrors.Conflict, "当前版本每个模型别名只能有一个启用的 RouteTarget", "status", nil)
					}
				}
			}
			target.Status = *command.Status
		}
		if target.Status == sharedmodel.StatusActive {
			if project.Status != sharedmodel.StatusActive {
				return disabled("项目")
			}
			if organization.Status != sharedmodel.StatusActive {
				return disabled("组织")
			}
			if deployment.Status != sharedmodel.StatusActive {
				return disabled("Deployment")
			}
			if !deploymentVisibleToProject(deployment.Scope, project.ID, project.OrganizationID) {
				return controlerrors.New(controlerrors.PermissionDenied, "Deployment 对当前项目不可见", "deployment_id", nil)
			}
		}
		target.UpdatedAt = time.Now().UTC()
		if err := target.Validate(); err != nil {
			return err
		}
		if err := s.targets.Save(txCtx, target); err != nil {
			return err
		}
		revision, err = s.revisions.Next(txCtx)
		return err
	})
	if err != nil {
		return dto.RouteTargetResult{}, mapApplicationError(err)
	}
	return dto.RouteTargetResult{RouteTarget: *target, Revision: revision}, nil
}

func (s *CatalogService) validateAliasActivation(ctx context.Context, alias *catalogmodel.ModelAlias, project *tenantmodel.Project) error {
	targets, err := s.targets.ListByModelAlias(ctx, alias.ID)
	if err != nil {
		return err
	}
	var selected *catalogmodel.RouteTarget
	for index := range targets {
		if targets[index].Status != sharedmodel.StatusActive {
			continue
		}
		if selected != nil {
			return controlerrors.New(controlerrors.Conflict, "模型别名必须且只能关联一个启用的 RouteTarget", "status", nil)
		}
		selected = &targets[index]
	}
	if selected == nil {
		return controlerrors.New(controlerrors.Conflict, "模型别名必须且只能关联一个启用的 RouteTarget", "status", nil)
	}
	deployment, err := s.deployments.FindByID(ctx, selected.DeploymentID)
	if err != nil {
		return err
	}
	if deployment == nil {
		return notFound("Deployment")
	}
	if deployment.Status != sharedmodel.StatusActive {
		return disabled("Deployment")
	}
	if !deploymentVisibleToProject(deployment.Scope, project.ID, project.OrganizationID) {
		return controlerrors.New(controlerrors.PermissionDenied, "Deployment 对当前项目不可见", "status", nil)
	}
	return s.validateDeploymentAssociations(ctx, deployment)
}

func validateCredentialPayload(connectorType string, payload []byte) error {
	if connectorType == catalogmodel.ConnectorFake {
		return controlerrors.New(controlerrors.InvalidRequest, "Fake Provider 不接受供应商凭据", "credential", nil)
	}
	var value struct {
		APIKey string `json:"api_key"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil || strings.TrimSpace(value.APIKey) == "" {
		return controlerrors.New(controlerrors.InvalidRequest, "credential 必须只包含非空 api_key", "credential", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return controlerrors.New(controlerrors.InvalidRequest, "credential 只能包含一个 JSON 值", "credential", nil)
	}
	return nil
}

func disabling(current sharedmodel.Status, next *sharedmodel.Status) bool {
	return current == sharedmodel.StatusActive && next != nil && *next == sharedmodel.StatusDisabled
}
