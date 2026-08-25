package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/goairix/llm-proxy/internal/application/controlplane/dto"
	controlport "github.com/goairix/llm-proxy/internal/application/controlplane/port"
	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	catalogrepository "github.com/goairix/llm-proxy/internal/domain/catalog/repository"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
	sharedport "github.com/goairix/llm-proxy/internal/domain/shared/port"
	tenantmodel "github.com/goairix/llm-proxy/internal/domain/tenancy/model"
	tenantrepository "github.com/goairix/llm-proxy/internal/domain/tenancy/repository"
)

type TenancyService struct {
	organizations tenantrepository.OrganizationRepository
	projects      tenantrepository.ProjectRepository
	virtualKeys   tenantrepository.VirtualKeyRepository
	revisions     catalogrepository.ConfigRevisionRepository
	transactions  sharedport.TransactionManager
	keyGenerator  controlport.VirtualKeyGenerator
	notifier      gatewayport.RefreshNotifier
}

func NewTenancyService(
	organizations tenantrepository.OrganizationRepository,
	projects tenantrepository.ProjectRepository,
	virtualKeys tenantrepository.VirtualKeyRepository,
	revisions catalogrepository.ConfigRevisionRepository,
	transactions sharedport.TransactionManager,
	keyGenerator controlport.VirtualKeyGenerator,
	notifier gatewayport.RefreshNotifier,
) *TenancyService {
	return &TenancyService{
		organizations: organizations, projects: projects, virtualKeys: virtualKeys, revisions: revisions,
		transactions: transactions, keyGenerator: keyGenerator, notifier: notifier,
	}
}

func (s *TenancyService) CreateOrganization(ctx context.Context, command dto.CreateOrganization) (dto.OrganizationResult, error) {
	organization, err := tenantmodel.NewOrganization(command.Name)
	if err != nil {
		return dto.OrganizationResult{}, mapApplicationError(err)
	}
	var revision int64
	err = s.mutate(ctx, func(txCtx context.Context) error {
		if err := s.organizations.Save(txCtx, organization); err != nil {
			return err
		}
		var err error
		revision, err = s.revisions.Next(txCtx)
		return err
	})
	if err != nil {
		return dto.OrganizationResult{}, mapApplicationError(err)
	}
	return dto.OrganizationResult{Organization: *organization, Revision: revision}, nil
}

func (s *TenancyService) CreateProject(ctx context.Context, command dto.CreateProject) (dto.ProjectResult, error) {
	var project *tenantmodel.Project
	var revision int64
	err := s.mutate(ctx, func(txCtx context.Context) error {
		organization, err := s.organizations.FindByID(txCtx, command.OrganizationID)
		if err != nil {
			return err
		}
		if organization == nil {
			return notFound("组织")
		}
		if organization.Status != sharedmodel.StatusActive {
			return disabled("组织")
		}
		project, err = tenantmodel.NewProject(command.OrganizationID, command.Name)
		if err != nil {
			return err
		}
		if err := s.projects.Save(txCtx, project); err != nil {
			return err
		}
		revision, err = s.revisions.Next(txCtx)
		return err
	})
	if err != nil {
		return dto.ProjectResult{}, mapApplicationError(err)
	}
	return dto.ProjectResult{Project: *project, Revision: revision}, nil
}

func (s *TenancyService) CreateVirtualKey(ctx context.Context, command dto.CreateVirtualKey) (dto.CreateVirtualKeyResult, error) {
	var key *tenantmodel.VirtualKey
	var generated controlport.GeneratedVirtualKey
	var revision int64
	err := s.mutate(ctx, func(txCtx context.Context) error {
		project, err := s.projects.FindByID(txCtx, command.ProjectID)
		if err != nil {
			return err
		}
		if project == nil {
			return notFound("项目")
		}
		if project.Status != sharedmodel.StatusActive {
			return disabled("项目")
		}
		organization, err := s.organizations.FindByID(txCtx, project.OrganizationID)
		if err != nil {
			return err
		}
		if organization == nil {
			return notFound("组织")
		}
		if organization.Status != sharedmodel.StatusActive {
			return disabled("组织")
		}
		generated, err = s.keyGenerator.Generate()
		if err != nil {
			return err
		}
		key, err = tenantmodel.NewVirtualKey(command.ProjectID, command.Name, generated.Hash, generated.Prefix, generated.LastFour, command.ExpiresAt)
		if err != nil {
			return err
		}
		if err := s.virtualKeys.Save(txCtx, key); err != nil {
			return err
		}
		revision, err = s.revisions.Next(txCtx)
		return err
	})
	if err != nil {
		return dto.CreateVirtualKeyResult{}, mapApplicationError(err)
	}
	return dto.CreateVirtualKeyResult{ID: key.ID, Secret: generated.Plaintext, Prefix: key.Prefix, LastFour: key.LastFour, ExpiresAt: key.ExpiresAt, Revision: revision}, nil
}

func (s *TenancyService) GetOrganization(ctx context.Context, id uuid.UUID) (*tenantmodel.Organization, error) {
	organization, err := s.organizations.FindByID(ctx, id)
	if err != nil {
		return nil, mapApplicationError(err)
	}
	if organization == nil {
		return nil, notFound("组织")
	}
	return organization, nil
}

func (s *TenancyService) ListOrganizations(ctx context.Context, pagination dto.Pagination) ([]tenantmodel.Organization, error) {
	organizations, err := s.organizations.List(ctx, pagination.Limit, pagination.Offset)
	return organizations, mapApplicationError(err)
}

func (s *TenancyService) UpdateOrganization(ctx context.Context, command dto.UpdateOrganization) (dto.OrganizationResult, error) {
	var organization *tenantmodel.Organization
	var revision int64
	err := s.mutate(ctx, func(txCtx context.Context) error {
		var err error
		organization, err = s.organizations.FindByID(txCtx, command.ID)
		if err != nil {
			return err
		}
		if organization == nil {
			return notFound("组织")
		}
		if command.Name != nil {
			organization.Name = *command.Name
		}
		if command.Status != nil {
			organization.Status = *command.Status
		}
		organization.UpdatedAt = time.Now().UTC()
		if err := organization.Validate(); err != nil {
			return err
		}
		if err := s.organizations.Save(txCtx, organization); err != nil {
			return err
		}
		revision, err = s.revisions.Next(txCtx)
		return err
	})
	if err != nil {
		return dto.OrganizationResult{}, mapApplicationError(err)
	}
	return dto.OrganizationResult{Organization: *organization, Revision: revision}, nil
}

func (s *TenancyService) GetProject(ctx context.Context, id uuid.UUID) (*tenantmodel.Project, error) {
	project, err := s.projects.FindByID(ctx, id)
	if err != nil {
		return nil, mapApplicationError(err)
	}
	if project == nil {
		return nil, notFound("项目")
	}
	return project, nil
}

func (s *TenancyService) ListProjects(ctx context.Context, organizationID uuid.UUID, pagination dto.Pagination) ([]tenantmodel.Project, error) {
	if _, err := s.GetOrganization(ctx, organizationID); err != nil {
		return nil, err
	}
	projects, err := s.projects.ListByOrganization(ctx, organizationID, pagination.Limit, pagination.Offset)
	return projects, mapApplicationError(err)
}

func (s *TenancyService) UpdateProject(ctx context.Context, command dto.UpdateProject) (dto.ProjectResult, error) {
	var project *tenantmodel.Project
	var revision int64
	err := s.mutate(ctx, func(txCtx context.Context) error {
		var err error
		project, err = s.projects.FindByID(txCtx, command.ID)
		if err != nil {
			return err
		}
		if project == nil {
			return notFound("项目")
		}
		organization, err := s.organizations.FindByID(txCtx, project.OrganizationID)
		if err != nil {
			return err
		}
		if organization == nil {
			return notFound("组织")
		}
		if command.Name != nil {
			project.Name = *command.Name
		}
		if command.Status != nil {
			project.Status = *command.Status
		}
		project.UpdatedAt = time.Now().UTC()
		if err := project.Validate(); err != nil {
			return err
		}
		if err := s.projects.Save(txCtx, project); err != nil {
			return err
		}
		revision, err = s.revisions.Next(txCtx)
		return err
	})
	if err != nil {
		return dto.ProjectResult{}, mapApplicationError(err)
	}
	return dto.ProjectResult{Project: *project, Revision: revision}, nil
}

func (s *TenancyService) GetVirtualKey(ctx context.Context, id uuid.UUID) (dto.VirtualKeyView, error) {
	key, err := s.virtualKeys.FindByID(ctx, id)
	if err != nil {
		return dto.VirtualKeyView{}, mapApplicationError(err)
	}
	if key == nil {
		return dto.VirtualKeyView{}, notFound("虚拟密钥")
	}
	return virtualKeyView(key), nil
}

func (s *TenancyService) ListVirtualKeys(ctx context.Context, projectID uuid.UUID, pagination dto.Pagination) ([]dto.VirtualKeyView, error) {
	if _, err := s.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	keys, err := s.virtualKeys.ListByProject(ctx, projectID, pagination.Limit, pagination.Offset)
	if err != nil {
		return nil, mapApplicationError(err)
	}
	result := make([]dto.VirtualKeyView, 0, len(keys))
	for i := range keys {
		result = append(result, virtualKeyView(&keys[i]))
	}
	return result, nil
}

func (s *TenancyService) UpdateVirtualKey(ctx context.Context, command dto.UpdateVirtualKey) (dto.VirtualKeyResult, error) {
	var key *tenantmodel.VirtualKey
	var revision int64
	err := s.mutate(ctx, func(txCtx context.Context) error {
		var err error
		key, err = s.virtualKeys.FindByID(txCtx, command.ID)
		if err != nil {
			return err
		}
		if key == nil {
			return notFound("虚拟密钥")
		}
		project, err := s.projects.FindByID(txCtx, key.ProjectID)
		if err != nil {
			return err
		}
		if project == nil {
			return notFound("项目")
		}
		organization, err := s.organizations.FindByID(txCtx, project.OrganizationID)
		if err != nil {
			return err
		}
		if organization == nil {
			return notFound("组织")
		}
		if command.Name != nil {
			key.Name = *command.Name
		}
		if command.Status != nil {
			key.Status = *command.Status
		}
		if command.ExpiresAt != nil {
			expires := command.ExpiresAt.UTC()
			key.ExpiresAt = &expires
		}
		key.UpdatedAt = time.Now().UTC()
		if err := key.Validate(); err != nil {
			return err
		}
		if err := s.virtualKeys.Save(txCtx, key); err != nil {
			return err
		}
		revision, err = s.revisions.Next(txCtx)
		return err
	})
	if err != nil {
		return dto.VirtualKeyResult{}, mapApplicationError(err)
	}
	return dto.VirtualKeyResult{VirtualKey: virtualKeyView(key), Revision: revision}, nil
}

func virtualKeyView(key *tenantmodel.VirtualKey) dto.VirtualKeyView {
	return dto.VirtualKeyView{
		ID: key.ID, ProjectID: key.ProjectID, Name: key.Name, Prefix: key.Prefix, LastFour: key.LastFour,
		Status: key.Status, ExpiresAt: key.ExpiresAt, CreatedAt: key.CreatedAt, UpdatedAt: key.UpdatedAt,
	}
}

func (s *TenancyService) mutate(ctx context.Context, fn func(context.Context) error) error {
	if err := s.transactions.Transaction(ctx, func(txCtx context.Context) error {
		if err := s.revisions.Lock(txCtx); err != nil {
			return err
		}
		return fn(txCtx)
	}); err != nil {
		return err
	}
	if s.notifier != nil {
		s.notifier.NotifyRefresh()
	}
	return nil
}
