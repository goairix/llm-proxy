package model

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
)

// VirtualKey identifies a project without retaining the plaintext key.
type VirtualKey struct {
	sharedmodel.Entity
	ProjectID uuid.UUID
	Name      string
	Hash      [32]byte
	Prefix    string
	LastFour  string
	Status    sharedmodel.Status
	ExpiresAt *time.Time
}

// NewVirtualKey creates an active project key from its already-derived hash.
func NewVirtualKey(projectID uuid.UUID, name string, hash [32]byte, prefix, lastFour string, expiresAt *time.Time) (*VirtualKey, error) {
	entity, err := sharedmodel.NewEntity()
	if err != nil {
		return nil, err
	}
	key := &VirtualKey{
		Entity:    entity,
		ProjectID: projectID,
		Name:      strings.TrimSpace(name),
		Hash:      hash,
		Prefix:    strings.TrimSpace(prefix),
		LastFour:  strings.TrimSpace(lastFour),
		Status:    sharedmodel.StatusActive,
	}
	if expiresAt != nil {
		expires := expiresAt.UTC()
		key.ExpiresAt = &expires
	}
	if err := key.Validate(); err != nil {
		return nil, err
	}
	return key, nil
}

// Validate checks virtual-key invariants and stored display metadata.
func (k VirtualKey) Validate() error {
	if k.ID == uuid.Nil {
		return fmt.Errorf("%w: virtual key id is required", sharederrors.ErrInvalid)
	}
	if k.ProjectID == uuid.Nil {
		return fmt.Errorf("%w: project id is required", sharederrors.ErrInvalid)
	}
	if strings.TrimSpace(k.Name) == "" {
		return fmt.Errorf("%w: virtual key name is required", sharederrors.ErrInvalid)
	}
	if k.Hash == [32]byte{} {
		return fmt.Errorf("%w: virtual key hash is required", sharederrors.ErrInvalid)
	}
	if strings.TrimSpace(k.Prefix) == "" {
		return fmt.Errorf("%w: virtual key prefix is required", sharederrors.ErrInvalid)
	}
	if len(strings.TrimSpace(k.LastFour)) != 4 {
		return fmt.Errorf("%w: virtual key last four must contain four characters", sharederrors.ErrInvalid)
	}
	if k.ExpiresAt != nil && k.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: virtual key expiration cannot be zero", sharederrors.ErrInvalid)
	}
	if err := k.Status.Validate(); err != nil {
		return fmt.Errorf("virtual key status: %w", err)
	}
	return nil
}

// ActiveAt reports whether the key is enabled and unexpired at the given time.
func (k VirtualKey) ActiveAt(now time.Time) bool {
	if !k.Status.Active() {
		return false
	}
	return k.ExpiresAt == nil || now.Before(*k.ExpiresAt)
}
