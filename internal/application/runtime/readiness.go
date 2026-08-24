package runtime

import "sync/atomic"

// Readiness stores the process readiness state shared by lifecycle and HTTP adapters.
type Readiness struct {
	ready atomic.Bool
}

// NewReadiness creates a readiness state that starts as not ready.
func NewReadiness() *Readiness {
	return &Readiness{}
}

// Ready reports whether the process is ready to serve traffic.
func (s *Readiness) Ready() bool {
	return s != nil && s.ready.Load()
}

// SetReady updates the process readiness state.
func (s *Readiness) SetReady(ready bool) {
	s.ready.Store(ready)
}
