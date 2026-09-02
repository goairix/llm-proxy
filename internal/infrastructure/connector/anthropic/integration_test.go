package anthropic

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

func TestConnectorCallsMessagesWithFixedHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/prefix/v1/messages" {
			t.Fatalf("method=%s path=%s", r.Method, r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "anthropic-secret" ||
			r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("Authorization") != "" {
			t.Fatalf("headers=%v", r.Header)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if !strings.Contains(string(body), `"model":"claude-upstream"`) || strings.Contains(string(body), `"model":"assistant"`) {
			t.Fatalf("body=%s", body)
		}
		payload, err := os.ReadFile("testdata/message_text_tool.json")
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()

	connector := newConnectorForClient(t, upstream.Client(), time.Second)
	invocation := fullInvocation(t)
	invocation.Provider.BaseURL = upstream.URL + "/prefix"
	response, err := connector.Complete(context.Background(), invocation)
	if err != nil || response.Model != invocation.Request.Model {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestConnectorStreamsMessages(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("Accept") != "text/event-stream" {
			t.Fatalf("path=%s headers=%v", r.URL.Path, r.Header)
		}
		payload, err := os.ReadFile("testdata/message_tool_stream.sse")
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()
	connector := newConnectorForClient(t, upstream.Client(), time.Second)
	invocation := fullInvocation(t)
	invocation.Provider.BaseURL = upstream.URL
	invocation.Request.Stream = true
	stream, err := connector.Stream(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, stream)
	if err := inference.ValidateEventSequence(events); err != nil {
		t.Fatal(err)
	}
}

func TestConnectorClassifiesHTTPStatusWithoutLeakingDetails(t *testing.T) {
	for _, test := range []struct {
		status int
		kind   gatewayport.ConnectorErrorKind
	}{
		{http.StatusBadRequest, gatewayport.UpstreamRequestRejected},
		{http.StatusUnauthorized, gatewayport.UpstreamAuthentication},
		{http.StatusForbidden, gatewayport.UpstreamAuthentication},
		{http.StatusRequestTimeout, gatewayport.UpstreamTimeout},
		{http.StatusTooManyRequests, gatewayport.UpstreamRateLimited},
		{http.StatusInternalServerError, gatewayport.UpstreamUnavailable},
		{http.StatusBadGateway, gatewayport.UpstreamUnavailable},
		{http.StatusServiceUnavailable, gatewayport.UpstreamUnavailable},
		{http.StatusGatewayTimeout, gatewayport.UpstreamTimeout},
		{529, gatewayport.UpstreamUnavailable},
		{http.StatusTemporaryRedirect, gatewayport.UpstreamUnavailable},
	} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, "private upstream detail")
			}))
			defer upstream.Close()
			connector := newConnectorForClient(t, upstream.Client(), time.Second)
			invocation := fullInvocation(t)
			invocation.Provider.BaseURL = upstream.URL
			_, err := connector.Complete(context.Background(), invocation)
			assertConnectorErrorKind(t, err, test.kind, "")
			visible := err.Error() + " " + causeString(err)
			for _, secret := range []string{
				"private upstream detail", "anthropic-secret", upstream.URL, invocation.Deployment.UpstreamModel,
				invocation.Provider.ID.String(), invocation.Deployment.ID.String(), invocation.Credential.CredentialID.String(),
			} {
				if strings.Contains(visible, secret) {
					t.Fatalf("error leaked %q: %s", secret, visible)
				}
			}
		})
	}
}

func TestConnectorDoesNotFollowRedirects(t *testing.T) {
	var targetCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	connector := newConnectorForClient(t, redirect.Client(), time.Second)
	invocation := fullInvocation(t)
	invocation.Provider.BaseURL = redirect.URL
	_, err := connector.Complete(context.Background(), invocation)
	assertConnectorErrorKind(t, err, gatewayport.UpstreamUnavailable, "")
	if targetCalls.Load() != 0 {
		t.Fatalf("redirect target calls=%d", targetCalls.Load())
	}
}

func TestConnectorClassifiesNetworkDeadlineAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "dns", err: &net.DNSError{Err: "no such host", Name: "private.invalid"}},
		{name: "tls", err: tls.RecordHeaderError{Msg: "private TLS detail"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, test.err })}
			connector := newConnectorForClient(t, client, time.Second)
			_, err := connector.Complete(context.Background(), fullInvocation(t))
			assertConnectorErrorKind(t, err, gatewayport.UpstreamUnavailable, "")
			if strings.Contains(causeString(err), "private") {
				t.Fatalf("error leaked network detail: %v", err)
			}
		})
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer upstream.Close()
	connector := newConnectorForClient(t, upstream.Client(), 20*time.Millisecond)
	invocation := fullInvocation(t)
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

func TestConnectorBoundsSuccessAndErrorBodies(t *testing.T) {
	oversized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", maxSuccessBodyBytes+1))
	}))
	defer oversized.Close()
	connector := newConnectorForClient(t, oversized.Client(), time.Second)
	invocation := fullInvocation(t)
	invocation.Provider.BaseURL = oversized.URL
	_, err := connector.Complete(context.Background(), invocation)
	assertConnectorErrorKind(t, err, gatewayport.UpstreamInvalidResponse, "")

	body := &countingBody{reader: strings.NewReader(strings.Repeat("private", maxErrorBodyBytes))}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: body, Request: request}, nil
	})}
	connector = newConnectorForClient(t, client, time.Second)
	_, err = connector.Complete(context.Background(), fullInvocation(t))
	assertConnectorErrorKind(t, err, gatewayport.UpstreamRequestRejected, "")
	if body.read > maxErrorBodyBytes+1 {
		t.Fatalf("error body bytes read=%d", body.read)
	}
}

func TestConnectorRejectsNilResponseBody(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: nil, Request: request}, nil
	})}
	connector := newConnectorForClient(t, client, time.Second)
	_, err := connector.Complete(context.Background(), fullInvocation(t))
	assertConnectorErrorKind(t, err, gatewayport.UpstreamInvalidResponse, "")
}

func newConnectorForClient(t *testing.T, client *http.Client, timeout time.Duration) *Connector {
	t.Helper()
	connector, err := New(Options{
		Client: client, CredentialOpener: allocatingOpener{},
		CompleteTimeout: timeout, StreamIdleTimeout: timeout,
		IDGenerator: fixedIDGenerator, Clock: fixedClock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return connector
}

type allocatingOpener struct{}

func (allocatingOpener) Open(context.Context, uuid.UUID, uuid.UUID, catalogmodel.Scope, catalogmodel.SealedCredential) ([]byte, error) {
	return []byte(`{"api_key":"anthropic-secret"}`), nil
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

type countingBody struct {
	reader *strings.Reader
	read   int
}

func (b *countingBody) Read(destination []byte) (int, error) {
	count, err := b.reader.Read(destination)
	b.read += count
	return count, err
}

func (*countingBody) Close() error { return nil }
