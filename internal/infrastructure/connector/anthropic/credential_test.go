package anthropic

import (
	"context"
	"errors"
	"testing"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
)

func TestOpenAPIKeyAcceptsOnlyOneNonEmptyField(t *testing.T) {
	envelope := credentialEnvelope(t)
	for _, test := range []struct {
		name, payload string
		want          string
		wantError     bool
	}{
		{name: "valid", payload: `{"api_key":"anthropic-secret"}`, want: "anthropic-secret"},
		{name: "empty object", payload: `{}`, wantError: true},
		{name: "empty", payload: `{"api_key":""}`, wantError: true},
		{name: "whitespace", payload: `{"api_key":"  "}`, wantError: true},
		{name: "unknown", payload: `{"api_key":"x","header":"y"}`, wantError: true},
		{name: "trailing", payload: `{"api_key":"x"}{}`, wantError: true},
		{name: "array", payload: `[]`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			opener := &recordingOpener{plaintext: []byte(test.payload)}
			key, err := openAPIKey(context.Background(), opener, envelope)
			if test.wantError {
				assertConnectorErrorKind(t, err, gatewayport.CredentialUnavailable, "")
			} else if err != nil || string(key) != test.want {
				t.Fatalf("key=%q err=%v", key, err)
			}
			if opener.credentialID != envelope.CredentialID || opener.providerID != envelope.ProviderID ||
				opener.scope != envelope.Scope {
				t.Fatalf("opener=%+v", opener)
			}
			for index, value := range opener.plaintext {
				if value != 0 {
					t.Fatalf("plaintext[%d]=%d was not cleared", index, value)
				}
			}
			clear(key)
		})
	}
}

func TestOpenAPIKeyClassifiesOpenerFailure(t *testing.T) {
	opener := &recordingOpener{err: errors.New("kms private detail")}
	_, err := openAPIKey(context.Background(), opener, credentialEnvelope(t))
	assertConnectorErrorKind(t, err, gatewayport.CredentialUnavailable, "")
}
