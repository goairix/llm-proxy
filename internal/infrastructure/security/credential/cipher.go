package credential

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"

	controlport "github.com/goairix/llm-proxy/internal/application/controlplane/port"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
)

const (
	dataKeyBytes = 32
	aadPrefix    = "llm-proxy/credential/v1"
)

type Cipher struct {
	currentVersion string
	keys           map[string][dataKeyBytes]byte
	random         io.Reader
}

func NewCipher(currentVersion string, encodedKeys map[string]string) (*Cipher, error) {
	return newCipher(currentVersion, encodedKeys, rand.Reader)
}

func newCipher(currentVersion string, encodedKeys map[string]string, random io.Reader) (*Cipher, error) {
	if currentVersion == "" {
		return nil, fmt.Errorf("credential current key version is required")
	}
	if random == nil {
		return nil, fmt.Errorf("credential random source is required")
	}
	keys := make(map[string][dataKeyBytes]byte, len(encodedKeys))
	for version, encoded := range encodedKeys {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode credential key version %q: %w", version, err)
		}
		if len(decoded) != dataKeyBytes {
			return nil, fmt.Errorf("credential key version %q has %d bytes, want %d", version, len(decoded), dataKeyBytes)
		}
		var key [dataKeyBytes]byte
		copy(key[:], decoded)
		keys[version] = key
		clear(decoded)
	}
	if _, ok := keys[currentVersion]; !ok {
		return nil, fmt.Errorf("credential current key version %q is not in keyring", currentVersion)
	}
	return &Cipher{currentVersion: currentVersion, keys: keys, random: random}, nil
}

func (c *Cipher) Seal(ctx context.Context, credentialID, providerID uuid.UUID, scope catalogmodel.Scope, plaintext []byte) (catalogmodel.SealedCredential, error) {
	if err := ctx.Err(); err != nil {
		return catalogmodel.SealedCredential{}, err
	}
	aad, err := credentialAAD(credentialID, providerID, scope)
	if err != nil {
		return catalogmodel.SealedCredential{}, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, plaintext); err != nil {
		return catalogmodel.SealedCredential{}, fmt.Errorf("credential payload must be valid JSON: %w", err)
	}

	dataKey := make([]byte, dataKeyBytes)
	if _, err := io.ReadFull(c.random, dataKey); err != nil {
		return catalogmodel.SealedCredential{}, fmt.Errorf("generate credential data key: %w", err)
	}
	defer clear(dataKey)
	payloadAEAD, err := newGCM(dataKey)
	if err != nil {
		return catalogmodel.SealedCredential{}, err
	}
	payloadNonce, err := randomNonce(c.random, payloadAEAD.NonceSize())
	if err != nil {
		return catalogmodel.SealedCredential{}, fmt.Errorf("generate credential payload nonce: %w", err)
	}
	ciphertext := payloadAEAD.Seal(nil, payloadNonce, compact.Bytes(), appendAAD(aad, "payload"))

	key := c.keys[c.currentVersion]
	wrappingAEAD, err := newGCM(key[:])
	if err != nil {
		return catalogmodel.SealedCredential{}, err
	}
	wrappedNonce, err := randomNonce(c.random, wrappingAEAD.NonceSize())
	if err != nil {
		return catalogmodel.SealedCredential{}, fmt.Errorf("generate wrapped-key nonce: %w", err)
	}
	wrappedDataKey := wrappingAEAD.Seal(nil, wrappedNonce, dataKey, appendAAD(aad, "dek"))

	return catalogmodel.SealedCredential{
		KeyVersion:      c.currentVersion,
		WrappedKeyNonce: wrappedNonce,
		WrappedDataKey:  wrappedDataKey,
		PayloadNonce:    payloadNonce,
		Ciphertext:      ciphertext,
	}, nil
}

func (c *Cipher) Open(ctx context.Context, credentialID, providerID uuid.UUID, scope catalogmodel.Scope, sealed catalogmodel.SealedCredential) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	aad, err := credentialAAD(credentialID, providerID, scope)
	if err != nil {
		return nil, err
	}
	if err := sealed.Validate(); err != nil {
		return nil, fmt.Errorf("invalid sealed credential: %w", err)
	}
	key, ok := c.keys[sealed.KeyVersion]
	if !ok {
		return nil, fmt.Errorf("unknown credential key version %q", sealed.KeyVersion)
	}
	wrappingAEAD, err := newGCM(key[:])
	if err != nil {
		return nil, err
	}
	if len(sealed.WrappedKeyNonce) != wrappingAEAD.NonceSize() {
		return nil, fmt.Errorf("invalid wrapped-key nonce length")
	}
	dataKey, err := wrappingAEAD.Open(nil, sealed.WrappedKeyNonce, sealed.WrappedDataKey, appendAAD(aad, "dek"))
	if err != nil {
		return nil, fmt.Errorf("unwrap credential data key: authentication failed")
	}
	defer clear(dataKey)
	if len(dataKey) != dataKeyBytes {
		return nil, fmt.Errorf("invalid unwrapped credential data key length")
	}
	payloadAEAD, err := newGCM(dataKey)
	if err != nil {
		return nil, err
	}
	if len(sealed.PayloadNonce) != payloadAEAD.NonceSize() {
		return nil, fmt.Errorf("invalid payload nonce length")
	}
	plaintext, err := payloadAEAD.Open(nil, sealed.PayloadNonce, sealed.Ciphertext, appendAAD(aad, "payload"))
	if err != nil {
		return nil, fmt.Errorf("open credential payload: authentication failed")
	}
	if !json.Valid(plaintext) {
		clear(plaintext)
		return nil, fmt.Errorf("decrypted credential payload is not valid JSON")
	}
	return plaintext, nil
}

func credentialAAD(credentialID, providerID uuid.UUID, scope catalogmodel.Scope) ([]byte, error) {
	if credentialID == uuid.Nil || providerID == uuid.Nil {
		return nil, fmt.Errorf("credential and provider ids are required")
	}
	if err := scope.Validate(); err != nil {
		return nil, fmt.Errorf("credential scope: %w", err)
	}
	target := ""
	switch scope.Kind {
	case catalogmodel.ScopeOrganization:
		target = scope.OrganizationID.String()
	case catalogmodel.ScopeProject:
		target = scope.ProjectID.String()
	}
	var aad strings.Builder
	aad.WriteString(aadPrefix)
	aad.WriteByte(0)
	aad.WriteString(credentialID.String())
	aad.WriteByte(0)
	aad.WriteString(providerID.String())
	aad.WriteByte(0)
	aad.WriteString(string(scope.Kind))
	aad.WriteByte(0)
	aad.WriteString(target)
	return []byte(aad.String()), nil
}

func appendAAD(base []byte, purpose string) []byte {
	aad := make([]byte, 0, len(base)+1+len(purpose))
	aad = append(aad, base...)
	aad = append(aad, 0)
	aad = append(aad, purpose...)
	return aad
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize AES-256: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize AES-GCM: %w", err)
	}
	return gcm, nil
}

func randomNonce(random io.Reader, size int) ([]byte, error) {
	nonce := make([]byte, size)
	if _, err := io.ReadFull(random, nonce); err != nil {
		return nil, err
	}
	return nonce, nil
}

var _ controlport.CredentialCipher = (*Cipher)(nil)
