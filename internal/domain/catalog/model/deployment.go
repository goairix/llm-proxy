package model

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
)

// Deployment describes a callable upstream model and its declared capabilities.
type Deployment struct {
	sharedmodel.Entity
	ProviderID       uuid.UUID
	Name             string
	UpstreamModel    string
	UpstreamProtocol UpstreamProtocol
	Scope            Scope
	Capabilities     CapabilitySet
	Status           sharedmodel.Status
}

// NewDeployment creates an active model deployment using provider-owned transport configuration.
func NewDeployment(providerID uuid.UUID, name, upstreamModel string, protocol UpstreamProtocol, scope Scope, capabilities CapabilitySet) (*Deployment, error) {
	entity, err := sharedmodel.NewEntity()
	if err != nil {
		return nil, err
	}
	deployment := &Deployment{
		Entity:           entity,
		ProviderID:       providerID,
		Name:             strings.TrimSpace(name),
		UpstreamModel:    strings.TrimSpace(upstreamModel),
		UpstreamProtocol: protocol,
		Scope:            scope,
		Capabilities:     capabilities,
		Status:           sharedmodel.StatusActive,
	}
	if err := deployment.Validate(); err != nil {
		return nil, err
	}
	return deployment, nil
}

// Validate checks deployment references and declared behavior.
func (d Deployment) Validate() error {
	if d.ID == uuid.Nil {
		return fmt.Errorf("%w: deployment id is required", sharederrors.ErrInvalid)
	}
	if d.ProviderID == uuid.Nil {
		return fmt.Errorf("%w: provider id is required", sharederrors.ErrInvalid)
	}
	if strings.TrimSpace(d.Name) == "" || strings.TrimSpace(d.UpstreamModel) == "" {
		return fmt.Errorf("%w: deployment name and upstream model are required", sharederrors.ErrInvalid)
	}
	if err := d.UpstreamProtocol.Validate(); err != nil {
		return fmt.Errorf("deployment upstream protocol: %w", err)
	}
	if err := d.Scope.Validate(); err != nil {
		return fmt.Errorf("deployment scope: %w", err)
	}
	if err := d.Capabilities.Validate(); err != nil {
		return fmt.Errorf("deployment capabilities: %w", err)
	}
	if err := d.Status.Validate(); err != nil {
		return fmt.Errorf("deployment status: %w", err)
	}
	return nil
}
