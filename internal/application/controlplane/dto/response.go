package dto

import (
	"time"

	"github.com/google/uuid"

	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
	tenantmodel "github.com/goairix/llm-proxy/internal/domain/tenancy/model"
)

type OrganizationResult struct {
	Organization tenantmodel.Organization
	Revision     int64
}

type ProjectResult struct {
	Project  tenantmodel.Project
	Revision int64
}

type CreateVirtualKeyResult struct {
	ID        uuid.UUID
	Secret    string
	Prefix    string
	LastFour  string
	ExpiresAt *time.Time
	Revision  int64
}

// VirtualKeyView excludes the authentication hash and plaintext secret.
type VirtualKeyView struct {
	ID        uuid.UUID
	ProjectID uuid.UUID
	Name      string
	Prefix    string
	LastFour  string
	Status    sharedmodel.Status
	ExpiresAt *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

type VirtualKeyResult struct {
	VirtualKey VirtualKeyView
	Revision   int64
}

type ProviderResult struct {
	Provider catalogmodel.Provider
	Revision int64
}

// ProviderCredentialResult intentionally excludes encrypted envelope fields.
type ProviderCredentialResult struct {
	ID         uuid.UUID
	ProviderID uuid.UUID
	Scope      catalogmodel.Scope
	KeyVersion string
	Status     sharedmodel.Status
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Revision   int64
}

type DeploymentResult struct {
	Deployment catalogmodel.Deployment
	Revision   int64
}

type ModelAliasResult struct {
	ModelAlias catalogmodel.ModelAlias
	Revision   int64
}

type RouteTargetResult struct {
	RouteTarget catalogmodel.RouteTarget
	Revision    int64
}
