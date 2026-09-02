package anthropic

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
)

var (
	fixedResponseID = uuid.Must(uuid.NewV7())
	fixedNow        = time.Date(2026, 9, 2, 10, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
)

type recordingOpener struct {
	plaintext    []byte
	err          error
	credentialID uuid.UUID
	providerID   uuid.UUID
	scope        catalogmodel.Scope
}

func (o *recordingOpener) Open(
	_ context.Context,
	credentialID, providerID uuid.UUID,
	scope catalogmodel.Scope,
	_ catalogmodel.SealedCredential,
) ([]byte, error) {
	o.credentialID, o.providerID, o.scope = credentialID, providerID, scope
	return o.plaintext, o.err
}

func credentialEnvelope(t *testing.T) gatewaysnapshot.CredentialEnvelope {
	t.Helper()
	return gatewaysnapshot.CredentialEnvelope{
		CredentialID: uuid.Must(uuid.NewV7()),
		ProviderID:   uuid.Must(uuid.NewV7()),
		Scope:        catalogmodel.Scope{Kind: catalogmodel.ScopePlatform},
		Sealed: catalogmodel.SealedCredential{
			KeyVersion: "v1", WrappedKeyNonce: []byte{1}, WrappedDataKey: []byte{2},
			PayloadNonce: []byte{3}, Ciphertext: []byte{4},
		},
	}
}

func fullInvocation(t *testing.T) gatewayport.Invocation {
	t.Helper()
	providerID := uuid.Must(uuid.NewV7())
	credential := credentialEnvelope(t)
	credential.ProviderID = providerID
	return gatewayport.Invocation{
		Request: inference.Request{
			Model: "assistant",
			Messages: []inference.Message{{
				Role:    inference.RoleUser,
				Content: []inference.ContentBlock{textBlock("hello")},
			}},
			MaxTokens: inference.Some[int64](128),
		},
		Provider: gatewaysnapshot.Provider{
			ID: providerID, ConnectorType: catalogmodel.ConnectorAnthropic,
			BaseURL: "https://api.anthropic.example",
		},
		Deployment: gatewaysnapshot.Deployment{
			ID: uuid.Must(uuid.NewV7()), ProviderID: providerID,
			UpstreamModel: "claude-upstream", UpstreamProtocol: catalogmodel.UpstreamAnthropicMessages,
			Capabilities: catalogmodel.CapabilitySet{Text: true, ImageInput: true, Tools: true, StructuredOutput: true, Streaming: true},
		},
		Credential: &credential,
		Revision:   1,
	}
}

func textBlock(value string) inference.ContentBlock {
	return inference.ContentBlock{Type: inference.ContentText, Text: &inference.TextContent{Text: value}}
}

func fixedIDGenerator() (uuid.UUID, error) { return fixedResponseID, nil }
func fixedClock() time.Time                { return fixedNow }

func assertConnectorErrorKind(t *testing.T, err error, kind gatewayport.ConnectorErrorKind, param string) {
	t.Helper()
	var connectorErr *gatewayport.ConnectorError
	if !errors.As(err, &connectorErr) || connectorErr.Kind != kind || connectorErr.Param != param {
		t.Fatalf("error=%v kind=%q param=%q", err, kind, param)
	}
}

func collectEvents(t *testing.T, stream interface {
	Recv(context.Context) (inference.Event, error)
	Close() error
}) []inference.Event {
	t.Helper()
	defer stream.Close()
	var events []inference.Event
	for {
		event, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			return events
		}
		if err != nil {
			t.Fatalf("receive event: %v", err)
		}
		events = append(events, event)
	}
}

func lastUsage(t *testing.T, events []inference.Event) inference.Usage {
	t.Helper()
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type == inference.EventUsageUpdate {
			return events[index].UsageUpdate.Usage
		}
	}
	t.Fatal("usage update not found")
	return inference.Usage{}
}
