package snapshot

import "sync/atomic"

// Store atomically publishes complete immutable runtime snapshots.
type Store struct {
	current atomic.Pointer[RuntimeSnapshot]
}

func NewStore() *Store {
	return &Store{}
}

func (s *Store) Current() (*RuntimeSnapshot, bool) {
	if s == nil {
		return nil, false
	}
	current := s.current.Load()
	return current, current != nil
}

func (s *Store) Publish(next *RuntimeSnapshot) {
	if next == nil {
		panic("publish nil runtime snapshot")
	}
	if !next.compiled {
		panic("publish runtime snapshot not created by compiler")
	}
	s.current.Store(next)
}

// Begin pins one snapshot for the complete lifetime of a gateway request.
func (s *Store) Begin() (Session, error) {
	current, ok := s.Current()
	if !ok {
		return Session{}, ErrSnapshotUnavailable
	}
	return Session{snapshot: current}, nil
}
