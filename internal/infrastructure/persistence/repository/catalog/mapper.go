package catalog

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/entity"
)

func providerToEntity(domain *catalogmodel.Provider) *entity.Provider {
	return &entity.Provider{
		BaseEntity:    baseToEntity(domain.Entity),
		Name:          domain.Name,
		ConnectorType: domain.ConnectorType,
		Status:        string(domain.Status),
	}
}

func providerToDomain(record *entity.Provider) (*catalogmodel.Provider, error) {
	domain := &catalogmodel.Provider{
		Entity:        baseToDomain(record.BaseEntity),
		Name:          record.Name,
		ConnectorType: record.ConnectorType,
		Status:        sharedmodel.Status(record.Status),
	}
	if err := domain.Validate(); err != nil {
		return nil, fmt.Errorf("map provider: %w", err)
	}
	return domain, nil
}

func credentialToEntity(domain *catalogmodel.ProviderCredential) *entity.ProviderCredential {
	organizationID, projectID := scopeIDs(domain.Scope)
	return &entity.ProviderCredential{
		BaseEntity:      baseToEntity(domain.Entity),
		ProviderID:      domain.ProviderID,
		ScopeKind:       string(domain.Scope.Kind),
		OrganizationID:  organizationID,
		ProjectID:       projectID,
		KeyVersion:      domain.Sealed.KeyVersion,
		WrappedKeyNonce: cloneBytes(domain.Sealed.WrappedKeyNonce),
		WrappedDataKey:  cloneBytes(domain.Sealed.WrappedDataKey),
		PayloadNonce:    cloneBytes(domain.Sealed.PayloadNonce),
		Ciphertext:      cloneBytes(domain.Sealed.Ciphertext),
		Status:          string(domain.Status),
	}
}

func credentialToDomain(record *entity.ProviderCredential) (*catalogmodel.ProviderCredential, error) {
	scope, err := scopeToDomain(record.ScopeKind, record.OrganizationID, record.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("map provider credential scope: %w", err)
	}
	domain := &catalogmodel.ProviderCredential{
		Entity:     baseToDomain(record.BaseEntity),
		ProviderID: record.ProviderID,
		Scope:      scope,
		Sealed: catalogmodel.SealedCredential{
			KeyVersion:      record.KeyVersion,
			WrappedKeyNonce: cloneBytes(record.WrappedKeyNonce),
			WrappedDataKey:  cloneBytes(record.WrappedDataKey),
			PayloadNonce:    cloneBytes(record.PayloadNonce),
			Ciphertext:      cloneBytes(record.Ciphertext),
		},
		Status: sharedmodel.Status(record.Status),
	}
	if err := domain.Validate(); err != nil {
		return nil, fmt.Errorf("map provider credential: %w", err)
	}
	return domain, nil
}

func deploymentToEntity(domain *catalogmodel.Deployment) (*entity.Deployment, error) {
	capabilities, err := json.Marshal(domain.Capabilities)
	if err != nil {
		return nil, fmt.Errorf("encode deployment capabilities: %w", err)
	}
	organizationID, projectID := scopeIDs(domain.Scope)
	return &entity.Deployment{
		BaseEntity:     baseToEntity(domain.Entity),
		ProviderID:     domain.ProviderID,
		CredentialID:   cloneUUID(domain.CredentialID),
		Name:           domain.Name,
		UpstreamModel:  domain.UpstreamModel,
		ConnectorType:  domain.ConnectorType,
		ScopeKind:      string(domain.Scope.Kind),
		OrganizationID: organizationID,
		ProjectID:      projectID,
		Capabilities:   string(capabilities),
		Status:         string(domain.Status),
	}, nil
}

func deploymentToDomain(record *entity.Deployment) (*catalogmodel.Deployment, error) {
	scope, err := scopeToDomain(record.ScopeKind, record.OrganizationID, record.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("map deployment scope: %w", err)
	}
	var capabilities catalogmodel.CapabilitySet
	if err := json.Unmarshal([]byte(record.Capabilities), &capabilities); err != nil {
		return nil, fmt.Errorf("decode deployment capabilities: %w", err)
	}
	domain := &catalogmodel.Deployment{
		Entity:        baseToDomain(record.BaseEntity),
		ProviderID:    record.ProviderID,
		CredentialID:  cloneUUID(record.CredentialID),
		Name:          record.Name,
		UpstreamModel: record.UpstreamModel,
		ConnectorType: record.ConnectorType,
		Scope:         scope,
		Capabilities:  capabilities,
		Status:        sharedmodel.Status(record.Status),
	}
	if err := domain.Validate(); err != nil {
		return nil, fmt.Errorf("map deployment: %w", err)
	}
	return domain, nil
}

func modelAliasToEntity(domain *catalogmodel.ModelAlias) *entity.ModelAlias {
	return &entity.ModelAlias{
		BaseEntity: baseToEntity(domain.Entity),
		ProjectID:  domain.ProjectID,
		Name:       domain.Name,
		Status:     string(domain.Status),
	}
}

func modelAliasToDomain(record *entity.ModelAlias) (*catalogmodel.ModelAlias, error) {
	domain := &catalogmodel.ModelAlias{
		Entity:    baseToDomain(record.BaseEntity),
		ProjectID: record.ProjectID,
		Name:      record.Name,
		Status:    sharedmodel.Status(record.Status),
	}
	if err := domain.Validate(); err != nil {
		return nil, fmt.Errorf("map model alias: %w", err)
	}
	return domain, nil
}

func routeTargetToEntity(domain *catalogmodel.RouteTarget) *entity.RouteTarget {
	return &entity.RouteTarget{
		BaseEntity:   baseToEntity(domain.Entity),
		ModelAliasID: domain.ModelAliasID,
		DeploymentID: domain.DeploymentID,
		Priority:     domain.Priority,
		Weight:       domain.Weight,
		Status:       string(domain.Status),
	}
}

func routeTargetToDomain(record *entity.RouteTarget) (*catalogmodel.RouteTarget, error) {
	domain := &catalogmodel.RouteTarget{
		Entity:       baseToDomain(record.BaseEntity),
		ModelAliasID: record.ModelAliasID,
		DeploymentID: record.DeploymentID,
		Priority:     record.Priority,
		Weight:       record.Weight,
		Status:       sharedmodel.Status(record.Status),
	}
	if err := domain.Validate(); err != nil {
		return nil, fmt.Errorf("map route target: %w", err)
	}
	return domain, nil
}

func baseToEntity(domain sharedmodel.Entity) entity.BaseEntity {
	return entity.BaseEntity{ID: domain.ID, CreatedAt: domain.CreatedAt, UpdatedAt: domain.UpdatedAt}
}

func baseToDomain(record entity.BaseEntity) sharedmodel.Entity {
	return sharedmodel.Entity{ID: record.ID, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}
}

func scopeIDs(scope catalogmodel.Scope) (*uuid.UUID, *uuid.UUID) {
	switch scope.Kind {
	case catalogmodel.ScopeOrganization:
		return cloneUUID(&scope.OrganizationID), nil
	case catalogmodel.ScopeProject:
		return nil, cloneUUID(&scope.ProjectID)
	default:
		return nil, nil
	}
}

func scopeToDomain(kind string, organizationID, projectID *uuid.UUID) (catalogmodel.Scope, error) {
	scope := catalogmodel.Scope{Kind: catalogmodel.ScopeKind(kind)}
	if organizationID != nil {
		scope.OrganizationID = *organizationID
	}
	if projectID != nil {
		scope.ProjectID = *projectID
	}
	return scope, scope.Validate()
}

func cloneUUID(value *uuid.UUID) *uuid.UUID {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}

func utcNow() time.Time { return time.Now().UTC() }
