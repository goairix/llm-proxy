package port

import (
	"context"

	"github.com/google/uuid"

	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
)

// GeneratedVirtualKey contains the one-time plaintext and persistable metadata.
type GeneratedVirtualKey struct {
	Plaintext string
	Hash      [32]byte
	Prefix    string
	LastFour  string
}

type VirtualKeyGenerator interface {
	Generate() (GeneratedVirtualKey, error)
}

type CredentialCipher interface {
	Seal(context.Context, uuid.UUID, uuid.UUID, catalogmodel.Scope, []byte) (catalogmodel.SealedCredential, error)
	Open(context.Context, uuid.UUID, uuid.UUID, catalogmodel.Scope, catalogmodel.SealedCredential) ([]byte, error)
}

type ControlPlaneAuthorizer interface {
	Authorize(token string) bool
}
