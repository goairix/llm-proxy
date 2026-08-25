package snapshot

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
	tenancymodel "github.com/goairix/llm-proxy/internal/domain/tenancy/model"
)

type Compiler struct{}

func NewCompiler() *Compiler { return &Compiler{} }

func (c *Compiler) Compile(source SourceConfig, now time.Time) (*RuntimeSnapshot, error) {
	if source.Revision < 0 {
		return nil, fmt.Errorf("revision %d: 配置版本不能为负数", source.Revision)
	}
	organizations := make(map[uuid.UUID]tenancymodel.Organization, len(source.Organizations))
	activeOrganizations := make(map[uuid.UUID]tenancymodel.Organization, len(source.Organizations))
	for _, organization := range source.Organizations {
		if err := organization.Validate(); err != nil {
			return nil, compileError(source.Revision, "organization", organization.ID, err)
		}
		if _, exists := organizations[organization.ID]; exists {
			return nil, compileMessage(source.Revision, "organization", organization.ID, "ID 重复")
		}
		organizations[organization.ID] = organization
		if organization.Status == sharedmodel.StatusActive {
			activeOrganizations[organization.ID] = organization
		}
	}

	projects := make(map[uuid.UUID]tenancymodel.Project, len(source.Projects))
	activeProjects := make(map[uuid.UUID]tenancymodel.Project, len(source.Projects))
	for _, project := range source.Projects {
		if err := project.Validate(); err != nil {
			return nil, compileError(source.Revision, "project", project.ID, err)
		}
		if _, exists := projects[project.ID]; exists {
			return nil, compileMessage(source.Revision, "project", project.ID, "ID 重复")
		}
		if _, exists := organizations[project.OrganizationID]; !exists {
			return nil, compileMessage(source.Revision, "project", project.ID, "引用的 Organization 不存在")
		}
		projects[project.ID] = project
		if project.Status == sharedmodel.StatusActive {
			if _, organizationActive := activeOrganizations[project.OrganizationID]; organizationActive {
				activeProjects[project.ID] = project
			}
		}
	}

	providers := make(map[uuid.UUID]catalogmodel.Provider, len(source.Providers))
	activeProviders := make(map[uuid.UUID]catalogmodel.Provider, len(source.Providers))
	for _, provider := range source.Providers {
		if err := provider.Validate(); err != nil {
			return nil, compileError(source.Revision, "provider", provider.ID, err)
		}
		if _, exists := providers[provider.ID]; exists {
			return nil, compileMessage(source.Revision, "provider", provider.ID, "ID 重复")
		}
		providers[provider.ID] = provider
		if provider.Status == sharedmodel.StatusActive {
			activeProviders[provider.ID] = provider
		}
	}

	credentials := make(map[uuid.UUID]catalogmodel.ProviderCredential, len(source.Credentials))
	activeCredentials := make(map[uuid.UUID]catalogmodel.ProviderCredential, len(source.Credentials))
	for _, credential := range source.Credentials {
		if err := credential.Validate(); err != nil {
			return nil, compileError(source.Revision, "provider credential", credential.ID, err)
		}
		if _, exists := credentials[credential.ID]; exists {
			return nil, compileMessage(source.Revision, "provider credential", credential.ID, "ID 重复")
		}
		if _, exists := providers[credential.ProviderID]; !exists {
			return nil, compileMessage(source.Revision, "provider credential", credential.ID, "引用的 Provider 不存在")
		}
		if err := validateScopeReferences(credential.Scope, organizations, projects); err != nil {
			return nil, compileError(source.Revision, "provider credential", credential.ID, err)
		}
		credentials[credential.ID] = credential
		_, providerActive := activeProviders[credential.ProviderID]
		if credential.Status == sharedmodel.StatusActive && providerActive && scopeActive(credential.Scope, activeOrganizations, activeProjects) {
			activeCredentials[credential.ID] = credential
		}
	}

	allDeployments := make(map[uuid.UUID]catalogmodel.Deployment, len(source.Deployments))
	activeDeployments := make(map[uuid.UUID]Deployment, len(source.Deployments))
	for _, deployment := range source.Deployments {
		if err := deployment.Validate(); err != nil {
			return nil, compileError(source.Revision, "deployment", deployment.ID, err)
		}
		if _, exists := allDeployments[deployment.ID]; exists {
			return nil, compileMessage(source.Revision, "deployment", deployment.ID, "ID 重复")
		}
		provider, exists := providers[deployment.ProviderID]
		if !exists {
			return nil, compileMessage(source.Revision, "deployment", deployment.ID, "引用的 Provider 不存在")
		}
		if provider.ConnectorType != deployment.ConnectorType {
			return nil, compileMessage(source.Revision, "deployment", deployment.ID, "Connector 类型与 Provider 不一致")
		}
		if err := validateScopeReferences(deployment.Scope, organizations, projects); err != nil {
			return nil, compileError(source.Revision, "deployment", deployment.ID, err)
		}
		var credentialEnvelope *CredentialEnvelope
		credentialActive := deployment.CredentialID == nil
		if deployment.CredentialID == nil {
			if deployment.ConnectorType != "fake" {
				return nil, compileMessage(source.Revision, "deployment", deployment.ID, "非 Fake Deployment 缺少 Credential")
			}
		} else {
			credential, found := credentials[*deployment.CredentialID]
			if !found {
				return nil, compileMessage(source.Revision, "deployment", deployment.ID, "引用的 Credential 不存在")
			}
			if credential.ProviderID != deployment.ProviderID {
				return nil, compileMessage(source.Revision, "deployment", deployment.ID, "Credential 不属于 Provider")
			}
			organizationID := deploymentOrganizationID(deployment.Scope, projects)
			if !credentialScopeCovers(credential.Scope, deployment.Scope, organizationID) {
				return nil, compileMessage(source.Revision, "deployment", deployment.ID, "Credential 作用域不允许该 Deployment")
			}
			if activeCredential, found := activeCredentials[credential.ID]; found {
				credentialActive = true
				credentialEnvelope = &CredentialEnvelope{
					CredentialID: activeCredential.ID, ProviderID: activeCredential.ProviderID,
					Scope: activeCredential.Scope, Sealed: cloneSealedCredential(activeCredential.Sealed),
				}
			}
		}
		allDeployments[deployment.ID] = deployment
		_, providerActive := activeProviders[deployment.ProviderID]
		if deployment.Status == sharedmodel.StatusActive && providerActive && credentialActive && scopeActive(deployment.Scope, activeOrganizations, activeProjects) {
			activeDeployments[deployment.ID] = Deployment{
				ID: deployment.ID, ProviderID: deployment.ProviderID, ConnectorType: deployment.ConnectorType,
				UpstreamModel: deployment.UpstreamModel, Capabilities: deployment.Capabilities, Credential: credentialEnvelope,
			}
		}
	}

	virtualKeys := make(map[[32]byte]AccessContext, len(source.VirtualKeys))
	for _, key := range source.VirtualKeys {
		if err := key.Validate(); err != nil {
			return nil, compileError(source.Revision, "virtual key", key.ID, err)
		}
		project, exists := projects[key.ProjectID]
		if !exists {
			return nil, compileMessage(source.Revision, "virtual key", key.ID, "引用的 Project 不存在")
		}
		if !key.ActiveAt(now) {
			continue
		}
		if _, projectActive := activeProjects[key.ProjectID]; !projectActive {
			continue
		}
		if _, duplicate := virtualKeys[key.Hash]; duplicate {
			return nil, compileMessage(source.Revision, "virtual key", key.ID, "Hash 重复")
		}
		access := AccessContext{OrganizationID: project.OrganizationID, ProjectID: project.ID, VirtualKeyID: key.ID}
		if key.ExpiresAt != nil {
			access.ExpiresAt = *key.ExpiresAt
			access.HasExpiry = true
		}
		virtualKeys[key.Hash] = access
	}

	aliases := make(map[uuid.UUID]catalogmodel.ModelAlias, len(source.ModelAliases))
	for _, alias := range source.ModelAliases {
		if err := alias.Validate(); err != nil {
			return nil, compileError(source.Revision, "model alias", alias.ID, err)
		}
		if _, exists := aliases[alias.ID]; exists {
			return nil, compileMessage(source.Revision, "model alias", alias.ID, "ID 重复")
		}
		if _, exists := projects[alias.ProjectID]; !exists {
			return nil, compileMessage(source.Revision, "model alias", alias.ID, "引用的 Project 不存在")
		}
		aliases[alias.ID] = alias
	}

	targetsByAlias := make(map[uuid.UUID][]catalogmodel.RouteTarget)
	for _, target := range source.RouteTargets {
		if err := target.Validate(); err != nil {
			return nil, compileError(source.Revision, "route target", target.ID, err)
		}
		if _, exists := aliases[target.ModelAliasID]; !exists {
			return nil, compileMessage(source.Revision, "route target", target.ID, "引用的 ModelAlias 不存在")
		}
		if _, exists := allDeployments[target.DeploymentID]; !exists {
			return nil, compileMessage(source.Revision, "route target", target.ID, "引用的 Deployment 不存在")
		}
		targetsByAlias[target.ModelAliasID] = append(targetsByAlias[target.ModelAliasID], target)
	}

	routes := make(map[RouteKey]RoutePlan, len(source.ModelAliases))
	for _, alias := range source.ModelAliases {
		if alias.Status != sharedmodel.StatusActive {
			continue
		}
		project, projectActive := activeProjects[alias.ProjectID]
		if !projectActive {
			continue
		}
		var selected *catalogmodel.RouteTarget
		for _, target := range targetsByAlias[alias.ID] {
			if target.Status != sharedmodel.StatusActive {
				continue
			}
			if selected != nil {
				return nil, compileMessage(source.Revision, "model alias", alias.ID, "没有唯一启用的 RouteTarget")
			}
			targetCopy := target
			selected = &targetCopy
		}
		if selected == nil {
			return nil, compileMessage(source.Revision, "model alias", alias.ID, "没有唯一启用的 RouteTarget")
		}
		deployment, deploymentActive := activeDeployments[selected.DeploymentID]
		if !deploymentActive {
			return nil, compileMessage(source.Revision, "route target", selected.ID, "引用的 Deployment 未启用")
		}
		sourceDeployment := allDeployments[selected.DeploymentID]
		if !deploymentVisibleToProject(sourceDeployment.Scope, project.ID, project.OrganizationID) {
			return nil, compileMessage(source.Revision, "route target", selected.ID, "Deployment 对 Project 不可见")
		}
		key := RouteKey{ProjectID: alias.ProjectID, Model: alias.Name}
		if _, duplicate := routes[key]; duplicate {
			return nil, compileMessage(source.Revision, "model alias", alias.ID, "Project 内模型名称重复")
		}
		routes[key] = RoutePlan{AliasID: alias.ID, Alias: alias.Name, Deployment: deployment}
	}

	return &RuntimeSnapshot{revision: source.Revision, builtAt: now, virtualKeys: virtualKeys, routes: routes, compiled: true}, nil
}

func validateScopeReferences(scope catalogmodel.Scope, organizations map[uuid.UUID]tenancymodel.Organization, projects map[uuid.UUID]tenancymodel.Project) error {
	switch scope.Kind {
	case catalogmodel.ScopePlatform:
		return nil
	case catalogmodel.ScopeOrganization:
		if _, exists := organizations[scope.OrganizationID]; !exists {
			return fmt.Errorf("作用域引用的 Organization 不存在")
		}
	case catalogmodel.ScopeProject:
		if _, exists := projects[scope.ProjectID]; !exists {
			return fmt.Errorf("作用域引用的 Project 不存在")
		}
	}
	return nil
}

func scopeActive(scope catalogmodel.Scope, organizations map[uuid.UUID]tenancymodel.Organization, projects map[uuid.UUID]tenancymodel.Project) bool {
	switch scope.Kind {
	case catalogmodel.ScopePlatform:
		return true
	case catalogmodel.ScopeOrganization:
		_, ok := organizations[scope.OrganizationID]
		return ok
	case catalogmodel.ScopeProject:
		_, ok := projects[scope.ProjectID]
		return ok
	default:
		return false
	}
}

func deploymentOrganizationID(scope catalogmodel.Scope, projects map[uuid.UUID]tenancymodel.Project) uuid.UUID {
	if scope.Kind == catalogmodel.ScopeOrganization {
		return scope.OrganizationID
	}
	if scope.Kind == catalogmodel.ScopeProject {
		return projects[scope.ProjectID].OrganizationID
	}
	return uuid.Nil
}

func credentialScopeCovers(credentialScope, deploymentScope catalogmodel.Scope, deploymentOrganizationID uuid.UUID) bool {
	switch credentialScope.Kind {
	case catalogmodel.ScopePlatform:
		return true
	case catalogmodel.ScopeOrganization:
		return credentialScope.OrganizationID == deploymentOrganizationID
	case catalogmodel.ScopeProject:
		return deploymentScope.Kind == catalogmodel.ScopeProject && credentialScope.ProjectID == deploymentScope.ProjectID
	default:
		return false
	}
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

func compileError(revision int64, resource string, id uuid.UUID, err error) error {
	return fmt.Errorf("revision %d %s %s: %w", revision, resource, id, err)
}

func compileMessage(revision int64, resource string, id uuid.UUID, message string) error {
	return fmt.Errorf("revision %d %s %s: %s", revision, resource, id, message)
}
