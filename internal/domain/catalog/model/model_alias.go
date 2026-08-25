package model

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
)

// ModelAlias is the project-visible model name accepted by data-plane APIs.
type ModelAlias struct {
	sharedmodel.Entity
	ProjectID uuid.UUID
	Name      string
	Status    sharedmodel.Status
}

// NewModelAlias creates a disabled model alias that can be activated after its route is prepared.
func NewModelAlias(projectID uuid.UUID, name string) (*ModelAlias, error) {
	entity, err := sharedmodel.NewEntity()
	if err != nil {
		return nil, err
	}
	alias := &ModelAlias{
		Entity:    entity,
		ProjectID: projectID,
		Name:      strings.TrimSpace(name),
		Status:    sharedmodel.StatusDisabled,
	}
	if err := alias.Validate(); err != nil {
		return nil, err
	}
	return alias, nil
}

// Validate checks model alias invariants.
func (a ModelAlias) Validate() error {
	if a.ID == uuid.Nil {
		return fmt.Errorf("%w: model alias id is required", sharederrors.ErrInvalid)
	}
	if a.ProjectID == uuid.Nil {
		return fmt.Errorf("%w: project id is required", sharederrors.ErrInvalid)
	}
	if strings.TrimSpace(a.Name) == "" {
		return fmt.Errorf("%w: model alias name is required", sharederrors.ErrInvalid)
	}
	if err := a.Status.Validate(); err != nil {
		return fmt.Errorf("model alias status: %w", err)
	}
	return nil
}
