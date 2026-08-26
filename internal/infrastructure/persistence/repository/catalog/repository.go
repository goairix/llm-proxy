package catalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	catalogrepository "github.com/goairix/llm-proxy/internal/domain/catalog/repository"
	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/database"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/entity"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/transactions"
)

type ProviderRepository struct{ manager transactions.DBManager }

func NewProviderRepository(manager transactions.DBManager) *ProviderRepository {
	return &ProviderRepository{manager: manager}
}

func (r *ProviderRepository) Save(ctx context.Context, domain *catalogmodel.Provider) error {
	if domain == nil {
		return invalid("provider is required")
	}
	if err := domain.Validate(); err != nil {
		return err
	}
	record := providerToEntity(domain)
	return r.save(ctx, record)
}

func (r *ProviderRepository) FindByID(ctx context.Context, id uuid.UUID) (*catalogmodel.Provider, error) {
	var record entity.Provider
	found, err := r.find(ctx, &record, "id = ?", id)
	if err != nil || !found {
		return nil, err
	}
	return providerToDomain(&record)
}

func (r *ProviderRepository) List(ctx context.Context, limit, offset int) ([]catalogmodel.Provider, error) {
	var records []entity.Provider
	if err := r.list(ctx, &records, nil, limit, offset); err != nil {
		return nil, err
	}
	result := make([]catalogmodel.Provider, 0, len(records))
	for i := range records {
		domain, err := providerToDomain(&records[i])
		if err != nil {
			return nil, err
		}
		result = append(result, *domain)
	}
	return result, nil
}

type ProviderCredentialRepository struct{ manager transactions.DBManager }

func NewProviderCredentialRepository(manager transactions.DBManager) *ProviderCredentialRepository {
	return &ProviderCredentialRepository{manager: manager}
}

func (r *ProviderCredentialRepository) Save(ctx context.Context, domain *catalogmodel.ProviderCredential) error {
	if domain == nil {
		return invalid("provider credential is required")
	}
	if err := domain.Validate(); err != nil {
		return err
	}
	return r.save(ctx, credentialToEntity(domain))
}

func (r *ProviderCredentialRepository) FindByID(ctx context.Context, id uuid.UUID) (*catalogmodel.ProviderCredential, error) {
	var record entity.ProviderCredential
	found, err := r.find(ctx, &record, "id = ?", id)
	if err != nil || !found {
		return nil, err
	}
	return credentialToDomain(&record)
}

func (r *ProviderCredentialRepository) List(ctx context.Context, limit, offset int) ([]catalogmodel.ProviderCredential, error) {
	return r.listCredentials(ctx, nil, limit, offset)
}

func (r *ProviderCredentialRepository) ListByProvider(ctx context.Context, providerID uuid.UUID, limit, offset int) ([]catalogmodel.ProviderCredential, error) {
	return r.listCredentials(ctx, func(db *gorm.DB) *gorm.DB { return db.Where("provider_id = ?", providerID) }, limit, offset)
}

func (r *ProviderCredentialRepository) listCredentials(ctx context.Context, filter queryFilter, limit, offset int) ([]catalogmodel.ProviderCredential, error) {
	var records []entity.ProviderCredential
	if err := r.list(ctx, &records, filter, limit, offset); err != nil {
		return nil, err
	}
	result := make([]catalogmodel.ProviderCredential, 0, len(records))
	for i := range records {
		domain, err := credentialToDomain(&records[i])
		if err != nil {
			return nil, err
		}
		result = append(result, *domain)
	}
	return result, nil
}

type DeploymentRepository struct{ manager transactions.DBManager }

func NewDeploymentRepository(manager transactions.DBManager) *DeploymentRepository {
	return &DeploymentRepository{manager: manager}
}

func (r *DeploymentRepository) Save(ctx context.Context, domain *catalogmodel.Deployment) error {
	if domain == nil {
		return invalid("deployment is required")
	}
	if err := domain.Validate(); err != nil {
		return err
	}
	record, err := deploymentToEntity(domain)
	if err != nil {
		return err
	}
	return r.save(ctx, record)
}

func (r *DeploymentRepository) FindByID(ctx context.Context, id uuid.UUID) (*catalogmodel.Deployment, error) {
	var record entity.Deployment
	found, err := r.find(ctx, &record, "id = ?", id)
	if err != nil || !found {
		return nil, err
	}
	return deploymentToDomain(&record)
}

func (r *DeploymentRepository) List(ctx context.Context, limit, offset int) ([]catalogmodel.Deployment, error) {
	return r.listDeployments(ctx, nil, limit, offset)
}

func (r *DeploymentRepository) ListByProvider(ctx context.Context, providerID uuid.UUID, limit, offset int) ([]catalogmodel.Deployment, error) {
	return r.listDeployments(ctx, func(db *gorm.DB) *gorm.DB { return db.Where("provider_id = ?", providerID) }, limit, offset)
}

func (r *DeploymentRepository) HasActiveByProvider(ctx context.Context, providerID uuid.UUID) (bool, error) {
	return (repositoryBase{r.manager}).exists(ctx, &entity.Deployment{}, "provider_id = ? AND status = ?", providerID, sharedmodel.StatusActive)
}

func (r *DeploymentRepository) HasActiveByCredential(ctx context.Context, credentialID uuid.UUID) (bool, error) {
	return (repositoryBase{r.manager}).exists(ctx, &entity.Deployment{}, "credential_id = ? AND status = ?", credentialID, sharedmodel.StatusActive)
}

func (r *DeploymentRepository) listDeployments(ctx context.Context, filter queryFilter, limit, offset int) ([]catalogmodel.Deployment, error) {
	var records []entity.Deployment
	if err := r.list(ctx, &records, filter, limit, offset); err != nil {
		return nil, err
	}
	result := make([]catalogmodel.Deployment, 0, len(records))
	for i := range records {
		domain, err := deploymentToDomain(&records[i])
		if err != nil {
			return nil, err
		}
		result = append(result, *domain)
	}
	return result, nil
}

type ModelAliasRepository struct{ manager transactions.DBManager }

func NewModelAliasRepository(manager transactions.DBManager) *ModelAliasRepository {
	return &ModelAliasRepository{manager: manager}
}

func (r *ModelAliasRepository) Save(ctx context.Context, domain *catalogmodel.ModelAlias) error {
	if domain == nil {
		return invalid("model alias is required")
	}
	if err := domain.Validate(); err != nil {
		return err
	}
	return r.save(ctx, modelAliasToEntity(domain))
}

func (r *ModelAliasRepository) FindByID(ctx context.Context, id uuid.UUID) (*catalogmodel.ModelAlias, error) {
	var record entity.ModelAlias
	found, err := r.find(ctx, &record, "id = ?", id)
	if err != nil || !found {
		return nil, err
	}
	return modelAliasToDomain(&record)
}

func (r *ModelAliasRepository) FindByProjectAndName(ctx context.Context, projectID uuid.UUID, name string) (*catalogmodel.ModelAlias, error) {
	var record entity.ModelAlias
	found, err := r.find(ctx, &record, "project_id = ? AND name = ?", projectID, name)
	if err != nil || !found {
		return nil, err
	}
	return modelAliasToDomain(&record)
}

func (r *ModelAliasRepository) List(ctx context.Context, limit, offset int) ([]catalogmodel.ModelAlias, error) {
	return r.listAliases(ctx, nil, limit, offset)
}

func (r *ModelAliasRepository) ListByProject(ctx context.Context, projectID uuid.UUID, limit, offset int) ([]catalogmodel.ModelAlias, error) {
	return r.listAliases(ctx, func(db *gorm.DB) *gorm.DB { return db.Where("project_id = ?", projectID) }, limit, offset)
}

func (r *ModelAliasRepository) listAliases(ctx context.Context, filter queryFilter, limit, offset int) ([]catalogmodel.ModelAlias, error) {
	var records []entity.ModelAlias
	if err := r.list(ctx, &records, filter, limit, offset); err != nil {
		return nil, err
	}
	result := make([]catalogmodel.ModelAlias, 0, len(records))
	for i := range records {
		domain, err := modelAliasToDomain(&records[i])
		if err != nil {
			return nil, err
		}
		result = append(result, *domain)
	}
	return result, nil
}

type RouteTargetRepository struct{ manager transactions.DBManager }

func NewRouteTargetRepository(manager transactions.DBManager) *RouteTargetRepository {
	return &RouteTargetRepository{manager: manager}
}

func (r *RouteTargetRepository) Save(ctx context.Context, domain *catalogmodel.RouteTarget) error {
	if domain == nil {
		return invalid("route target is required")
	}
	if err := domain.Validate(); err != nil {
		return err
	}
	return r.save(ctx, routeTargetToEntity(domain))
}

func (r *RouteTargetRepository) FindByID(ctx context.Context, id uuid.UUID) (*catalogmodel.RouteTarget, error) {
	var record entity.RouteTarget
	found, err := r.find(ctx, &record, "id = ?", id)
	if err != nil || !found {
		return nil, err
	}
	return routeTargetToDomain(&record)
}

func (r *RouteTargetRepository) ListByModelAlias(ctx context.Context, modelAliasID uuid.UUID) ([]catalogmodel.RouteTarget, error) {
	db, err := r.manager.DB(ctx)
	if err != nil {
		return nil, mapPersistenceError(err)
	}
	var records []entity.RouteTarget
	if err := db.WithContext(ctx).Where("model_alias_id = ?", modelAliasID).Order("priority ASC, created_at ASC, id ASC").Find(&records).Error; err != nil {
		return nil, mapPersistenceError(err)
	}
	result := make([]catalogmodel.RouteTarget, 0, len(records))
	for i := range records {
		domain, err := routeTargetToDomain(&records[i])
		if err != nil {
			return nil, err
		}
		result = append(result, *domain)
	}
	return result, nil
}

func (r *RouteTargetRepository) HasActiveByDeployment(ctx context.Context, deploymentID uuid.UUID) (bool, error) {
	return (repositoryBase{r.manager}).exists(ctx, &entity.RouteTarget{}, "deployment_id = ? AND status = ?", deploymentID, sharedmodel.StatusActive)
}

type ConfigRevisionRepository struct{ manager transactions.DBManager }

func NewConfigRevisionRepository(manager transactions.DBManager) *ConfigRevisionRepository {
	return &ConfigRevisionRepository{manager: manager}
}

// Lock serializes all configuration mutations before they validate code-managed associations.
func (r *ConfigRevisionRepository) Lock(ctx context.Context) error {
	db, err := r.manager.DB(ctx)
	if err != nil {
		return mapPersistenceError(err)
	}
	var record entity.ConfigRevision
	return mapPersistenceError(db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Order("revision DESC").First(&record).Error)
}

func (r *ConfigRevisionRepository) Current(ctx context.Context) (int64, error) {
	db, err := r.manager.DB(ctx)
	if err != nil {
		return 0, mapPersistenceError(err)
	}
	var record entity.ConfigRevision
	if err := db.WithContext(ctx).Order("revision DESC").First(&record).Error; err != nil {
		return 0, mapPersistenceError(err)
	}
	return record.Revision, nil
}

func (r *ConfigRevisionRepository) Next(ctx context.Context) (int64, error) {
	db, err := r.manager.DB(ctx)
	if err != nil {
		return 0, mapPersistenceError(err)
	}
	var record entity.ConfigRevision
	if err := db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Order("revision DESC").First(&record).Error; err != nil {
		return 0, mapPersistenceError(err)
	}
	next := record.Revision + 1
	if err := db.WithContext(ctx).Model(&record).Updates(map[string]any{"revision": next, "updated_at": utcNow()}).Error; err != nil {
		return 0, mapPersistenceError(err)
	}
	return next, nil
}

type repositoryBase struct{ manager transactions.DBManager }
type queryFilter func(*gorm.DB) *gorm.DB

func (r repositoryBase) save(ctx context.Context, record any) error {
	db, err := r.manager.DB(ctx)
	if err != nil {
		return mapPersistenceError(err)
	}
	return mapPersistenceError(db.WithContext(ctx).Omit(clause.Associations).Save(record).Error)
}

func (r repositoryBase) find(ctx context.Context, record any, query string, args ...any) (bool, error) {
	db, err := r.manager.DB(ctx)
	if err != nil {
		return false, mapPersistenceError(err)
	}
	conditions := append([]any{query}, args...)
	err = db.WithContext(ctx).First(record, conditions...).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil, mapPersistenceError(err)
}

func (r repositoryBase) exists(ctx context.Context, record any, query string, args ...any) (bool, error) {
	db, err := r.manager.DB(ctx)
	if err != nil {
		return false, mapPersistenceError(err)
	}
	conditions := append([]any{query}, args...)
	err = db.WithContext(ctx).Select("id").Take(record, conditions...).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil, mapPersistenceError(err)
}

func (r repositoryBase) list(ctx context.Context, records any, filter queryFilter, limit, offset int) error {
	if err := validatePagination(limit, offset); err != nil {
		return err
	}
	db, err := r.manager.DB(ctx)
	if err != nil {
		return mapPersistenceError(err)
	}
	query := db.WithContext(ctx)
	if filter != nil {
		query = filter(query)
	}
	return mapPersistenceError(query.Order("created_at ASC, id ASC").Limit(limit).Offset(offset).Find(records).Error)
}

func (r *ProviderRepository) save(ctx context.Context, record any) error {
	return (repositoryBase{r.manager}).save(ctx, record)
}
func (r *ProviderRepository) find(ctx context.Context, record any, query string, args ...any) (bool, error) {
	return (repositoryBase{r.manager}).find(ctx, record, query, args...)
}
func (r *ProviderRepository) list(ctx context.Context, records any, filter queryFilter, limit, offset int) error {
	return (repositoryBase{r.manager}).list(ctx, records, filter, limit, offset)
}
func (r *ProviderCredentialRepository) save(ctx context.Context, record any) error {
	return (repositoryBase{r.manager}).save(ctx, record)
}
func (r *ProviderCredentialRepository) find(ctx context.Context, record any, query string, args ...any) (bool, error) {
	return (repositoryBase{r.manager}).find(ctx, record, query, args...)
}
func (r *ProviderCredentialRepository) list(ctx context.Context, records any, filter queryFilter, limit, offset int) error {
	return (repositoryBase{r.manager}).list(ctx, records, filter, limit, offset)
}
func (r *DeploymentRepository) save(ctx context.Context, record any) error {
	return (repositoryBase{r.manager}).save(ctx, record)
}
func (r *DeploymentRepository) find(ctx context.Context, record any, query string, args ...any) (bool, error) {
	return (repositoryBase{r.manager}).find(ctx, record, query, args...)
}
func (r *DeploymentRepository) list(ctx context.Context, records any, filter queryFilter, limit, offset int) error {
	return (repositoryBase{r.manager}).list(ctx, records, filter, limit, offset)
}
func (r *ModelAliasRepository) save(ctx context.Context, record any) error {
	return (repositoryBase{r.manager}).save(ctx, record)
}
func (r *ModelAliasRepository) find(ctx context.Context, record any, query string, args ...any) (bool, error) {
	return (repositoryBase{r.manager}).find(ctx, record, query, args...)
}
func (r *ModelAliasRepository) list(ctx context.Context, records any, filter queryFilter, limit, offset int) error {
	return (repositoryBase{r.manager}).list(ctx, records, filter, limit, offset)
}
func (r *RouteTargetRepository) save(ctx context.Context, record any) error {
	return (repositoryBase{r.manager}).save(ctx, record)
}
func (r *RouteTargetRepository) find(ctx context.Context, record any, query string, args ...any) (bool, error) {
	return (repositoryBase{r.manager}).find(ctx, record, query, args...)
}

func validatePagination(limit, offset int) error {
	if limit < 1 || limit > 100 || offset < 0 {
		return invalid("pagination requires limit 1..100 and non-negative offset")
	}
	return nil
}

func invalid(message string) error { return fmt.Errorf("%w: %s", sharederrors.ErrInvalid, message) }

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
	_ catalogrepository.ProviderRepository           = (*ProviderRepository)(nil)
	_ catalogrepository.ProviderCredentialRepository = (*ProviderCredentialRepository)(nil)
	_ catalogrepository.DeploymentRepository         = (*DeploymentRepository)(nil)
	_ catalogrepository.ModelAliasRepository         = (*ModelAliasRepository)(nil)
	_ catalogrepository.RouteTargetRepository        = (*RouteTargetRepository)(nil)
	_ catalogrepository.ConfigRevisionRepository     = (*ConfigRevisionRepository)(nil)
)
