package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
	inferenceport "github.com/goairix/llm-proxy/internal/domain/inference/port"
)

type Gateway interface {
	Complete(context.Context, string, inference.Request) (inference.Response, error)
	Stream(context.Context, string, inference.Request) (inferenceport.Stream, error)
}

type snapshotStore interface {
	Begin() (gatewaysnapshot.Session, error)
}

type gateway struct {
	store    snapshotStore
	registry gatewayport.ConnectorRegistry
	selector *gatewaysnapshot.CredentialSelector
	clock    func() time.Time
}

func New(store snapshotStore, registry gatewayport.ConnectorRegistry, selector *gatewaysnapshot.CredentialSelector) Gateway {
	return &gateway{store: store, registry: registry, selector: selector, clock: func() time.Time { return time.Now().UTC() }}
}

func (g *gateway) Complete(ctx context.Context, virtualKey string, request inference.Request) (inference.Response, error) {
	if err := validateGatewayRequest(request, false); err != nil {
		return inference.Response{}, err
	}
	invocation, connector, err := g.prepare(virtualKey, request)
	if err != nil {
		return inference.Response{}, err
	}
	response, err := connector.Complete(ctx, invocation)
	if err != nil {
		return inference.Response{}, mapConnectorError(err, "供应商请求失败")
	}
	if err := response.Validate(); err != nil {
		return inference.Response{}, NewError(ConnectorFailed, "供应商返回了无效响应", "", err)
	}
	if response.Model != request.Model {
		return inference.Response{}, NewError(ConnectorFailed, "供应商返回了无效响应", "", fmt.Errorf("response model does not match request alias"))
	}
	return response, nil
}

func (g *gateway) Stream(ctx context.Context, virtualKey string, request inference.Request) (inferenceport.Stream, error) {
	if err := validateGatewayRequest(request, true); err != nil {
		return nil, err
	}
	invocation, connector, err := g.prepare(virtualKey, request)
	if err != nil {
		return nil, err
	}
	stream, err := connector.Stream(ctx, invocation)
	if err != nil {
		return nil, mapConnectorError(err, "供应商流式请求失败")
	}
	if stream == nil {
		return nil, NewError(ConnectorFailed, "供应商流式请求失败", "", fmt.Errorf("connector returned nil stream"))
	}
	return &validatedStream{delegate: stream, validator: inference.NewSequenceValidator(), expectedModel: request.Model}, nil
}

func (g *gateway) prepare(virtualKey string, request inference.Request) (gatewayport.Invocation, gatewayport.Connector, error) {
	if g == nil || g.store == nil {
		return gatewayport.Invocation{}, nil, NewError(GatewayNotReady, "网关尚未就绪", "", gatewaysnapshot.ErrSnapshotUnavailable)
	}
	session, err := g.store.Begin()
	if err != nil {
		return gatewayport.Invocation{}, nil, mapSnapshotError(err)
	}
	now := time.Now().UTC()
	if g.clock != nil {
		now = g.clock()
	}
	access, err := session.Authenticate(virtualKey, now)
	if err != nil {
		return gatewayport.Invocation{}, nil, mapSnapshotError(err)
	}
	plan, err := session.Resolve(access.ProjectID, request.Model)
	if err != nil {
		return gatewayport.Invocation{}, nil, mapSnapshotError(err)
	}
	if param := unsupportedCapability(request.RequiredCapabilities(), plan.Deployment.Capabilities); param != "" {
		return gatewayport.Invocation{}, nil, NewError(CapabilityUnsupported, "当前模型不支持请求所需能力", param, nil)
	}
	provider, err := session.Provider(plan.Deployment.ProviderID)
	if err != nil {
		return gatewayport.Invocation{}, nil, mapSnapshotError(err)
	}
	var credential *gatewaysnapshot.CredentialEnvelope
	if provider.ConnectorType != catalogmodel.ConnectorFake {
		if g.selector == nil {
			return gatewayport.Invocation{}, nil, NewError(InternalError, "网关连接器配置无效", "", fmt.Errorf("credential selector is nil"))
		}
		selected, selectErr := g.selector.Select(session, access, provider.ID)
		if selectErr != nil {
			if errors.Is(selectErr, gatewaysnapshot.ErrCredentialUnavailable) {
				return gatewayport.Invocation{}, nil, NewError(ConnectorFailed, "供应商凭据不可用", "", selectErr)
			}
			return gatewayport.Invocation{}, nil, mapSnapshotError(selectErr)
		}
		credential = &selected
	}
	if g.registry == nil {
		return gatewayport.Invocation{}, nil, NewError(InternalError, "网关连接器配置无效", "", fmt.Errorf("connector registry is nil"))
	}
	connector, ok := g.registry.Find(provider.ConnectorType)
	if !ok || connector == nil {
		return gatewayport.Invocation{}, nil, NewError(InternalError, "网关连接器配置无效", "", fmt.Errorf("connector type is not registered"))
	}
	return gatewayport.Invocation{
		Request: request, Access: access, Provider: provider, Deployment: plan.Deployment,
		Credential: credential, Revision: session.Revision(),
	}, connector, nil
}

func validateGatewayRequest(request inference.Request, stream bool) error {
	if err := request.Validate(); err != nil {
		return NewError(InvalidRequest, "请求参数无效", "", err)
	}
	if request.Stream != stream {
		if stream {
			return NewError(InvalidRequest, "流式调用必须设置 stream", "stream", nil)
		}
		return NewError(InvalidRequest, "非流式调用不能设置 stream", "stream", nil)
	}
	return nil
}

func mapSnapshotError(err error) error {
	switch {
	case errors.Is(err, gatewaysnapshot.ErrSnapshotUnavailable):
		return NewError(GatewayNotReady, "网关尚未就绪", "", err)
	case errors.Is(err, gatewaysnapshot.ErrInvalidVirtualKey):
		return NewError(AuthenticationFailed, "Virtual Key 无效或已过期", "", err)
	case errors.Is(err, gatewaysnapshot.ErrRouteNotFound):
		return NewError(ResourceNotFound, "模型别名不存在或未启用", "model", err)
	default:
		return NewError(InternalError, "内部服务错误", "", err)
	}
}

func mapConnectorError(err error, safeMessage string) error {
	var connectorError *gatewayport.ConnectorError
	if errors.As(err, &connectorError) && connectorError.Kind == gatewayport.ParameterUnsupported {
		return NewError(CapabilityUnsupported, "当前模型不支持请求参数", connectorError.Param, err)
	}
	return NewError(ConnectorFailed, safeMessage, "", err)
}

func unsupportedCapability(required inference.Requirements, available catalogmodel.CapabilitySet) string {
	switch {
	case required.Text && !available.Text:
		return "messages"
	case required.ImageInput && !available.ImageInput:
		return "messages.image"
	case required.Tools && !available.Tools:
		return "tools"
	case required.StructuredOutput && !available.StructuredOutput:
		return "structured_output"
	case required.Streaming && !available.Streaming:
		return "stream"
	default:
		return ""
	}
}

type validatedStream struct {
	delegate      inferenceport.Stream
	validator     *inference.SequenceValidator
	expectedModel string
	recvMu        sync.Mutex
	closeOnce     sync.Once
	closeErr      error
}

func (s *validatedStream) Recv(ctx context.Context) (inference.Event, error) {
	s.recvMu.Lock()
	defer s.recvMu.Unlock()
	event, err := s.delegate.Recv(ctx)
	if err != nil {
		if errors.Is(err, io.EOF) {
			if validationErr := s.validator.ValidateEOF(); validationErr != nil {
				return inference.Event{}, s.fail(validationErr)
			}
			return inference.Event{}, io.EOF
		}
		_ = s.Close()
		return inference.Event{}, mapConnectorError(err, "供应商流式响应失败")
	}
	if err := s.validator.Push(event); err != nil {
		return inference.Event{}, s.fail(err)
	}
	if event.Type == inference.EventResponseStart && event.ResponseStart.Model != s.expectedModel {
		return inference.Event{}, s.fail(fmt.Errorf("stream response model does not match request alias"))
	}
	return event, nil
}

func (s *validatedStream) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.delegate != nil {
			s.closeErr = s.delegate.Close()
		}
	})
	return s.closeErr
}

func (s *validatedStream) fail(cause error) error {
	_ = s.Close()
	return NewError(ConnectorFailed, "供应商返回了无效的流式响应", "", cause)
}

var _ Gateway = (*gateway)(nil)
var _ inferenceport.Stream = (*validatedStream)(nil)
