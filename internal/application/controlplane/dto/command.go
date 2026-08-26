package dto

import (
	"time"

	"github.com/google/uuid"

	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
)

type Pagination struct {
	Limit  int
	Offset int
}

type CreateOrganization struct{ Name string }

type UpdateOrganization struct {
	ID     uuid.UUID
	Name   *string
	Status *sharedmodel.Status
}

type CreateProject struct {
	OrganizationID uuid.UUID
	Name           string
}

type UpdateProject struct {
	ID     uuid.UUID
	Name   *string
	Status *sharedmodel.Status
}

type CreateVirtualKey struct {
	ProjectID uuid.UUID
	Name      string
	ExpiresAt *time.Time
}

type UpdateVirtualKey struct {
	ID        uuid.UUID
	Name      *string
	Status    *sharedmodel.Status
	ExpiresAt *time.Time
}

type CreateProvider struct {
	Name          string
	ConnectorType string
	BaseURL       string
}

type UpdateProvider struct {
	ID      uuid.UUID
	Name    *string
	BaseURL *string
	Status  *sharedmodel.Status
}

type CreateProviderCredential struct {
	ProviderID uuid.UUID
	Scope      catalogmodel.Scope
	Credential []byte
}

type UpdateProviderCredential struct {
	ID         uuid.UUID
	Status     *sharedmodel.Status
	Credential []byte
}

type CreateDeployment struct {
	ProviderID       uuid.UUID
	Name             string
	UpstreamModel    string
	UpstreamProtocol catalogmodel.UpstreamProtocol
	Scope            catalogmodel.Scope
	Capabilities     catalogmodel.CapabilitySet
}

type UpdateDeployment struct {
	ID               uuid.UUID
	Name             *string
	UpstreamModel    *string
	UpstreamProtocol *catalogmodel.UpstreamProtocol
	Capabilities     *catalogmodel.CapabilitySet
	Status           *sharedmodel.Status
}

type CreateModelAlias struct {
	ProjectID uuid.UUID
	Name      string
}

type UpdateModelAlias struct {
	ID     uuid.UUID
	Name   *string
	Status *sharedmodel.Status
}

type CreateRouteTarget struct {
	ModelAliasID uuid.UUID
	DeploymentID uuid.UUID
	Priority     int
	Weight       int
}

type UpdateRouteTarget struct {
	ID       uuid.UUID
	Priority *int
	Weight   *int
	Status   *sharedmodel.Status
}
