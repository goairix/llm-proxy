package snapshot

import (
	"crypto/sha256"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrSnapshotUnavailable = errors.New("runtime snapshot unavailable")
	ErrInvalidVirtualKey   = errors.New("invalid virtual key")
	ErrRouteNotFound       = errors.New("route not found")
)

// Session pins authentication and route resolution to one runtime revision.
type Session struct {
	snapshot *RuntimeSnapshot
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
