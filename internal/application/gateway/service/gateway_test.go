package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
	inferenceport "github.com/goairix/llm-proxy/internal/domain/inference/port"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
	tenancymodel "github.com/goairix/llm-proxy/internal/domain/tenancy/model"
)

func TestCompleteUsesOneSnapshotSessionAndResolvedDeployment(t *testing.T) {
	fixture := newGatewayFixture(t, catalogmodel.CapabilitySet{Text: true, Streaming: true})

	response, err := fixture.gateway.Complete(context.Background(), fixture.virtualKey, validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if fixture.store.beginCalls != 1 || fixture.connector.completeCalls != 1 || fixture.connector.streamCalls != 0 {
		t.Fatalf("begin=%d complete=%d stream=%d", fixture.store.beginCalls, fixture.connector.completeCalls, fixture.connector.streamCalls)
	}
	if response.Model != validRequest().Model || fixture.connector.invocation.Access.ProjectID != fixture.projectID {
		t.Fatalf("response=%+v invocation=%+v", response, fixture.connector.invocation)
	}
	if fixture.connector.invocation.Deployment.ID != fixture.deploymentID || fixture.connector.invocation.Revision != 7 {
		t.Fatalf("invocation=%+v", fixture.connector.invocation)
	}
	if fixture.connector.invocation.Provider.ConnectorType != catalogmodel.ConnectorFake || fixture.connector.invocation.Credential != nil {
		t.Fatalf("fake invocation=%+v", fixture.connector.invocation)
	}
}

func TestGatewayPreparesProviderAndScopedCredentialWithoutVirtualKey(t *testing.T) {
	fixture := newGatewayFixture(t, catalogmodel.CapabilitySet{Text: true})
	fixture.configureOpenAIProvider(t, catalogmodel.Scope{Kind: catalogmodel.ScopePlatform})

	_, err := fixture.gateway.Complete(context.Background(), fixture.virtualKey, validRequest())
	if err != nil {
		t.Fatal(err)
	}
	invocation := fixture.connector.invocation
	if invocation.Provider.BaseURL != "https://api.openai.com" || invocation.Provider.ConnectorType != catalogmodel.ConnectorOpenAI || invocation.Credential == nil {
		t.Fatalf("invocation=%+v", invocation)
	}
	encoded, err := json.Marshal(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(fixture.virtualKey)) {
		t.Fatal("Virtual Key leaked into Connector Invocation")
	}
	if fixture.store.beginCalls != 1 {
		t.Fatalf("snapshot Begin calls=%d", fixture.store.beginCalls)
	}
}

func TestGatewayMapsParameterUnsupportedAcrossStreamLifecycle(t *testing.T) {
	request := validRequest()
	request.Stream = true

	fixture := newGatewayFixture(t, catalogmodel.CapabilitySet{Text: true, Streaming: true})
	fixture.connector.streamErr = &gatewayport.ConnectorError{Kind: gatewayport.ParameterUnsupported, Param: "stop"}
	_, err := fixture.gateway.Stream(context.Background(), fixture.virtualKey, request)
	gatewayError := assertGatewayErrorCode(t, err, CapabilityUnsupported)
	if gatewayError.Param != "stop" {
		t.Fatalf("stream creation param=%q", gatewayError.Param)
	}

	fixture = newGatewayFixture(t, catalogmodel.CapabilitySet{Text: true, Streaming: true})
	fixture.connector.stream = &sliceStream{recvErr: &gatewayport.ConnectorError{Kind: gatewayport.ParameterUnsupported, Param: "temperature"}}
	stream, err := fixture.gateway.Stream(context.Background(), fixture.virtualKey, request)
	if err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv(context.Background())
	gatewayError = assertGatewayErrorCode(t, err, CapabilityUnsupported)
	if gatewayError.Param != "temperature" {
		t.Fatalf("stream Recv param=%q", gatewayError.Param)
	}
}

func TestGatewayRejectsMissingProviderCredentialBeforeConnector(t *testing.T) {
	fixture := newGatewayFixture(t, catalogmodel.CapabilitySet{Text: true})
	fixture.configureOpenAIProvider(t, catalogmodel.Scope{Kind: catalogmodel.ScopePlatform})
	fixture.source.Credentials = nil
	fixture.publish(t)

	_, err := fixture.gateway.Complete(context.Background(), fixture.virtualKey, validRequest())
	gatewayError := assertGatewayErrorCode(t, err, ConnectorFailed)
	if gatewayError.SafeMessage != "供应商凭据不可用" || fixture.connector.completeCalls != 0 {
		t.Fatalf("error=%+v calls=%d", gatewayError, fixture.connector.completeCalls)
	}
}

func TestGatewayMapsConnectorErrorKinds(t *testing.T) {
	tests := []struct {
		kind  gatewayport.ConnectorErrorKind
		code  ErrorCode
		param string
	}{
		{kind: gatewayport.ParameterUnsupported, code: CapabilityUnsupported, param: "stop"},
		{kind: gatewayport.UpstreamAuthentication, code: ConnectorFailed},
		{kind: gatewayport.UpstreamRateLimited, code: ConnectorFailed},
		{kind: gatewayport.UpstreamTimeout, code: ConnectorFailed},
		{kind: gatewayport.UpstreamUnavailable, code: ConnectorFailed},
		{kind: gatewayport.UpstreamRequestRejected, code: ConnectorFailed},
		{kind: gatewayport.UpstreamInvalidResponse, code: ConnectorFailed},
		{kind: gatewayport.CredentialUnavailable, code: ConnectorFailed},
	}
	for _, test := range tests {
		t.Run(string(test.kind), func(t *testing.T) {
			fixture := newGatewayFixture(t, catalogmodel.CapabilitySet{Text: true})
			fixture.connector.completeErr = &gatewayport.ConnectorError{Kind: test.kind, Param: test.param, Cause: context.DeadlineExceeded}
			_, err := fixture.gateway.Complete(context.Background(), fixture.virtualKey, validRequest())
			gatewayError := assertGatewayErrorCode(t, err, test.code)
			if gatewayError.Param != test.param {
				t.Fatalf("param=%q want=%q", gatewayError.Param, test.param)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("connector cause was not preserved")
			}
		})
	}
}

func TestGatewayValidatesBeforeOpeningSnapshot(t *testing.T) {
	fixture := newGatewayFixture(t, catalogmodel.CapabilitySet{Text: true})
	request := validRequest()
	request.Model = ""

	_, err := fixture.gateway.Complete(context.Background(), fixture.virtualKey, request)
	assertGatewayErrorCode(t, err, InvalidRequest)
	if fixture.store.beginCalls != 0 || fixture.connector.completeCalls != 0 {
		t.Fatalf("begin=%d complete=%d", fixture.store.beginCalls, fixture.connector.completeCalls)
	}
}

func TestGatewayMapsSnapshotAuthenticationAndRouteErrors(t *testing.T) {
	emptyStore := &countingStore{store: gatewaysnapshot.NewStore()}
	gateway := New(emptyStore, registry{}, gatewaysnapshot.NewCredentialSelector())
	_, err := gateway.Complete(context.Background(), "missing", validRequest())
	assertGatewayErrorCode(t, err, GatewayNotReady)

	fixture := newGatewayFixture(t, catalogmodel.CapabilitySet{Text: true})
	_, err = fixture.gateway.Complete(context.Background(), "wrong-key", validRequest())
	assertGatewayErrorCode(t, err, AuthenticationFailed)

	request := validRequest()
	request.Model = "unknown"
	_, err = fixture.gateway.Complete(context.Background(), fixture.virtualKey, request)
	assertGatewayErrorCode(t, err, ResourceNotFound)
}

func TestGatewayRejectsUnsupportedCapabilitiesBeforeConnector(t *testing.T) {
	tests := []struct {
		name         string
		capabilities catalogmodel.CapabilitySet
		request      func() inference.Request
		stream       bool
		param        string
	}{
		{name: "text", capabilities: catalogmodel.CapabilitySet{ImageInput: true}, request: validRequest, param: "messages"},
		{name: "image", capabilities: catalogmodel.CapabilitySet{Text: true}, request: func() inference.Request {
			request := validRequest()
			request.Messages[0].Content = []inference.ContentBlock{{Type: inference.ContentImage, Image: &inference.ImageContent{
				Source: inference.ImageSource{Type: inference.ImageURL, Data: "https://example.invalid/a.png"},
			}}}
			return request
		}, param: "messages.image"},
		{name: "tools", capabilities: catalogmodel.CapabilitySet{Text: true}, request: func() inference.Request {
			request := validRequest()
			request.Tools = []inference.Tool{{Name: "weather", InputSchema: json.RawMessage(`{"type":"object"}`)}}
			return request
		}, param: "tools"},
		{name: "structured output", capabilities: catalogmodel.CapabilitySet{Text: true}, request: func() inference.Request {
			request := validRequest()
			request.StructuredOutput = &inference.StructuredOutput{Type: inference.StructuredJSONObject}
			return request
		}, param: "structured_output"},
		{name: "streaming", capabilities: catalogmodel.CapabilitySet{Text: true}, request: func() inference.Request {
			request := validRequest()
			request.Stream = true
			return request
		}, stream: true, param: "stream"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayFixture(t, test.capabilities)
			var err error
			if test.stream {
				_, err = fixture.gateway.Stream(context.Background(), fixture.virtualKey, test.request())
			} else {
				_, err = fixture.gateway.Complete(context.Background(), fixture.virtualKey, test.request())
			}
			gatewayError := assertGatewayErrorCode(t, err, CapabilityUnsupported)
			if gatewayError.Param != test.param {
				t.Fatalf("param=%q want=%q", gatewayError.Param, test.param)
			}
			if fixture.connector.completeCalls != 0 || fixture.connector.streamCalls != 0 {
				t.Fatal("connector was called before capability rejection")
			}
		})
	}
}

func TestGatewayRejectsMissingConnectorAndPreservesConnectorCause(t *testing.T) {
	fixture := newGatewayFixture(t, catalogmodel.CapabilitySet{Text: true})
	fixture.gateway = New(fixture.store, registry{}, gatewaysnapshot.NewCredentialSelector())
	_, err := fixture.gateway.Complete(context.Background(), fixture.virtualKey, validRequest())
	assertGatewayErrorCode(t, err, InternalError)

	fixture = newGatewayFixture(t, catalogmodel.CapabilitySet{Text: true})
	fixture.connector.completeErr = context.DeadlineExceeded
	_, err = fixture.gateway.Complete(context.Background(), fixture.virtualKey, validRequest())
	assertGatewayErrorCode(t, err, ConnectorFailed)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("connector cause was not preserved")
	}

	fixture = newGatewayFixture(t, catalogmodel.CapabilitySet{Text: true})
	fixture.connector.response.Model = "wrong-alias"
	_, err = fixture.gateway.Complete(context.Background(), fixture.virtualKey, validRequest())
	assertGatewayErrorCode(t, err, ConnectorFailed)
}

func TestGatewayRejectsNilStreamAndPreservesRecvCancellation(t *testing.T) {
	fixture := newGatewayFixture(t, catalogmodel.CapabilitySet{Text: true, Streaming: true})
	request := validRequest()
	request.Stream = true
	_, err := fixture.gateway.Stream(context.Background(), fixture.virtualKey, request)
	assertGatewayErrorCode(t, err, ConnectorFailed)

	fixture = newGatewayFixture(t, catalogmodel.CapabilitySet{Text: true, Streaming: true})
	fixture.connector.streamErr = context.DeadlineExceeded
	_, err = fixture.gateway.Stream(context.Background(), fixture.virtualKey, request)
	assertGatewayErrorCode(t, err, ConnectorFailed)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("stream creation cause was not preserved")
	}

	fixture = newGatewayFixture(t, catalogmodel.CapabilitySet{Text: true, Streaming: true})
	underlying := &sliceStream{recvErr: context.Canceled}
	fixture.connector.stream = underlying
	stream, err := fixture.gateway.Stream(context.Background(), fixture.virtualKey, request)
	if err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv(context.Background())
	assertGatewayErrorCode(t, err, ConnectorFailed)
	if !errors.Is(err, context.Canceled) || underlying.closeCalls != 1 {
		t.Fatalf("recv error=%v close calls=%d", err, underlying.closeCalls)
	}
}

func TestGatewayStreamValidatesEventsAndCloseIsIdempotent(t *testing.T) {
	fixture := newGatewayFixture(t, catalogmodel.CapabilitySet{Text: true, Streaming: true})
	request := validRequest()
	request.Stream = true
	fixture.connector.stream = &sliceStream{events: []inference.Event{
		inference.NewResponseStart(uuid.Must(uuid.NewV7()), request.Model),
		inference.NewContentBlockStart(0, inference.ContentText),
		inference.NewTextDelta(0, "hello"),
		inference.NewContentBlockStop(0),
		inference.NewUsageUpdate(inference.Usage{InputTokens: 1, OutputTokens: 1}),
		inference.NewResponseFinish(inference.StopEndTurn),
	}}

	stream, err := fixture.gateway.Stream(context.Background(), fixture.virtualKey, request)
	if err != nil {
		t.Fatal(err)
	}
	for range fixture.connector.stream.(*sliceStream).events {
		if _, err := stream.Recv(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := stream.Recv(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("final Recv() error=%v", err)
	}
	var wait sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs <- stream.Close()
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if fixture.connector.stream.(*sliceStream).closeCalls != 1 || fixture.connector.completeCalls != 0 || fixture.connector.streamCalls != 1 {
		t.Fatalf("close=%d complete=%d stream=%d", fixture.connector.stream.(*sliceStream).closeCalls, fixture.connector.completeCalls, fixture.connector.streamCalls)
	}
}

func TestGatewayStreamClosesInvalidOrPrematureStream(t *testing.T) {
	tests := []struct {
		name   string
		events []inference.Event
	}{
		{name: "response model mismatch", events: []inference.Event{
			inference.NewResponseStart(uuid.Must(uuid.NewV7()), "different-alias"),
		}},
		{name: "malformed response start", events: []inference.Event{{
			Type: inference.EventResponseStart, TextDelta: &inference.TextDeltaEvent{Index: 0, Text: "wrong payload"},
		}}},
		{name: "invalid event", events: []inference.Event{
			inference.NewResponseStart(uuid.Must(uuid.NewV7()), "assistant"), inference.NewTextDelta(0, "missing block"),
		}},
		{name: "premature eof", events: []inference.Event{
			inference.NewResponseStart(uuid.Must(uuid.NewV7()), "assistant"),
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayFixture(t, catalogmodel.CapabilitySet{Text: true, Streaming: true})
			request := validRequest()
			request.Stream = true
			underlying := &sliceStream{events: test.events}
			fixture.connector.stream = underlying
			stream, err := fixture.gateway.Stream(context.Background(), fixture.virtualKey, request)
			if err != nil {
				t.Fatal(err)
			}
			for {
				_, err = stream.Recv(context.Background())
				if err != nil {
					break
				}
			}
			assertGatewayErrorCode(t, err, ConnectorFailed)
			if underlying.closeCalls != 1 {
				t.Fatalf("close calls=%d", underlying.closeCalls)
			}
		})
	}
}

func validRequest() inference.Request {
	return inference.Request{
		Model: "assistant",
		Messages: []inference.Message{{Role: inference.RoleUser, Content: []inference.ContentBlock{
			{Type: inference.ContentText, Text: &inference.TextContent{Text: "hello"}},
		}}},
	}
}

type gatewayFixture struct {
	gateway      Gateway
	store        *countingStore
	connector    *recordingConnector
	virtualKey   string
	projectID    uuid.UUID
	deploymentID uuid.UUID
	source       gatewaysnapshot.SourceConfig
}

func newGatewayFixture(t *testing.T, capabilities catalogmodel.CapabilitySet) *gatewayFixture {
	t.Helper()
	const virtualKey = "llmp_v1_gateway-test-secret"
	organization, _ := tenancymodel.NewOrganization("Acme")
	project, _ := tenancymodel.NewProject(organization.ID, "Production")
	key, _ := tenancymodel.NewVirtualKey(project.ID, "ci", sha256.Sum256([]byte(virtualKey)), "llmp_v1_gateway", "cret", nil)
	provider, _ := catalogmodel.NewProvider("Fake", catalogmodel.ConnectorFake, "")
	deployment, err := catalogmodel.NewDeployment(
		provider.ID, "fake", "fake-model", catalogmodel.UpstreamFake, catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, capabilities,
	)
	if err != nil {
		t.Fatal(err)
	}
	alias, _ := catalogmodel.NewModelAlias(project.ID, "assistant")
	alias.Status = sharedmodel.StatusActive
	target, _ := catalogmodel.NewRouteTarget(alias.ID, deployment.ID, 0, 100)
	source := gatewaysnapshot.SourceConfig{
		Revision: 7, Organizations: []tenancymodel.Organization{*organization}, Projects: []tenancymodel.Project{*project},
		VirtualKeys: []tenancymodel.VirtualKey{*key}, Providers: []catalogmodel.Provider{*provider},
		Deployments: []catalogmodel.Deployment{*deployment}, ModelAliases: []catalogmodel.ModelAlias{*alias},
		RouteTargets: []catalogmodel.RouteTarget{*target},
	}
	compiled, err := gatewaysnapshot.NewCompiler().Compile(source, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	store := gatewaysnapshot.NewStore()
	store.Publish(compiled)
	counted := &countingStore{store: store}
	connector := &recordingConnector{response: inference.Response{
		ID: uuid.Must(uuid.NewV7()), Model: "assistant",
		Content:    []inference.ContentBlock{{Type: inference.ContentText, Text: &inference.TextContent{Text: "hello"}}},
		StopReason: inference.StopEndTurn, CreatedAt: time.Now().UTC(),
	}}
	return &gatewayFixture{
		gateway: New(counted, registry{"fake": connector}, gatewaysnapshot.NewCredentialSelector()), store: counted, connector: connector,
		virtualKey: virtualKey, projectID: project.ID, deploymentID: deployment.ID, source: source,
	}
}

func (f *gatewayFixture) configureOpenAIProvider(t *testing.T, scope catalogmodel.Scope) {
	t.Helper()
	provider, err := catalogmodel.NewProvider("OpenAI", catalogmodel.ConnectorOpenAI, "https://api.openai.com")
	if err != nil {
		t.Fatal(err)
	}
	credential, err := catalogmodel.NewProviderCredential(provider.ID, scope, testGatewaySealedCredential())
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := catalogmodel.NewDeployment(
		provider.ID, "GPT-5", "gpt-5", catalogmodel.UpstreamResponses, scope, catalogmodel.CapabilitySet{Text: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	f.source.Providers = []catalogmodel.Provider{*provider}
	f.source.Credentials = []catalogmodel.ProviderCredential{*credential}
	f.source.Deployments = []catalogmodel.Deployment{*deployment}
	f.source.RouteTargets[0].DeploymentID = deployment.ID
	f.deploymentID = deployment.ID
	f.publish(t)
	f.gateway = New(f.store, registry{catalogmodel.ConnectorOpenAI: f.connector}, gatewaysnapshot.NewCredentialSelector())
}

func (f *gatewayFixture) publish(t *testing.T) {
	t.Helper()
	f.source.Revision++
	compiled, err := gatewaysnapshot.NewCompiler().Compile(f.source, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	f.store.store.Publish(compiled)
}

func testGatewaySealedCredential() catalogmodel.SealedCredential {
	return catalogmodel.SealedCredential{
		KeyVersion: "v1", WrappedKeyNonce: []byte{1}, WrappedDataKey: []byte{2}, PayloadNonce: []byte{3}, Ciphertext: []byte{4},
	}
}

type countingStore struct {
	store      *gatewaysnapshot.Store
	beginCalls int
}

func (s *countingStore) Begin() (gatewaysnapshot.Session, error) {
	s.beginCalls++
	return s.store.Begin()
}

type registry map[string]gatewayport.Connector

func (r registry) Find(connectorType string) (gatewayport.Connector, bool) {
	connector, ok := r[connectorType]
	return connector, ok
}

type recordingConnector struct {
	response      inference.Response
	completeErr   error
	stream        inferenceport.Stream
	streamErr     error
	invocation    gatewayport.Invocation
	completeCalls int
	streamCalls   int
}

func (c *recordingConnector) Complete(_ context.Context, invocation gatewayport.Invocation) (inference.Response, error) {
	c.completeCalls++
	c.invocation = invocation
	return c.response, c.completeErr
}

func (c *recordingConnector) Stream(_ context.Context, invocation gatewayport.Invocation) (inferenceport.Stream, error) {
	c.streamCalls++
	c.invocation = invocation
	return c.stream, c.streamErr
}

type sliceStream struct {
	events     []inference.Event
	index      int
	closeCalls int
	recvErr    error
}

func (s *sliceStream) Recv(context.Context) (inference.Event, error) {
	if s.index >= len(s.events) {
		if s.recvErr != nil {
			return inference.Event{}, s.recvErr
		}
		return inference.Event{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *sliceStream) Close() error {
	s.closeCalls++
	return nil
}

func assertGatewayErrorCode(t *testing.T, err error, code ErrorCode) *GatewayError {
	t.Helper()
	var gatewayError *GatewayError
	if !errors.As(err, &gatewayError) || gatewayError.Code != code {
		t.Fatalf("error=%v; want code %s", err, code)
	}
	return gatewayError
}
