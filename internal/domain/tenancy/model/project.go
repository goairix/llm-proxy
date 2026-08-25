package model

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
)

// Project is the isolation boundary used by virtual keys and routing policy.
type Project struct {
	sharedmodel.Entity
	OrganizationID uuid.UUID
	Name           string
	Status         sharedmodel.Status
}

// NewProject creates an active project under an organization.
func NewProject(organizationID uuid.UUID, name string) (*Project, error) {
	entity, err := sharedmodel.NewEntity()
	if err != nil {
		return nil, err
	}
	project := &Project{
		Entity:         entity,
		OrganizationID: organizationID,
		Name:           strings.TrimSpace(name),
		Status:         sharedmodel.StatusActive,
	}
	if err := project.Validate(); err != nil {
		return nil, err
	}
	return project, nil
}

// Validate checks project invariants.
func (p Project) Validate() error {
	if p.ID == uuid.Nil {
		return fmt.Errorf("%w: project id is required", sharederrors.ErrInvalid)
	}
	if p.OrganizationID == uuid.Nil {
		return fmt.Errorf("%w: organization id is required", sharederrors.ErrInvalid)
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("%w: project name is required", sharederrors.ErrInvalid)
	}
	if err := p.Status.Validate(); err != nil {
		return fmt.Errorf("project status: %w", err)
	}
	return nil
}
