package openai

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
)

type recordingOpener struct {
	plaintext []byte
	err       error
}

func (o *recordingOpener) Open(context.Context, uuid.UUID, uuid.UUID, catalogmodel.Scope, catalogmodel.SealedCredential) ([]byte, error) {
	return o.plaintext, o.err
}

func TestOpenAPIKeyStrictlyParsesAndClearsPlaintext(t *testing.T) {
	opener := &recordingOpener{plaintext: []byte(`{"api_key":"upstream-secret"}`)}
	key, err := openAPIKey(context.Background(), opener, credentialEnvelope())
	if err != nil {
		t.Fatal(err)
	}
	if string(key) != "upstream-secret" {
		t.Fatalf("key=%q", key)
	}
	for index, value := range opener.plaintext {
		if value != 0 {
			t.Fatalf("plaintext[%d]=%d was not cleared", index, value)
		}
	}
	clear(key)
}

func TestOpenAPIKeyRejectsInvalidPayloadAndOpenFailure(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		err     error
	}{
		{name: "open failure", err: errors.New("kms private detail")},
		{name: "empty object", payload: `{}`},
		{name: "empty key", payload: `{"api_key":""}`},
		{name: "unknown field", payload: `{"api_key":"x","extra":true}`},
		{name: "multiple values", payload: `{"api_key":"x"}{}`},
		{name: "array", payload: `[]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opener := &recordingOpener{plaintext: []byte(test.payload), err: test.err}
			_, err := openAPIKey(context.Background(), opener, credentialEnvelope())
			var connectorErr *gatewayport.ConnectorError
			if !errors.As(err, &connectorErr) || connectorErr.Kind != gatewayport.CredentialUnavailable {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func credentialEnvelope() gatewaysnapshot.CredentialEnvelope {
	return gatewaysnapshot.CredentialEnvelope{
		CredentialID: uuid.Must(uuid.NewV7()), ProviderID: uuid.Must(uuid.NewV7()),
		Scope: catalogmodel.Scope{Kind: catalogmodel.ScopePlatform},
		Sealed: catalogmodel.SealedCredential{
			KeyVersion: "v1", WrappedKeyNonce: []byte{1}, WrappedDataKey: []byte{2}, PayloadNonce: []byte{3}, Ciphertext: []byte{4},
		},
	}
}
