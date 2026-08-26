package credential

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/google/uuid"

	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
)

func TestCipherRoundTripAndEnvelopeContainsNoPlaintext(t *testing.T) {
	cipher := newTestCipher(t, "v2")
	credentialID := uuid.Must(uuid.NewV7())
	providerID := uuid.Must(uuid.NewV7())
	plaintext := []byte(`{ "api_key": "provider-secret" }`)

	sealed, err := cipher.Seal(context.Background(), credentialID, providerID, catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if sealed.KeyVersion != "v2" {
		t.Fatalf("KeyVersion = %q; want v2", sealed.KeyVersion)
	}
	for name, value := range map[string][]byte{
		"wrapped nonce": sealed.WrappedKeyNonce,
		"wrapped key":   sealed.WrappedDataKey,
		"payload nonce": sealed.PayloadNonce,
		"ciphertext":    sealed.Ciphertext,
	} {
		if bytes.Contains(value, []byte("provider-secret")) {
			t.Fatalf("%s contains plaintext", name)
		}
	}
	opened, err := cipher.Open(context.Background(), credentialID, providerID, catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if string(opened) != `{"api_key":"provider-secret"}` {
		t.Fatalf("opened = %s", opened)
	}
}

func TestCipherRejectsTamperingAndAADChanges(t *testing.T) {
	cipher := newTestCipher(t, "v2")
	credentialID := uuid.Must(uuid.NewV7())
	providerID := uuid.Must(uuid.NewV7())
	scope := catalogmodel.Scope{Kind: catalogmodel.ScopeProject, ProjectID: uuid.Must(uuid.NewV7())}
	sealed, err := cipher.Seal(context.Background(), credentialID, providerID, scope, []byte(`{"api_key":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}

	tampered := sealed
	tampered.Ciphertext = append([]byte(nil), sealed.Ciphertext...)
	tampered.Ciphertext[0] ^= 1
	if _, err := cipher.Open(context.Background(), credentialID, providerID, scope, tampered); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}
	if _, err := cipher.Open(context.Background(), uuid.Must(uuid.NewV7()), providerID, scope, sealed); err == nil {
		t.Fatal("ciphertext moved to another credential was accepted")
	}
	if _, err := cipher.Open(context.Background(), credentialID, uuid.Must(uuid.NewV7()), scope, sealed); err == nil {
		t.Fatal("ciphertext moved to another provider was accepted")
	}
	otherScope := catalogmodel.Scope{Kind: catalogmodel.ScopeProject, ProjectID: uuid.Must(uuid.NewV7())}
	if _, err := cipher.Open(context.Background(), credentialID, providerID, otherScope, sealed); err == nil {
		t.Fatal("ciphertext moved to another scope was accepted")
	}
}

func TestCipherSupportsKeyRotationAndRejectsUnknownVersion(t *testing.T) {
	credentialID := uuid.Must(uuid.NewV7())
	providerID := uuid.Must(uuid.NewV7())
	scope := catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}
	oldCipher := newTestCipher(t, "v1")
	oldSealed, err := oldCipher.Seal(context.Background(), credentialID, providerID, scope, []byte(`{"api_key":"old"}`))
	if err != nil {
		t.Fatal(err)
	}
	rotated := newTestCipher(t, "v2")
	if _, err := rotated.Open(context.Background(), credentialID, providerID, scope, oldSealed); err != nil {
		t.Fatalf("open old key version: %v", err)
	}
	newSealed, err := rotated.Seal(context.Background(), credentialID, providerID, scope, []byte(`{"api_key":"new"}`))
	if err != nil || newSealed.KeyVersion != "v2" {
		t.Fatalf("new sealed = %+v, %v", newSealed, err)
	}
	newSealed.KeyVersion = "unknown"
	if _, err := rotated.Open(context.Background(), credentialID, providerID, scope, newSealed); err == nil {
		t.Fatal("unknown key version was accepted")
	}
}

func TestCipherRejectsInvalidJSON(t *testing.T) {
	cipher := newTestCipher(t, "v2")
	if _, err := cipher.Seal(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, []byte(`not-json`)); err == nil {
		t.Fatal("invalid JSON was accepted")
	}
}

func TestCipherRejectsInvalidNonceWithoutPanicking(t *testing.T) {
	cipher := newTestCipher(t, "v2")
	credentialID := uuid.Must(uuid.NewV7())
	providerID := uuid.Must(uuid.NewV7())
	scope := catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}
	sealed, err := cipher.Seal(context.Background(), credentialID, providerID, scope, []byte(`{"api_key":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	sealed.PayloadNonce = []byte{1}
	if _, err := cipher.Open(context.Background(), credentialID, providerID, scope, sealed); err == nil {
		t.Fatal("invalid nonce was accepted")
	}
}

func TestCipherPropagatesRandomSourceFailure(t *testing.T) {
	keys := testKeys()
	cipher, err := newCipher("v2", keys, cipherFailingReader{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cipher.Seal(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, []byte(`{"api_key":"secret"}`)); err == nil {
		t.Fatal("random source failure was ignored")
	}
}

func newTestCipher(t *testing.T, current string) *Cipher {
	t.Helper()
	cipher, err := NewCipher(current, testKeys())
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

func testKeys() map[string]string {
	return map[string]string{
		"v1": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)),
		"v2": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)),
	}
}

type cipherFailingReader struct{}

func (cipherFailingReader) Read([]byte) (int, error) { return 0, errors.New("random offline") }
