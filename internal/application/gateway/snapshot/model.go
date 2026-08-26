package snapshot

import (
	"time"

	"github.com/google/uuid"

	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
)

type AccessContext struct {
	OrganizationID uuid.UUID
	ProjectID      uuid.UUID
	VirtualKeyID   uuid.UUID
	ExpiresAt      time.Time
	HasExpiry      bool
}

type RouteKey struct {
	ProjectID uuid.UUID
	Model     string
}

type CredentialEnvelope struct {
	CredentialID uuid.UUID
	ProviderID   uuid.UUID
	Scope        catalogmodel.Scope
	Sealed       catalogmodel.SealedCredential
}

type Provider struct {
	ID            uuid.UUID
	ConnectorType string
	BaseURL       string
}

type CredentialPoolKey struct {
	ProviderID uuid.UUID
	ScopeKind  catalogmodel.ScopeKind
	TargetID   uuid.UUID
}

type CredentialPool struct {
	Key         CredentialPoolKey
	Credentials []CredentialEnvelope
}

type Deployment struct {
	ID               uuid.UUID
	ProviderID       uuid.UUID
	UpstreamModel    string
	UpstreamProtocol catalogmodel.UpstreamProtocol
	Capabilities     catalogmodel.CapabilitySet
}

type RoutePlan struct {
	AliasID    uuid.UUID
	Alias      string
	Deployment Deployment
}

type RuntimeSnapshot struct {
	revision    int64
	builtAt     time.Time
	virtualKeys map[[32]byte]AccessContext
	routes      map[RouteKey]RoutePlan
	providers   map[uuid.UUID]Provider
	credentials map[CredentialPoolKey][]CredentialEnvelope
	compiled    bool
}

func (s *RuntimeSnapshot) Revision() int64 {
	if s == nil {
		return 0
	}
	return s.revision
}

func (s *RuntimeSnapshot) BuiltAt() time.Time {
	if s == nil {
		return time.Time{}
	}
	return s.builtAt
}

func cloneRoutePlan(plan RoutePlan) RoutePlan {
	return plan
}

func cloneSealedCredential(sealed catalogmodel.SealedCredential) catalogmodel.SealedCredential {
	return catalogmodel.SealedCredential{
		KeyVersion:      sealed.KeyVersion,
		WrappedKeyNonce: append([]byte(nil), sealed.WrappedKeyNonce...),
		WrappedDataKey:  append([]byte(nil), sealed.WrappedDataKey...),
		PayloadNonce:    append([]byte(nil), sealed.PayloadNonce...),
		Ciphertext:      append([]byte(nil), sealed.Ciphertext...),
	}
}

func cloneCredentialEnvelope(value CredentialEnvelope) CredentialEnvelope {
	result := value
	result.Sealed = cloneSealedCredential(value.Sealed)
	return result
}
