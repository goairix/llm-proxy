package tenancy

import (
	"fmt"
	"time"

	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
	tenantmodel "github.com/goairix/llm-proxy/internal/domain/tenancy/model"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/entity"
)

func organizationToEntity(domain *tenantmodel.Organization) *entity.Organization {
	return &entity.Organization{
		BaseEntity: entity.BaseEntity{ID: domain.ID, CreatedAt: domain.CreatedAt, UpdatedAt: domain.UpdatedAt},
		Name:       domain.Name,
		Status:     string(domain.Status),
	}
}

func organizationToDomain(record *entity.Organization) (*tenantmodel.Organization, error) {
	domain := &tenantmodel.Organization{
		Entity: sharedmodel.Entity{ID: record.ID, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt},
		Name:   record.Name,
		Status: sharedmodel.Status(record.Status),
	}
	if err := domain.Validate(); err != nil {
		return nil, fmt.Errorf("map organization: %w", err)
	}
	return domain, nil
}

func projectToEntity(domain *tenantmodel.Project) *entity.Project {
	return &entity.Project{
		BaseEntity:     entity.BaseEntity{ID: domain.ID, CreatedAt: domain.CreatedAt, UpdatedAt: domain.UpdatedAt},
		OrganizationID: domain.OrganizationID,
		Name:           domain.Name,
		Status:         string(domain.Status),
	}
}

func projectToDomain(record *entity.Project) (*tenantmodel.Project, error) {
	domain := &tenantmodel.Project{
		Entity:         sharedmodel.Entity{ID: record.ID, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt},
		OrganizationID: record.OrganizationID,
		Name:           record.Name,
		Status:         sharedmodel.Status(record.Status),
	}
	if err := domain.Validate(); err != nil {
		return nil, fmt.Errorf("map project: %w", err)
	}
	return domain, nil
}

func virtualKeyToEntity(domain *tenantmodel.VirtualKey) *entity.VirtualKey {
	hash := make([]byte, len(domain.Hash))
	copy(hash, domain.Hash[:])
	return &entity.VirtualKey{
		BaseEntity: entity.BaseEntity{ID: domain.ID, CreatedAt: domain.CreatedAt, UpdatedAt: domain.UpdatedAt},
		ProjectID:  domain.ProjectID,
		Name:       domain.Name,
		Hash:       hash,
		Prefix:     domain.Prefix,
		LastFour:   domain.LastFour,
		Status:     string(domain.Status),
		ExpiresAt:  cloneTime(domain.ExpiresAt),
	}
}

func virtualKeyToDomain(record *entity.VirtualKey) (*tenantmodel.VirtualKey, error) {
	if len(record.Hash) != 32 {
		return nil, fmt.Errorf("%w: persisted virtual key hash length is %d, want 32", sharederrors.ErrInvalid, len(record.Hash))
	}
	var hash [32]byte
	copy(hash[:], record.Hash)
	domain := &tenantmodel.VirtualKey{
		Entity:    sharedmodel.Entity{ID: record.ID, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt},
		ProjectID: record.ProjectID,
		Name:      record.Name,
		Hash:      hash,
		Prefix:    record.Prefix,
		LastFour:  record.LastFour,
		Status:    sharedmodel.Status(record.Status),
		ExpiresAt: cloneTime(record.ExpiresAt),
	}
	if err := domain.Validate(); err != nil {
		return nil, fmt.Errorf("map virtual key: %w", err)
	}
	return domain, nil
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
