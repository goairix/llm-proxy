package model

import (
	"fmt"

	"github.com/google/uuid"

	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
)

// ScopeKind identifies who may use a credential or deployment.
type ScopeKind string

const (
	ScopePlatform     ScopeKind = "platform"
	ScopeOrganization ScopeKind = "organization"
	ScopeProject      ScopeKind = "project"
)

// Scope encodes a mutually exclusive platform, organization, or project target.
// A project's organization is derived from the project aggregate and is not duplicated here.
type Scope struct {
	Kind           ScopeKind
	OrganizationID uuid.UUID
	ProjectID      uuid.UUID
}

// Validate checks the target combination for the selected scope kind.
func (s Scope) Validate() error {
	switch s.Kind {
	case ScopePlatform:
		if s.OrganizationID != uuid.Nil || s.ProjectID != uuid.Nil {
			return fmt.Errorf("%w: platform scope cannot target an organization or project", sharederrors.ErrInvalid)
		}
	case ScopeOrganization:
		if s.OrganizationID == uuid.Nil || s.ProjectID != uuid.Nil {
			return fmt.Errorf("%w: organization scope requires only an organization id", sharederrors.ErrInvalid)
		}
	case ScopeProject:
		if s.OrganizationID != uuid.Nil || s.ProjectID == uuid.Nil {
			return fmt.Errorf("%w: project scope requires only a project id", sharederrors.ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unsupported scope kind %q", sharederrors.ErrInvalid, s.Kind)
	}
	return nil
}
