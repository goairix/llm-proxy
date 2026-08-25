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

type Deployment struct {
	ID            uuid.UUID
	ProviderID    uuid.UUID
	ConnectorType string
	UpstreamModel string
	Capabilities  catalogmodel.CapabilitySet
	Credential    *CredentialEnvelope
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
	result := plan
	if plan.Deployment.Credential != nil {
		credential := *plan.Deployment.Credential
		credential.Sealed = cloneSealedCredential(credential.Sealed)
		result.Deployment.Credential = &credential
	}
	return result
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
