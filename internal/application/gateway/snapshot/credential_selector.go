package snapshot

import (
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
)

// CredentialSelector distributes calls across the highest-priority available pool.
type CredentialSelector struct {
	cursors sync.Map
}

func NewCredentialSelector() *CredentialSelector { return &CredentialSelector{} }

func (s *CredentialSelector) Select(session Session, access AccessContext, providerID uuid.UUID) (CredentialEnvelope, error) {
	pool, err := session.CredentialPool(access, providerID)
	if err != nil {
		return CredentialEnvelope{}, err
	}
	cursorValue, _ := s.cursors.LoadOrStore(pool.Key, &atomic.Uint64{})
	index := cursorValue.(*atomic.Uint64).Add(1) - 1
	return cloneCredentialEnvelope(pool.Credentials[index%uint64(len(pool.Credentials))]), nil
}
