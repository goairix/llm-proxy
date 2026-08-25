package repository

import (
	"context"

	"github.com/google/uuid"

	"github.com/goairix/llm-proxy/internal/domain/catalog/model"
)

// ProviderRepository persists non-sensitive provider definitions.
type ProviderRepository interface {
	Save(context.Context, *model.Provider) error
	FindByID(context.Context, uuid.UUID) (*model.Provider, error)
	List(context.Context, int, int) ([]model.Provider, error)
}

// ProviderCredentialRepository persists encrypted credentials. Missing queries return (nil, nil).
type ProviderCredentialRepository interface {
	Save(context.Context, *model.ProviderCredential) error
	FindByID(context.Context, uuid.UUID) (*model.ProviderCredential, error)
	List(context.Context, int, int) ([]model.ProviderCredential, error)
	ListByProvider(context.Context, uuid.UUID, int, int) ([]model.ProviderCredential, error)
}

// DeploymentRepository persists provider deployments.
type DeploymentRepository interface {
	Save(context.Context, *model.Deployment) error
	FindByID(context.Context, uuid.UUID) (*model.Deployment, error)
	List(context.Context, int, int) ([]model.Deployment, error)
	ListByProvider(context.Context, uuid.UUID, int, int) ([]model.Deployment, error)
}

// ModelAliasRepository persists project-visible model names.
type ModelAliasRepository interface {
	Save(context.Context, *model.ModelAlias) error
	FindByID(context.Context, uuid.UUID) (*model.ModelAlias, error)
	FindByProjectAndName(context.Context, uuid.UUID, string) (*model.ModelAlias, error)
	List(context.Context, int, int) ([]model.ModelAlias, error)
	ListByProject(context.Context, uuid.UUID, int, int) ([]model.ModelAlias, error)
}

// RouteTargetRepository persists alias-to-deployment routes.
type RouteTargetRepository interface {
	Save(context.Context, *model.RouteTarget) error
	FindByID(context.Context, uuid.UUID) (*model.RouteTarget, error)
	ListByModelAlias(context.Context, uuid.UUID) ([]model.RouteTarget, error)
}

// ConfigRevisionRepository increments and reads the global data-plane configuration version.
type ConfigRevisionRepository interface {
	Current(context.Context) (int64, error)
	Next(context.Context) (int64, error)
}
