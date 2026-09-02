package anthropic

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
)

func TestNewConnectorValidatesOptionsAndDisablesRedirects(t *testing.T) {
	sourceClient := &http.Client{}
	options := Options{
		Client: sourceClient, CredentialOpener: allocatingOpener{},
		CompleteTimeout: time.Minute, StreamIdleTimeout: time.Minute,
	}
	connector, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	if connector.options.Client.CheckRedirect == nil || sourceClient.CheckRedirect != nil ||
		connector.options.IDGenerator == nil || connector.options.Clock == nil {
		t.Fatalf("connector options=%+v source=%+v", connector.options, sourceClient)
	}
	for _, mutate := range []func(*Options){
		func(value *Options) { value.Client = nil },
		func(value *Options) { value.CredentialOpener = nil },
		func(value *Options) { value.CompleteTimeout = 0 },
		func(value *Options) { value.StreamIdleTimeout = 0 },
	} {
		invalid := options
		mutate(&invalid)
		if _, err := New(invalid); err == nil {
			t.Fatalf("invalid options accepted: %+v", invalid)
		}
	}
}

func TestNewUpstreamRequestUsesMessagesPathAndFixedHeaders(t *testing.T) {
	provider := gatewaysnapshot.Provider{BaseURL: "https://example.invalid/prefix"}
	request, err := newUpstreamRequest(context.Background(), provider, []byte(`{}`), []byte("anthropic-secret"), true)
	if err != nil {
		t.Fatal(err)
	}
	if request.Method != http.MethodPost || request.URL.String() != "https://example.invalid/prefix/v1/messages" || request.Host != "example.invalid" {
		t.Fatalf("request=%+v", request)
	}
	if request.Header.Get("Content-Type") != "application/json" || request.Header.Get("Accept") != "text/event-stream" ||
		request.Header.Get("x-api-key") != "anthropic-secret" || request.Header.Get("anthropic-version") != "2023-06-01" ||
		request.Header.Get("Authorization") != "" {
		t.Fatalf("headers=%v", request.Header)
	}
}

func TestReadLimitedRejectsOversizedAndNilBodies(t *testing.T) {
	if _, err := readLimited(nil, 4); err == nil {
		t.Fatal("nil body was accepted")
	}
	_, err := readLimited(io.NopCloser(strings.NewReader("12345")), 4)
	if !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("error=%v", err)
	}
	payload, err := readLimited(io.NopCloser(strings.NewReader("1234")), 4)
	if err != nil || string(payload) != "1234" {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
}

func TestConnectorRejectsInvalidInvocationBeforeOpeningCredential(t *testing.T) {
	opener := &recordingOpener{plaintext: []byte(`{"api_key":"anthropic-secret"}`)}
	connector, err := New(Options{
		Client: &http.Client{}, CredentialOpener: opener,
		CompleteTimeout: time.Second, StreamIdleTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*gatewayport.Invocation){
		func(value *gatewayport.Invocation) { value.Provider.ConnectorType = catalogmodel.ConnectorOpenAI },
		func(value *gatewayport.Invocation) { value.Deployment.ProviderID = uuid.Must(uuid.NewV7()) },
		func(value *gatewayport.Invocation) {
			value.Deployment.UpstreamProtocol = catalogmodel.UpstreamResponses
		},
		func(value *gatewayport.Invocation) { value.Credential = nil },
		func(value *gatewayport.Invocation) { value.Request.Stream = true },
	} {
		invocation := fullInvocation(t)
		mutate(&invocation)
		if _, err := connector.Complete(context.Background(), invocation); err == nil {
			t.Fatal("invalid invocation was accepted")
		}
	}
	if opener.credentialID != uuid.Nil {
		t.Fatalf("credential was opened: %+v", opener)
	}
}
