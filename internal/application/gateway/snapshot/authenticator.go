package snapshot

import (
	"crypto/sha256"
	"errors"
	"time"

	"github.com/google/uuid"

	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
)

var (
	ErrSnapshotUnavailable   = errors.New("runtime snapshot unavailable")
	ErrInvalidVirtualKey     = errors.New("invalid virtual key")
	ErrRouteNotFound         = errors.New("route not found")
	ErrProviderUnavailable   = errors.New("provider unavailable")
	ErrCredentialUnavailable = errors.New("provider credential unavailable")
)

// Session pins authentication and route resolution to one runtime revision.
type Session struct {
	snapshot *RuntimeSnapshot
}

func (s Session) Provider(providerID uuid.UUID) (Provider, error) {
	if s.snapshot == nil {
		return Provider{}, ErrSnapshotUnavailable
	}
	provider, ok := s.snapshot.providers[providerID]
	if !ok {
		return Provider{}, ErrProviderUnavailable
	}
	return provider, nil
}

func (s Session) CredentialPool(access AccessContext, providerID uuid.UUID) (CredentialPool, error) {
	if s.snapshot == nil {
		return CredentialPool{}, ErrSnapshotUnavailable
	}
	keys := [...]CredentialPoolKey{
		{ProviderID: providerID, ScopeKind: catalogmodel.ScopeProject, TargetID: access.ProjectID},
		{ProviderID: providerID, ScopeKind: catalogmodel.ScopeOrganization, TargetID: access.OrganizationID},
		{ProviderID: providerID, ScopeKind: catalogmodel.ScopePlatform},
	}
	for _, key := range keys {
		values := s.snapshot.credentials[key]
		if len(values) == 0 {
			continue
		}
		result := CredentialPool{Key: key, Credentials: make([]CredentialEnvelope, len(values))}
		for index := range values {
			result.Credentials[index] = cloneCredentialEnvelope(values[index])
		}
		return result, nil
	}
	return CredentialPool{}, ErrCredentialUnavailable
}

func (s Session) Authenticate(token string, now time.Time) (AccessContext, error) {
	if s.snapshot == nil {
		return AccessContext{}, ErrSnapshotUnavailable
	}
	if token == "" {
		return AccessContext{}, ErrInvalidVirtualKey
	}
	access, ok := s.snapshot.virtualKeys[sha256.Sum256([]byte(token))]
	if !ok || access.HasExpiry && !now.Before(access.ExpiresAt) {
		return AccessContext{}, ErrInvalidVirtualKey
	}
	return access, nil
}

func (s Session) Resolve(projectID uuid.UUID, model string) (RoutePlan, error) {
	if s.snapshot == nil {
		return RoutePlan{}, ErrSnapshotUnavailable
	}
	plan, ok := s.snapshot.routes[RouteKey{ProjectID: projectID, Model: model}]
	if !ok {
		return RoutePlan{}, ErrRouteNotFound
	}
	return cloneRoutePlan(plan), nil
}

func (s Session) Revision() int64 {
	if s.snapshot == nil {
		return 0
	}
	return s.snapshot.Revision()
}
