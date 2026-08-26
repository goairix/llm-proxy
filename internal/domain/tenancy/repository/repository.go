package repository

import (
	"context"

	"github.com/google/uuid"

	"github.com/goairix/llm-proxy/internal/domain/tenancy/model"
)

// OrganizationRepository persists organizations. Missing queries return (nil, nil).
type OrganizationRepository interface {
	Save(context.Context, *model.Organization) error
	FindByID(context.Context, uuid.UUID) (*model.Organization, error)
	List(context.Context, int, int) ([]model.Organization, error)
}

// ProjectRepository persists projects. Missing queries return (nil, nil).
type ProjectRepository interface {
	Save(context.Context, *model.Project) error
	FindByID(context.Context, uuid.UUID) (*model.Project, error)
	ListByOrganization(context.Context, uuid.UUID, int, int) ([]model.Project, error)
}

// VirtualKeyRepository persists virtual-key metadata and hashes, never plaintext keys.
type VirtualKeyRepository interface {
	Save(context.Context, *model.VirtualKey) error
	FindByID(context.Context, uuid.UUID) (*model.VirtualKey, error)
	ListByProject(context.Context, uuid.UUID, int, int) ([]model.VirtualKey, error)
}
