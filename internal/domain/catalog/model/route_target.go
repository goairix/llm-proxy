package model

import (
	"fmt"

	"github.com/google/uuid"

	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
)

// RouteTarget connects a project model alias to a deployment.
type RouteTarget struct {
	sharedmodel.Entity
	ModelAliasID uuid.UUID
	DeploymentID uuid.UUID
	Priority     int
	Weight       int
	Status       sharedmodel.Status
}

// NewRouteTarget creates an active route target.
func NewRouteTarget(modelAliasID, deploymentID uuid.UUID, priority, weight int) (*RouteTarget, error) {
	entity, err := sharedmodel.NewEntity()
	if err != nil {
		return nil, err
	}
	target := &RouteTarget{
		Entity:       entity,
		ModelAliasID: modelAliasID,
		DeploymentID: deploymentID,
		Priority:     priority,
		Weight:       weight,
		Status:       sharedmodel.StatusActive,
	}
	if err := target.Validate(); err != nil {
		return nil, err
	}
	return target, nil
}

// Validate checks route target references and selection values.
func (t RouteTarget) Validate() error {
	if t.ID == uuid.Nil || t.ModelAliasID == uuid.Nil || t.DeploymentID == uuid.Nil {
		return fmt.Errorf("%w: route target ids are required", sharederrors.ErrInvalid)
	}
	if t.Priority < 0 {
		return fmt.Errorf("%w: route target priority cannot be negative", sharederrors.ErrInvalid)
	}
	if t.Weight <= 0 {
		return fmt.Errorf("%w: route target weight must be positive", sharederrors.ErrInvalid)
	}
	if err := t.Status.Validate(); err != nil {
		return fmt.Errorf("route target status: %w", err)
	}
	return nil
}
