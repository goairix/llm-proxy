package model

import (
	"fmt"
	"strings"

	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
)

// Provider describes a non-sensitive model-vendor integration.
type Provider struct {
	sharedmodel.Entity
	Name          string
	ConnectorType string
	Status        sharedmodel.Status
}

// NewProvider creates an active provider definition.
func NewProvider(name, connectorType string) (*Provider, error) {
	entity, err := sharedmodel.NewEntity()
	if err != nil {
		return nil, err
	}
	provider := &Provider{
		Entity:        entity,
		Name:          strings.TrimSpace(name),
		ConnectorType: strings.TrimSpace(connectorType),
		Status:        sharedmodel.StatusActive,
	}
	if err := provider.Validate(); err != nil {
		return nil, err
	}
	return provider, nil
}

// Validate checks provider invariants.
func (p Provider) Validate() error {
	if p.ID == [16]byte{} {
		return fmt.Errorf("%w: provider id is required", sharederrors.ErrInvalid)
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("%w: provider name is required", sharederrors.ErrInvalid)
	}
	if strings.TrimSpace(p.ConnectorType) == "" {
		return fmt.Errorf("%w: provider connector type is required", sharederrors.ErrInvalid)
	}
	if err := p.Status.Validate(); err != nil {
		return fmt.Errorf("provider status: %w", err)
	}
	return nil
}
