package controlplane

import (
	"time"

	"github.com/google/uuid"

	"github.com/goairix/llm-proxy/internal/application/controlplane/dto"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
	tenantmodel "github.com/goairix/llm-proxy/internal/domain/tenancy/model"
)

type organizationView struct {
	ID        uuid.UUID          `json:"id"`
	Name      string             `json:"name"`
	Status    sharedmodel.Status `json:"status"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
}

func viewOrganization(value tenantmodel.Organization) organizationView {
	return organizationView{ID: value.ID, Name: value.Name, Status: value.Status, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

type projectView struct {
	ID             uuid.UUID          `json:"id"`
	OrganizationID uuid.UUID          `json:"organization_id"`
	Name           string             `json:"name"`
	Status         sharedmodel.Status `json:"status"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
}

func viewProject(value tenantmodel.Project) projectView {
	return projectView{ID: value.ID, OrganizationID: value.OrganizationID, Name: value.Name, Status: value.Status, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

type virtualKeyView struct {
	ID        uuid.UUID          `json:"id"`
	ProjectID uuid.UUID          `json:"project_id"`
	Name      string             `json:"name"`
	Prefix    string             `json:"prefix"`
	LastFour  string             `json:"last_four"`
	Status    sharedmodel.Status `json:"status"`
	ExpiresAt *time.Time         `json:"expires_at,omitempty"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
}

func viewVirtualKey(value dto.VirtualKeyView) virtualKeyView {
	return virtualKeyView{ID: value.ID, ProjectID: value.ProjectID, Name: value.Name, Prefix: value.Prefix, LastFour: value.LastFour, Status: value.Status, ExpiresAt: value.ExpiresAt, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

type createdVirtualKeyView struct {
	ID        uuid.UUID  `json:"id"`
	Secret    string     `json:"secret"`
	Prefix    string     `json:"prefix"`
	LastFour  string     `json:"last_four"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type scopeView struct {
	Kind           catalogmodel.ScopeKind `json:"kind"`
	OrganizationID *uuid.UUID             `json:"organization_id,omitempty"`
	ProjectID      *uuid.UUID             `json:"project_id,omitempty"`
}

func viewScope(value catalogmodel.Scope) scopeView {
	result := scopeView{Kind: value.Kind}
	if value.OrganizationID != uuid.Nil {
		id := value.OrganizationID
		result.OrganizationID = &id
	}
	if value.ProjectID != uuid.Nil {
		id := value.ProjectID
		result.ProjectID = &id
	}
	return result
}

type providerView struct {
	ID            uuid.UUID          `json:"id"`
	Name          string             `json:"name"`
	ConnectorType string             `json:"connector_type"`
	BaseURL       string             `json:"base_url"`
	Status        sharedmodel.Status `json:"status"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
}

func viewProvider(value catalogmodel.Provider) providerView {
	return providerView{ID: value.ID, Name: value.Name, ConnectorType: value.ConnectorType, BaseURL: value.BaseURL, Status: value.Status, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

type providerCredentialView struct {
	ID         uuid.UUID          `json:"id"`
	ProviderID uuid.UUID          `json:"provider_id"`
	Scope      scopeView          `json:"scope"`
	Status     sharedmodel.Status `json:"status"`
	CreatedAt  time.Time          `json:"created_at"`
	UpdatedAt  time.Time          `json:"updated_at"`
}

func viewProviderCredential(value dto.ProviderCredentialResult) providerCredentialView {
	return providerCredentialView{ID: value.ID, ProviderID: value.ProviderID, Scope: viewScope(value.Scope), Status: value.Status, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

type deploymentView struct {
	ID               uuid.UUID                     `json:"id"`
	ProviderID       uuid.UUID                     `json:"provider_id"`
	Name             string                        `json:"name"`
	UpstreamModel    string                        `json:"upstream_model"`
	UpstreamProtocol catalogmodel.UpstreamProtocol `json:"upstream_protocol"`
	Scope            scopeView                     `json:"scope"`
	Capabilities     catalogmodel.CapabilitySet    `json:"capabilities"`
	Status           sharedmodel.Status            `json:"status"`
	CreatedAt        time.Time                     `json:"created_at"`
	UpdatedAt        time.Time                     `json:"updated_at"`
}

func viewDeployment(value catalogmodel.Deployment) deploymentView {
	return deploymentView{ID: value.ID, ProviderID: value.ProviderID, Name: value.Name, UpstreamModel: value.UpstreamModel, UpstreamProtocol: value.UpstreamProtocol, Scope: viewScope(value.Scope), Capabilities: value.Capabilities, Status: value.Status, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

type modelAliasView struct {
	ID        uuid.UUID          `json:"id"`
	ProjectID uuid.UUID          `json:"project_id"`
	Name      string             `json:"name"`
	Status    sharedmodel.Status `json:"status"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
}

func viewModelAlias(value catalogmodel.ModelAlias) modelAliasView {
	return modelAliasView{ID: value.ID, ProjectID: value.ProjectID, Name: value.Name, Status: value.Status, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

type routeTargetView struct {
	ID           uuid.UUID          `json:"id"`
	ModelAliasID uuid.UUID          `json:"model_alias_id"`
	DeploymentID uuid.UUID          `json:"deployment_id"`
	Priority     int                `json:"priority"`
	Weight       int                `json:"weight"`
	Status       sharedmodel.Status `json:"status"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
}

func viewRouteTarget(value catalogmodel.RouteTarget) routeTargetView {
	return routeTargetView{ID: value.ID, ModelAliasID: value.ModelAliasID, DeploymentID: value.DeploymentID, Priority: value.Priority, Weight: value.Weight, Status: value.Status, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}
