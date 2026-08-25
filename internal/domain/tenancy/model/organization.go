package model

import (
	"fmt"
	"strings"

	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
)

// Organization is the top-level tenant boundary.
type Organization struct {
	sharedmodel.Entity
	Name   string
	Status sharedmodel.Status
}

// NewOrganization creates an active organization.
func NewOrganization(name string) (*Organization, error) {
	entity, err := sharedmodel.NewEntity()
	if err != nil {
		return nil, err
	}
	organization := &Organization{
		Entity: entity,
		Name:   strings.TrimSpace(name),
		Status: sharedmodel.StatusActive,
	}
	if err := organization.Validate(); err != nil {
		return nil, err
	}
	return organization, nil
}

// Validate checks organization invariants.
func (o Organization) Validate() error {
	if o.ID == [16]byte{} {
		return fmt.Errorf("%w: organization id is required", sharederrors.ErrInvalid)
	}
	if strings.TrimSpace(o.Name) == "" {
		return fmt.Errorf("%w: organization name is required", sharederrors.ErrInvalid)
	}
	if err := o.Status.Validate(); err != nil {
		return fmt.Errorf("organization status: %w", err)
	}
	return nil
}
