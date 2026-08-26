package openai

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
	inferenceport "github.com/goairix/llm-proxy/internal/domain/inference/port"
)

type CredentialOpener interface {
	Open(context.Context, uuid.UUID, uuid.UUID, catalogmodel.Scope, catalogmodel.SealedCredential) ([]byte, error)
}

type Options struct {
	ConnectorType     string
	ResponsesClient   *http.Client
	ChatClient        *http.Client
	CredentialOpener  CredentialOpener
	CompleteTimeout   time.Duration
	StreamIdleTimeout time.Duration
	IDGenerator       func() (uuid.UUID, error)
	Clock             func() time.Time
}

type Connector struct{ options Options }

func New(options Options) (*Connector, error) {
	if options.ConnectorType != catalogmodel.ConnectorOpenAI && options.ConnectorType != catalogmodel.ConnectorOpenAICompatible {
		return nil, fmt.Errorf("unsupported OpenAI connector type %q", options.ConnectorType)
	}
	if options.ResponsesClient == nil || options.ChatClient == nil || options.CredentialOpener == nil {
		return nil, fmt.Errorf("OpenAI connector clients and credential opener are required")
	}
	if options.CompleteTimeout <= 0 || options.StreamIdleTimeout <= 0 {
		return nil, fmt.Errorf("OpenAI connector timeouts must be positive")
	}
	if options.IDGenerator == nil {
		options.IDGenerator = uuid.NewV7
	}
	if options.Clock == nil {
		options.Clock = func() time.Time { return time.Now().UTC() }
	}
	options.ResponsesClient = cloneClientWithoutRedirects(options.ResponsesClient)
	options.ChatClient = cloneClientWithoutRedirects(options.ChatClient)
	return &Connector{options: options}, nil
}

func (c *Connector) Complete(ctx context.Context, invocation gatewayport.Invocation) (inference.Response, error) {
	if err := c.validateInvocation(ctx, invocation, false); err != nil {
		return inference.Response{}, err
	}
	body, endpoint, client, err := c.encode(invocation)
	if err != nil {
		return inference.Response{}, err
	}
	callContext, cancel := context.WithTimeout(ctx, c.options.CompleteTimeout)
	defer cancel()
	responseBody, err := c.do(callContext, client, invocation, body, endpoint, false)
	if err != nil {
		return inference.Response{}, err
	}
	payload, err := readLimited(responseBody, maxSuccessBodyBytes)
	if err != nil {
		return inference.Response{}, invalidResponseError(err)
	}
	switch invocation.Deployment.UpstreamProtocol {
	case catalogmodel.UpstreamResponses:
		return decodeResponsesResponse(bytesReader(payload), invocation, c.options.IDGenerator, c.options.Clock)
	case catalogmodel.UpstreamChatCompletions:
		return decodeChatResponse(bytesReader(payload), invocation, c.options.IDGenerator, c.options.Clock)
	default:
		return inference.Response{}, fmt.Errorf("unsupported OpenAI deployment protocol %q", invocation.Deployment.UpstreamProtocol)
	}
}

func (c *Connector) Stream(ctx context.Context, invocation gatewayport.Invocation) (inferenceport.Stream, error) {
	if err := c.validateInvocation(ctx, invocation, true); err != nil {
		return nil, err
	}
	body, endpoint, client, err := c.encode(invocation)
	if err != nil {
		return nil, err
	}
	streamContext, cancel := context.WithCancel(ctx)
	responseBody, err := c.do(streamContext, client, invocation, body, endpoint, true)
	if err != nil {
		cancel()
		return nil, err
	}
	reader := newSSEReader(responseBody, c.options.StreamIdleTimeout, maxSuccessBodyBytes)
	var delegate inferenceport.Stream
	switch invocation.Deployment.UpstreamProtocol {
	case catalogmodel.UpstreamResponses:
		delegate, err = newResponsesStream(reader, invocation, c.options.IDGenerator, c.options.Clock)
	case catalogmodel.UpstreamChatCompletions:
		delegate, err = newChatStream(reader, invocation, c.options.IDGenerator, c.options.Clock)
	default:
		err = fmt.Errorf("unsupported OpenAI deployment protocol %q", invocation.Deployment.UpstreamProtocol)
	}
	if err != nil {
		cancel()
		_ = reader.Close()
		return nil, err
	}
	return &cancelingStream{delegate: delegate, cancel: cancel}, nil
}

func (c *Connector) validateInvocation(ctx context.Context, invocation gatewayport.Invocation, stream bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil {
		return fmt.Errorf("OpenAI connector is nil")
	}
	if err := invocation.Request.Validate(); err != nil {
		return fmt.Errorf("validate OpenAI invocation: %w", err)
	}
	if invocation.Request.Stream != stream {
		return fmt.Errorf("OpenAI invocation stream mode does not match connector method")
	}
	if err := validateProvider(c.options.ConnectorType, invocation.Provider); err != nil {
		return err
	}
	if invocation.Deployment.ProviderID != invocation.Provider.ID || invocation.Deployment.UpstreamModel == "" {
		return fmt.Errorf("deployment does not belong to OpenAI provider")
	}
	switch invocation.Deployment.UpstreamProtocol {
	case catalogmodel.UpstreamResponses, catalogmodel.UpstreamChatCompletions:
	default:
		return fmt.Errorf("unsupported OpenAI deployment protocol %q", invocation.Deployment.UpstreamProtocol)
	}
	if invocation.Credential == nil || invocation.Credential.CredentialID == uuid.Nil || invocation.Credential.ProviderID != invocation.Provider.ID {
		return connectorError(gatewayport.CredentialUnavailable, fmt.Errorf("provider credential is missing or mismatched"))
	}
	return nil
}

func (c *Connector) encode(invocation gatewayport.Invocation) ([]byte, string, *http.Client, error) {
	switch invocation.Deployment.UpstreamProtocol {
	case catalogmodel.UpstreamResponses:
		body, err := encodeResponsesRequest(invocation)
		return body, "responses", c.options.ResponsesClient, err
	case catalogmodel.UpstreamChatCompletions:
		body, err := encodeChatRequest(invocation)
		return body, "chat/completions", c.options.ChatClient, err
	default:
		return nil, "", nil, fmt.Errorf("unsupported OpenAI deployment protocol %q", invocation.Deployment.UpstreamProtocol)
	}
}

func (c *Connector) do(ctx context.Context, client *http.Client, invocation gatewayport.Invocation, body []byte, endpoint string, stream bool) (io.ReadCloser, error) {
	key, err := openAPIKey(ctx, c.options.CredentialOpener, *invocation.Credential)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	request, err := newUpstreamRequest(ctx, invocation.Provider, endpoint, body, key, stream)
	if err != nil {
		return nil, classifyRequestError(err)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, classifyRequestError(err)
	}
	if response.Body == nil {
		return nil, invalidResponseError(fmt.Errorf("upstream response body is nil"))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = readLimited(response.Body, maxErrorBodyBytes)
		return nil, classifyHTTPStatus(response.StatusCode)
	}
	return response.Body, nil
}

type cancelingStream struct {
	delegate  inferenceport.Stream
	cancel    context.CancelFunc
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

func (s *cancelingStream) Recv(ctx context.Context) (inference.Event, error) {
	if s == nil || s.closed.Load() {
		return inference.Event{}, io.EOF
	}
	event, err := s.delegate.Recv(ctx)
	if err != nil && s.closed.Load() {
		return inference.Event{}, io.EOF
	}
	return event, err
}

func (s *cancelingStream) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		s.cancel()
		s.closeErr = s.delegate.Close()
	})
	return s.closeErr
}

func bytesReader(payload []byte) io.Reader { return bytes.NewReader(payload) }

var _ gatewayport.Connector = (*Connector)(nil)
var _ inferenceport.Stream = (*cancelingStream)(nil)

func validateProvider(connectorType string, provider gatewaysnapshot.Provider) error {
	if provider.ID == uuid.Nil || provider.ConnectorType != connectorType || provider.BaseURL == "" {
		return fmt.Errorf("provider does not match OpenAI connector")
	}
	return nil
}
