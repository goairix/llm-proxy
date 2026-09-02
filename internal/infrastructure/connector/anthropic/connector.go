package anthropic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
	inferenceport "github.com/goairix/llm-proxy/internal/domain/inference/port"
)

type Options struct {
	Client            *http.Client
	CredentialOpener  CredentialOpener
	CompleteTimeout   time.Duration
	StreamIdleTimeout time.Duration
	IDGenerator       func() (uuid.UUID, error)
	Clock             func() time.Time
}

type Connector struct {
	options Options
}

func New(options Options) (*Connector, error) {
	if options.Client == nil || options.CredentialOpener == nil {
		return nil, fmt.Errorf("Anthropic connector client and credential opener are required")
	}
	if options.CompleteTimeout <= 0 || options.StreamIdleTimeout <= 0 {
		return nil, fmt.Errorf("Anthropic connector timeouts must be positive")
	}
	if options.IDGenerator == nil {
		options.IDGenerator = uuid.NewV7
	}
	if options.Clock == nil {
		options.Clock = func() time.Time { return time.Now().UTC() }
	}
	options.Client = cloneClientWithoutRedirects(options.Client)
	return &Connector{options: options}, nil
}

func (c *Connector) Complete(ctx context.Context, invocation gatewayport.Invocation) (inference.Response, error) {
	if err := c.validateInvocation(ctx, invocation, false); err != nil {
		return inference.Response{}, err
	}
	body, err := encodeRequest(invocation)
	if err != nil {
		return inference.Response{}, err
	}
	callContext, cancel := context.WithTimeout(ctx, c.options.CompleteTimeout)
	defer cancel()
	responseBody, err := c.do(callContext, invocation, body, false)
	if err != nil {
		return inference.Response{}, err
	}
	payload, err := readLimited(responseBody, maxSuccessBodyBytes)
	if err != nil {
		if !errors.Is(err, errBodyTooLarge) {
			err = errors.New("read Anthropic response body")
		}
		return inference.Response{}, invalidResponseError(err)
	}
	return decodeResponse(bytes.NewReader(payload), invocation, c.options.IDGenerator, c.options.Clock)
}

func (c *Connector) Stream(ctx context.Context, invocation gatewayport.Invocation) (inferenceport.Stream, error) {
	if err := c.validateInvocation(ctx, invocation, true); err != nil {
		return nil, err
	}
	body, err := encodeRequest(invocation)
	if err != nil {
		return nil, err
	}
	streamContext, cancel := context.WithCancel(ctx)
	responseBody, err := c.do(streamContext, invocation, body, true)
	if err != nil {
		cancel()
		return nil, err
	}
	reader := newSSEReader(responseBody, c.options.StreamIdleTimeout, maxSSEEventBytes)
	delegate, err := newMessagesStream(reader, invocation, c.options.IDGenerator, c.options.Clock)
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
		return fmt.Errorf("Anthropic connector is nil")
	}
	if err := invocation.Request.Validate(); err != nil {
		return fmt.Errorf("validate Anthropic invocation: %w", err)
	}
	if invocation.Request.Stream != stream {
		return fmt.Errorf("Anthropic invocation stream mode does not match connector method")
	}
	if invocation.Provider.ID == uuid.Nil || invocation.Provider.ConnectorType != catalogmodel.ConnectorAnthropic ||
		strings.TrimSpace(invocation.Provider.BaseURL) == "" {
		return fmt.Errorf("provider does not match Anthropic connector")
	}
	if invocation.Deployment.ID == uuid.Nil || invocation.Deployment.ProviderID != invocation.Provider.ID ||
		strings.TrimSpace(invocation.Deployment.UpstreamModel) == "" || invocation.Deployment.UpstreamProtocol != catalogmodel.UpstreamAnthropicMessages {
		return fmt.Errorf("deployment does not belong to Anthropic provider")
	}
	if invocation.Credential == nil || invocation.Credential.CredentialID == uuid.Nil ||
		invocation.Credential.ProviderID != invocation.Provider.ID {
		return connectorError(gatewayport.CredentialUnavailable, fmt.Errorf("provider credential is missing or mismatched"))
	}
	if err := invocation.Credential.Scope.Validate(); err != nil {
		return connectorError(gatewayport.CredentialUnavailable, fmt.Errorf("provider credential scope is invalid"))
	}
	return nil
}

func (c *Connector) do(
	ctx context.Context,
	invocation gatewayport.Invocation,
	body []byte,
	stream bool,
) (io.ReadCloser, error) {
	key, err := openAPIKey(ctx, c.options.CredentialOpener, *invocation.Credential)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	request, err := newUpstreamRequest(ctx, invocation.Provider, body, key, stream)
	if err != nil {
		var connectorErr *gatewayport.ConnectorError
		if errors.As(err, &connectorErr) {
			return nil, err
		}
		return nil, classifyRequestError(err)
	}
	response, err := c.options.Client.Do(request)
	if err != nil {
		return nil, classifyRequestError(err)
	}
	if response == nil || response.Body == nil {
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

var _ gatewayport.Connector = (*Connector)(nil)
var _ inferenceport.Stream = (*cancelingStream)(nil)
