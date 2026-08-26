package provider

import (
	"fmt"
	"net"
	"net/http"
	"time"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	gatewayservice "github.com/goairix/llm-proxy/internal/application/gateway/service"
	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	"github.com/goairix/llm-proxy/internal/infrastructure/config"
	fakeconnector "github.com/goairix/llm-proxy/internal/infrastructure/connector/fake"
	openconnector "github.com/goairix/llm-proxy/internal/infrastructure/connector/openai"
	"github.com/goairix/llm-proxy/internal/infrastructure/observability"
	credentialsecurity "github.com/goairix/llm-proxy/internal/infrastructure/security/credential"
	gatewayhandler "github.com/goairix/llm-proxy/internal/interfaces/http/handler/gateway"
)

type UnifiedGatewayHandlers struct {
	OpenAIChat      http.Handler
	OpenAIResponses http.Handler
	Anthropic       http.Handler
	registry        staticConnectorRegistry
	cipher          *credentialsecurity.Cipher
	transport       *http.Transport
}

func NewUnifiedGatewayHandlers(cfg *config.Config, runtime *GatewayRuntime, cipherRuntime *CredentialCipherRuntime, telemetry *observability.Runtime) (*UnifiedGatewayHandlers, error) {
	handlers := &UnifiedGatewayHandlers{}
	if cfg == nil || !cfg.Gateway.Enabled || runtime == nil || runtime.Store() == nil {
		return handlers, nil
	}
	if cipherRuntime == nil || cipherRuntime.Cipher == nil {
		return nil, fmt.Errorf("credential cipher runtime is unavailable")
	}
	upstream := normalizedUpstreamConfig(cfg.Gateway.Upstream)
	base := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		DialContext:       (&net.Dialer{Timeout: upstream.ConnectTimeout, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2: true, TLSHandshakeTimeout: upstream.TLSHandshakeTimeout,
		ResponseHeaderTimeout: upstream.ResponseHeaderTimeout, IdleConnTimeout: upstream.IdleConnectionTimeout,
		MaxIdleConns: upstream.MaxIdleConnections, MaxIdleConnsPerHost: upstream.MaxIdleConnectionsPerHost,
	}
	newConnector := func(connectorType string) (*openconnector.Connector, error) {
		return openconnector.New(openconnector.Options{
			ConnectorType:    connectorType,
			ResponsesClient:  &http.Client{Transport: telemetry.TransportFor(connectorType, "responses", base)},
			ChatClient:       &http.Client{Transport: telemetry.TransportFor(connectorType, "chat.completions", base)},
			CredentialOpener: cipherRuntime.Cipher, CompleteTimeout: upstream.CompleteTimeout, StreamIdleTimeout: upstream.StreamIdleTimeout,
		})
	}
	official, err := newConnector(catalogmodel.ConnectorOpenAI)
	if err != nil {
		return nil, err
	}
	compatible, err := newConnector(catalogmodel.ConnectorOpenAICompatible)
	if err != nil {
		return nil, err
	}
	registry := staticConnectorRegistry{
		catalogmodel.ConnectorFake:   fakeconnector.New(fakeconnector.Options{}),
		catalogmodel.ConnectorOpenAI: official, catalogmodel.ConnectorOpenAICompatible: compatible,
	}
	gateway := gatewayservice.New(runtime.Store(), registry, gatewaysnapshot.NewCredentialSelector())
	handlers.OpenAIChat = gatewayhandler.NewOpenAI(gateway)
	handlers.OpenAIResponses = gatewayhandler.NewResponses(gateway)
	handlers.Anthropic = gatewayhandler.NewAnthropic(gateway)
	handlers.registry, handlers.cipher, handlers.transport = registry, cipherRuntime.Cipher, base
	return handlers, nil
}

func normalizedUpstreamConfig(value config.UpstreamTransportConfig) config.UpstreamTransportConfig {
	if value.ConnectTimeout <= 0 {
		value.ConnectTimeout = 10 * time.Second
	}
	if value.TLSHandshakeTimeout <= 0 {
		value.TLSHandshakeTimeout = 10 * time.Second
	}
	if value.ResponseHeaderTimeout <= 0 {
		value.ResponseHeaderTimeout = 30 * time.Second
	}
	if value.CompleteTimeout <= 0 {
		value.CompleteTimeout = 5 * time.Minute
	}
	if value.StreamIdleTimeout <= 0 {
		value.StreamIdleTimeout = 5 * time.Minute
	}
	if value.IdleConnectionTimeout <= 0 {
		value.IdleConnectionTimeout = 90 * time.Second
	}
	if value.MaxIdleConnections <= 0 {
		value.MaxIdleConnections = 100
	}
	if value.MaxIdleConnectionsPerHost <= 0 {
		value.MaxIdleConnectionsPerHost = 10
	}
	return value
}

type staticConnectorRegistry map[string]gatewayport.Connector

func (r staticConnectorRegistry) Find(connectorType string) (gatewayport.Connector, bool) {
	connector, ok := r[connectorType]
	return connector, ok
}
