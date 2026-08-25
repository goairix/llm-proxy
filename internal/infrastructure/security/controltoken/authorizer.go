package controltoken

import (
	"crypto/sha256"
	"crypto/subtle"

	controlport "github.com/goairix/llm-proxy/internal/application/controlplane/port"
)

type Authorizer struct {
	digest     [32]byte
	configured bool
}

func NewAuthorizer(token string) *Authorizer {
	return &Authorizer{digest: sha256.Sum256([]byte(token)), configured: token != ""}
}

func (a *Authorizer) Authorize(token string) bool {
	if a == nil || !a.configured || token == "" {
		return false
	}
	digest := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(a.digest[:], digest[:]) == 1
}

var _ controlport.ControlPlaneAuthorizer = (*Authorizer)(nil)
