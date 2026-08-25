package model

import (
	"fmt"

	"github.com/google/uuid"

	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
)

// SealedCredential is an envelope-encrypted provider secret.
type SealedCredential struct {
	KeyVersion      string
	WrappedKeyNonce []byte
	WrappedDataKey  []byte
	PayloadNonce    []byte
	Ciphertext      []byte
}

// Validate checks that every part required to decrypt the envelope is present.
func (s SealedCredential) Validate() error {
	if s.KeyVersion == "" || len(s.WrappedKeyNonce) == 0 || len(s.WrappedDataKey) == 0 || len(s.PayloadNonce) == 0 || len(s.Ciphertext) == 0 {
		return fmt.Errorf("%w: sealed credential is incomplete", sharederrors.ErrInvalid)
	}
	return nil
}

// ProviderCredential stores only encrypted provider credentials and their authorization scope.
type ProviderCredential struct {
	sharedmodel.Entity
	ProviderID uuid.UUID
	Scope      Scope
	Sealed     SealedCredential
	Status     sharedmodel.Status
}

// NewProviderCredential creates an active encrypted credential.
func NewProviderCredential(providerID uuid.UUID, scope Scope, sealed SealedCredential) (*ProviderCredential, error) {
	entity, err := sharedmodel.NewEntity()
	if err != nil {
		return nil, err
	}
	return NewProviderCredentialWithEntity(entity, providerID, scope, sealed)
}

// NewProviderCredentialWithEntity completes a credential whose UUIDv7 identity was
// allocated before encryption so the identity can be authenticated as AAD.
func NewProviderCredentialWithEntity(entity sharedmodel.Entity, providerID uuid.UUID, scope Scope, sealed SealedCredential) (*ProviderCredential, error) {
	credential := &ProviderCredential{
		Entity:     entity,
		ProviderID: providerID,
		Scope:      scope,
		Sealed: SealedCredential{
			KeyVersion:      sealed.KeyVersion,
			WrappedKeyNonce: append([]byte(nil), sealed.WrappedKeyNonce...),
			WrappedDataKey:  append([]byte(nil), sealed.WrappedDataKey...),
			PayloadNonce:    append([]byte(nil), sealed.PayloadNonce...),
			Ciphertext:      append([]byte(nil), sealed.Ciphertext...),
		},
		Status: sharedmodel.StatusActive,
	}
	if err := credential.Validate(); err != nil {
		return nil, err
	}
	return credential, nil
}

// Validate checks credential identity, scope, encrypted envelope, and status.
func (c ProviderCredential) Validate() error {
	if c.ID == uuid.Nil {
		return fmt.Errorf("%w: provider credential id is required", sharederrors.ErrInvalid)
	}
	if c.ProviderID == uuid.Nil {
		return fmt.Errorf("%w: provider id is required", sharederrors.ErrInvalid)
	}
	if err := c.Scope.Validate(); err != nil {
		return fmt.Errorf("provider credential scope: %w", err)
	}
	if err := c.Sealed.Validate(); err != nil {
		return fmt.Errorf("provider credential envelope: %w", err)
	}
	if err := c.Status.Validate(); err != nil {
		return fmt.Errorf("provider credential status: %w", err)
	}
	return nil
}
