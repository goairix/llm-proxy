package tenancy

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
	tenantmodel "github.com/goairix/llm-proxy/internal/domain/tenancy/model"
	tenantrepository "github.com/goairix/llm-proxy/internal/domain/tenancy/repository"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/database"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/entity"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/transactions"
)

type OrganizationRepository struct{ manager transactions.DBManager }

func NewOrganizationRepository(manager transactions.DBManager) *OrganizationRepository {
	return &OrganizationRepository{manager: manager}
}

func (r *OrganizationRepository) Save(ctx context.Context, domain *tenantmodel.Organization) error {
	if domain == nil {
		return fmt.Errorf("%w: organization is required", sharederrors.ErrInvalid)
	}
	if err := domain.Validate(); err != nil {
		return err
	}
	db, err := r.manager.DB(ctx)
	if err != nil {
		return mapPersistenceError(err)
	}
	return mapPersistenceError(db.WithContext(ctx).Omit(clause.Associations).Save(organizationToEntity(domain)).Error)
}

func (r *OrganizationRepository) FindByID(ctx context.Context, id uuid.UUID) (*tenantmodel.Organization, error) {
	db, err := r.manager.DB(ctx)
	if err != nil {
		return nil, mapPersistenceError(err)
	}
	var record entity.Organization
	err = db.WithContext(ctx).First(&record, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, mapPersistenceError(err)
	}
	return organizationToDomain(&record)
}

func (r *OrganizationRepository) List(ctx context.Context, limit, offset int) ([]tenantmodel.Organization, error) {
	if err := validatePagination(limit, offset); err != nil {
		return nil, err
	}
	db, err := r.manager.DB(ctx)
	if err != nil {
		return nil, mapPersistenceError(err)
	}
	var records []entity.Organization
	if err := db.WithContext(ctx).Order("created_at ASC, id ASC").Limit(limit).Offset(offset).Find(&records).Error; err != nil {
		return nil, mapPersistenceError(err)
	}
	result := make([]tenantmodel.Organization, 0, len(records))
	for i := range records {
		domain, err := organizationToDomain(&records[i])
		if err != nil {
			return nil, err
		}
		result = append(result, *domain)
	}
	return result, nil
}

type ProjectRepository struct{ manager transactions.DBManager }

func NewProjectRepository(manager transactions.DBManager) *ProjectRepository {
	return &ProjectRepository{manager: manager}
}

func (r *ProjectRepository) Save(ctx context.Context, domain *tenantmodel.Project) error {
	if domain == nil {
		return fmt.Errorf("%w: project is required", sharederrors.ErrInvalid)
	}
	if err := domain.Validate(); err != nil {
		return err
	}
	db, err := r.manager.DB(ctx)
	if err != nil {
		return mapPersistenceError(err)
	}
	return mapPersistenceError(db.WithContext(ctx).Omit(clause.Associations).Save(projectToEntity(domain)).Error)
}

func (r *ProjectRepository) FindByID(ctx context.Context, id uuid.UUID) (*tenantmodel.Project, error) {
	db, err := r.manager.DB(ctx)
	if err != nil {
		return nil, mapPersistenceError(err)
	}
	var record entity.Project
	err = db.WithContext(ctx).First(&record, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, mapPersistenceError(err)
	}
	return projectToDomain(&record)
}

func (r *ProjectRepository) ListByOrganization(ctx context.Context, organizationID uuid.UUID, limit, offset int) ([]tenantmodel.Project, error) {
	if err := validatePagination(limit, offset); err != nil {
		return nil, err
	}
	db, err := r.manager.DB(ctx)
	if err != nil {
		return nil, mapPersistenceError(err)
	}
	var records []entity.Project
	if err := db.WithContext(ctx).Where("organization_id = ?", organizationID).Order("created_at ASC, id ASC").Limit(limit).Offset(offset).Find(&records).Error; err != nil {
		return nil, mapPersistenceError(err)
	}
	result := make([]tenantmodel.Project, 0, len(records))
	for i := range records {
		domain, err := projectToDomain(&records[i])
		if err != nil {
			return nil, err
		}
		result = append(result, *domain)
	}
	return result, nil
}

type VirtualKeyRepository struct{ manager transactions.DBManager }

func NewVirtualKeyRepository(manager transactions.DBManager) *VirtualKeyRepository {
	return &VirtualKeyRepository{manager: manager}
}

func (r *VirtualKeyRepository) Save(ctx context.Context, domain *tenantmodel.VirtualKey) error {
	if domain == nil {
		return fmt.Errorf("%w: virtual key is required", sharederrors.ErrInvalid)
	}
	if err := domain.Validate(); err != nil {
		return err
	}
	db, err := r.manager.DB(ctx)
	if err != nil {
		return mapPersistenceError(err)
	}
	return mapPersistenceError(db.WithContext(ctx).Omit(clause.Associations).Save(virtualKeyToEntity(domain)).Error)
}

func (r *VirtualKeyRepository) FindByID(ctx context.Context, id uuid.UUID) (*tenantmodel.VirtualKey, error) {
	db, err := r.manager.DB(ctx)
	if err != nil {
		return nil, mapPersistenceError(err)
	}
	var record entity.VirtualKey
	err = db.WithContext(ctx).First(&record, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, mapPersistenceError(err)
	}
	return virtualKeyToDomain(&record)
}

func (r *VirtualKeyRepository) ListByProject(ctx context.Context, projectID uuid.UUID, limit, offset int) ([]tenantmodel.VirtualKey, error) {
	if err := validatePagination(limit, offset); err != nil {
		return nil, err
	}
	db, err := r.manager.DB(ctx)
	if err != nil {
		return nil, mapPersistenceError(err)
	}
	var records []entity.VirtualKey
	if err := db.WithContext(ctx).Where("project_id = ?", projectID).Order("created_at ASC, id ASC").Limit(limit).Offset(offset).Find(&records).Error; err != nil {
		return nil, mapPersistenceError(err)
	}
	result := make([]tenantmodel.VirtualKey, 0, len(records))
	for i := range records {
		domain, err := virtualKeyToDomain(&records[i])
		if err != nil {
			return nil, err
		}
		result = append(result, *domain)
	}
	return result, nil
}

func validatePagination(limit, offset int) error {
	if limit < 1 || limit > 100 || offset < 0 {
		return fmt.Errorf("%w: pagination requires limit 1..100 and non-negative offset", sharederrors.ErrInvalid)
	}
	return nil
}

func mapPersistenceError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return fmt.Errorf("%w: persisted resource already exists", sharederrors.ErrConflict)
	}
	if errors.Is(err, database.ErrUnavailable) {
		return fmt.Errorf("%w: database unavailable", sharederrors.ErrDependencyUnavailable)
	}
	return err
}

var (
	_ tenantrepository.OrganizationRepository = (*OrganizationRepository)(nil)
	_ tenantrepository.ProjectRepository      = (*ProjectRepository)(nil)
	_ tenantrepository.VirtualKeyRepository   = (*VirtualKeyRepository)(nil)
)
