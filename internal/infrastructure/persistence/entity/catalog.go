package entity

import "github.com/google/uuid"

type Provider struct {
	BaseEntity
	Name          string `gorm:"type:varchar(255);not null"`
	ConnectorType string `gorm:"type:varchar(64);not null;index:idx_providers_connector_type"`
	Status        string `gorm:"type:varchar(32);not null;index:idx_providers_status"`
}

func (Provider) TableName() string { return "providers" }

type ProviderCredential struct {
	BaseEntity
	ProviderID      uuid.UUID     `gorm:"type:uuid;not null;index:idx_provider_credentials_provider_id"`
	Provider        Provider      `gorm:"foreignKey:ProviderID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
	ScopeKind       string        `gorm:"type:varchar(32);not null;index:idx_provider_credentials_scope"`
	OrganizationID  *uuid.UUID    `gorm:"type:uuid;index:idx_provider_credentials_organization_id"`
	Organization    *Organization `gorm:"foreignKey:OrganizationID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
	ProjectID       *uuid.UUID    `gorm:"type:uuid;index:idx_provider_credentials_project_id"`
	Project         *Project      `gorm:"foreignKey:ProjectID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
	KeyVersion      string        `gorm:"type:varchar(64);not null"`
	WrappedKeyNonce []byte        `gorm:"type:bytea;not null"`
	WrappedDataKey  []byte        `gorm:"type:bytea;not null"`
	PayloadNonce    []byte        `gorm:"type:bytea;not null"`
	Ciphertext      []byte        `gorm:"type:bytea;not null"`
	Status          string        `gorm:"type:varchar(32);not null;index:idx_provider_credentials_status"`
}

func (ProviderCredential) TableName() string { return "provider_credentials" }

type Deployment struct {
	BaseEntity
	ProviderID     uuid.UUID           `gorm:"type:uuid;not null;index:idx_deployments_provider_id"`
	Provider       Provider            `gorm:"foreignKey:ProviderID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
	CredentialID   *uuid.UUID          `gorm:"type:uuid;index:idx_deployments_credential_id"`
	Credential     *ProviderCredential `gorm:"foreignKey:CredentialID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
	Name           string              `gorm:"type:varchar(255);not null"`
	UpstreamModel  string              `gorm:"type:varchar(255);not null"`
	ConnectorType  string              `gorm:"type:varchar(64);not null;index:idx_deployments_connector_type"`
	ScopeKind      string              `gorm:"type:varchar(32);not null;index:idx_deployments_scope"`
	OrganizationID *uuid.UUID          `gorm:"type:uuid;index:idx_deployments_organization_id"`
	Organization   *Organization       `gorm:"foreignKey:OrganizationID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
	ProjectID      *uuid.UUID          `gorm:"type:uuid;index:idx_deployments_project_id"`
	Project        *Project            `gorm:"foreignKey:ProjectID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
	Capabilities   string              `gorm:"type:text;not null"`
	Status         string              `gorm:"type:varchar(32);not null;index:idx_deployments_status"`
}

func (Deployment) TableName() string { return "deployments" }

type ModelAlias struct {
	BaseEntity
	ProjectID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:ux_model_aliases_project_name,priority:1;index:idx_model_aliases_project_id"`
	Project   Project   `gorm:"foreignKey:ProjectID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
	Name      string    `gorm:"type:varchar(255);not null;uniqueIndex:ux_model_aliases_project_name,priority:2"`
	Status    string    `gorm:"type:varchar(32);not null;index:idx_model_aliases_status"`
}

func (ModelAlias) TableName() string { return "model_aliases" }

type RouteTarget struct {
	BaseEntity
	ModelAliasID uuid.UUID  `gorm:"type:uuid;not null;index:idx_route_targets_model_alias_id"`
	ModelAlias   ModelAlias `gorm:"foreignKey:ModelAliasID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
	DeploymentID uuid.UUID  `gorm:"type:uuid;not null;index:idx_route_targets_deployment_id"`
	Deployment   Deployment `gorm:"foreignKey:DeploymentID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
	Priority     int        `gorm:"not null;default:0"`
	Weight       int        `gorm:"not null"`
	Status       string     `gorm:"type:varchar(32);not null;index:idx_route_targets_status"`
}

func (RouteTarget) TableName() string { return "route_targets" }

type ConfigRevision struct {
	BaseEntity
	Revision int64 `gorm:"not null;uniqueIndex:ux_config_revisions_revision"`
}

func (ConfigRevision) TableName() string { return "config_revisions" }
