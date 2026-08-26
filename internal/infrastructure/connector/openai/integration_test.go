package openai

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
)

type protocolUpstream struct {
	*httptest.Server
	requests atomic.Int64
}

func (s *protocolUpstream) RequestCount() int64 { return s.requests.Load() }

func protocolServer(t *testing.T, protocol catalogmodel.UpstreamProtocol) *protocolUpstream {
	t.Helper()
	path, fixture := protocolFixture(protocol, false)
	result := &protocolUpstream{}
	result.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		result.requests.Add(1)
		if request.Method != http.MethodPost || request.URL.Path != path {
			http.NotFound(w, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer upstream-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		payload, err := os.ReadFile(fixture)
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(result.Close)
	return result
}

func connectorForServer(t *testing.T, connectorType string, upstream *protocolUpstream) *Connector {
	t.Helper()
	connector, err := New(Options{
		ConnectorType: connectorType, ResponsesClient: upstream.Client(), ChatClient: upstream.Client(),
		CredentialOpener: allocatingOpener{}, CompleteTimeout: time.Second, StreamIdleTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return connector
}

func invocationForProtocol(t *testing.T, protocol catalogmodel.UpstreamProtocol) gatewayport.Invocation {
	t.Helper()
	invocation := fullInvocation(t)
	invocation.Deployment.UpstreamProtocol = protocol
	credential := credentialEnvelope()
	credential.ProviderID = invocation.Provider.ID
	invocation.Credential = &credential
	return invocation
}

func TestConnectorSelectsExplicitDeploymentProtocol(t *testing.T) {
	for _, connectorType := range []string{catalogmodel.ConnectorOpenAI, catalogmodel.ConnectorOpenAICompatible} {
		for _, protocol := range []catalogmodel.UpstreamProtocol{catalogmodel.UpstreamResponses, catalogmodel.UpstreamChatCompletions} {
			t.Run(connectorType+"/"+string(protocol), func(t *testing.T) {
				upstream := protocolServer(t, protocol)
				connector := connectorForServer(t, connectorType, upstream)
				invocation := invocationForProtocol(t, protocol)
				invocation.Provider.ConnectorType = connectorType
				invocation.Provider.BaseURL = upstream.URL
				response, err := connector.Complete(context.Background(), invocation)
				if err != nil {
					t.Fatal(err)
				}
				if response.Model != invocation.Request.Model || upstream.RequestCount() != 1 {
					t.Fatalf("response=%+v count=%d", response, upstream.RequestCount())
				}
			})
		}
	}
}

func TestConnectorStreamsSelectedProtocol(t *testing.T) {
	for _, protocol := range []catalogmodel.UpstreamProtocol{catalogmodel.UpstreamResponses, catalogmodel.UpstreamChatCompletions} {
		t.Run(string(protocol), func(t *testing.T) {
			path, fixture := protocolFixture(protocol, true)
			var count atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				count.Add(1)
				if request.URL.Path != path {
					http.NotFound(w, request)
					return
				}
				payload, err := os.ReadFile(fixture)
				if err != nil {
					t.Error(err)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write(payload)
			}))
			t.Cleanup(upstream.Close)
			connector := newConnectorForClient(t, catalogmodel.ConnectorOpenAICompatible, upstream.Client(), time.Second)
			invocation := invocationForProtocol(t, protocol)
			invocation.Provider.ConnectorType = catalogmodel.ConnectorOpenAICompatible
			invocation.Provider.BaseURL = upstream.URL
			invocation.Request.Stream = true
			stream, err := connector.Stream(context.Background(), invocation)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			events := collectEvents(t, stream)
			if err := inference.ValidateEventSequence(events); err != nil {
				t.Fatal(err)
			}
			if count.Load() != 1 {
				t.Fatalf("count=%d", count.Load())
			}
		})
	}
}

func TestConnectorClassifiesHTTPStatusWithoutLeakingBody(t *testing.T) {
	tests := []struct {
		status int
		kind   gatewayport.ConnectorErrorKind
	}{
		{http.StatusBadRequest, gatewayport.UpstreamRequestRejected},
		{http.StatusUnauthorized, gatewayport.UpstreamAuthentication},
		{http.StatusForbidden, gatewayport.UpstreamAuthentication},
		{http.StatusNotFound, gatewayport.UpstreamRequestRejected},
		{http.StatusConflict, gatewayport.UpstreamRequestRejected},
		{http.StatusUnprocessableEntity, gatewayport.UpstreamRequestRejected},
		{http.StatusTooManyRequests, gatewayport.UpstreamRateLimited},
		{http.StatusInternalServerError, gatewayport.UpstreamUnavailable},
		{http.StatusTemporaryRedirect, gatewayport.UpstreamUnavailable},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, "private upstream detail")
			}))
			t.Cleanup(upstream.Close)
			connector := newConnectorForClient(t, catalogmodel.ConnectorOpenAI, upstream.Client(), time.Second)
			invocation := invocationForProtocol(t, catalogmodel.UpstreamChatCompletions)
			invocation.Provider.BaseURL = upstream.URL
			_, err := connector.Complete(context.Background(), invocation)
			assertConnectorErrorKind(t, err, test.kind, "")
			if strings.Contains(causeString(err), "private upstream detail") {
				t.Fatalf("error leaked body: %v", err)
			}
		})
	}
}

func TestConnectorClassifiesNetworkTLSDeadlineAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "dns", err: &net.DNSError{Err: "no such host", Name: "private.invalid"}},
		{name: "tls", err: tls.RecordHeaderError{Msg: "private TLS detail"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, test.err })}
			connector := newConnectorForClient(t, catalogmodel.ConnectorOpenAI, client, time.Second)
			_, err := connector.Complete(context.Background(), invocationForProtocol(t, catalogmodel.UpstreamChatCompletions))
			assertConnectorErrorKind(t, err, gatewayport.UpstreamUnavailable, "")
		})
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	t.Cleanup(upstream.Close)
	connector := newConnectorForClient(t, catalogmodel.ConnectorOpenAI, upstream.Client(), 20*time.Millisecond)
	invocation := invocationForProtocol(t, catalogmodel.UpstreamChatCompletions)
	invocation.Provider.BaseURL = upstream.URL
	_, err := connector.Complete(context.Background(), invocation)
	assertConnectorErrorKind(t, err, gatewayport.UpstreamTimeout, "")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = connector.Complete(canceled, invocation)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestConnectorRejectsOversizedJSONAndInvalidStreams(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", maxSuccessBodyBytes+1))
	}))
	t.Cleanup(upstream.Close)
	connector := newConnectorForClient(t, catalogmodel.ConnectorOpenAI, upstream.Client(), time.Second)
	invocation := invocationForProtocol(t, catalogmodel.UpstreamChatCompletions)
	invocation.Provider.BaseURL = upstream.URL
	_, err := connector.Complete(context.Background(), invocation)
	assertConnectorErrorKind(t, err, gatewayport.UpstreamInvalidResponse, "")

	for _, test := range []struct {
		name, body string
		protocol   catalogmodel.UpstreamProtocol
	}{
		{name: "unknown event", protocol: catalogmodel.UpstreamResponses, body: "event: response.mystery\ndata: {\"type\":\"response.mystery\",\"sequence_number\":0}\n\n"},
		{name: "chat disconnect", protocol: catalogmodel.UpstreamChatCompletions, body: "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"x\"},\"finish_reason\":null}]}\n\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, test.body) }))
			t.Cleanup(server.Close)
			connector := newConnectorForClient(t, catalogmodel.ConnectorOpenAI, server.Client(), time.Second)
			invocation := invocationForProtocol(t, test.protocol)
			invocation.Provider.BaseURL = server.URL
			invocation.Request.Stream = true
			stream, err := connector.Stream(context.Background(), invocation)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			for {
				_, err = stream.Recv(context.Background())
				if err != nil {
					break
				}
			}
			assertConnectorErrorKind(t, err, gatewayport.UpstreamInvalidResponse, "")
		})
	}
}

func TestConnectorRejectsOversizedSSEEvent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+strings.Repeat("x", maxSuccessBodyBytes)+"\n\n")
	}))
	t.Cleanup(upstream.Close)
	connector := newConnectorForClient(t, catalogmodel.ConnectorOpenAI, upstream.Client(), time.Second)
	invocation := invocationForProtocol(t, catalogmodel.UpstreamChatCompletions)
	invocation.Provider.BaseURL = upstream.URL
	invocation.Request.Stream = true
	stream, err := connector.Stream(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_, err = stream.Recv(context.Background())
	assertConnectorErrorKind(t, err, gatewayport.UpstreamInvalidResponse, "")
}

func protocolFixture(protocol catalogmodel.UpstreamProtocol, stream bool) (string, string) {
	if protocol == catalogmodel.UpstreamResponses {
		if stream {
			return "/v1/responses", "testdata/responses_tool_stream.sse"
		}
		return "/v1/responses", "testdata/responses_text_response.json"
	}
	if stream {
		return "/v1/chat/completions", "testdata/chat_tool_stream.sse"
	}
	return "/v1/chat/completions", "testdata/chat_text_response.json"
}

func newConnectorForClient(t *testing.T, connectorType string, client *http.Client, timeout time.Duration) *Connector {
	t.Helper()
	connector, err := New(Options{ConnectorType: connectorType, ResponsesClient: client, ChatClient: client, CredentialOpener: allocatingOpener{}, CompleteTimeout: timeout, StreamIdleTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return connector
}

type allocatingOpener struct{}

func (allocatingOpener) Open(context.Context, uuid.UUID, uuid.UUID, catalogmodel.Scope, catalogmodel.SealedCredential) ([]byte, error) {
	return []byte(`{"api_key":"upstream-secret"}`), nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func causeString(err error) string {
	var connectorErr *gatewayport.ConnectorError
	if !errors.As(err, &connectorErr) || connectorErr.Cause == nil {
		return ""
	}
	return connectorErr.Cause.Error()
}
