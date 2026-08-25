package model

import (
	"fmt"

	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
)

// ConfigRevision is the monotonic version of data-plane configuration.
type ConfigRevision struct {
	sharedmodel.Entity
	Revision int64
}

// NewConfigRevision creates a revision record with a UUIDv7 database identity.
func NewConfigRevision(revision int64) (*ConfigRevision, error) {
	if revision < 0 {
		return nil, fmt.Errorf("%w: config revision cannot be negative", sharederrors.ErrInvalid)
	}
	entity, err := sharedmodel.NewEntity()
	if err != nil {
		return nil, err
	}
	return &ConfigRevision{Entity: entity, Revision: revision}, nil
}
