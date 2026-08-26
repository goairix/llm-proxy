package provider

import (
	"net/http"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	gatewayservice "github.com/goairix/llm-proxy/internal/application/gateway/service"
	"github.com/goairix/llm-proxy/internal/infrastructure/config"
	fakeconnector "github.com/goairix/llm-proxy/internal/infrastructure/connector/fake"
	gatewayhandler "github.com/goairix/llm-proxy/internal/interfaces/http/handler/gateway"
)

// UnifiedGatewayHandlers contains the optional provider-neutral data-plane adapters.
type UnifiedGatewayHandlers struct {
	OpenAI    http.Handler
	Anthropic http.Handler
}

// NewUnifiedGatewayHandlers assembles the provider-neutral Gateway over the shared snapshot store.
func NewUnifiedGatewayHandlers(cfg *config.Config, runtime *GatewayRuntime) *UnifiedGatewayHandlers {
	handlers := &UnifiedGatewayHandlers{}
	if cfg == nil || !cfg.Gateway.Enabled || runtime == nil || runtime.Store() == nil {
		return handlers
	}
	registry := staticConnectorRegistry{
		"fake": fakeconnector.New(fakeconnector.Options{}),
	}
	gateway := gatewayservice.New(runtime.Store(), registry)
	handlers.OpenAI = gatewayhandler.NewOpenAI(gateway)
	handlers.Anthropic = gatewayhandler.NewAnthropic(gateway)
	return handlers
}

type staticConnectorRegistry map[string]gatewayport.Connector

func (r staticConnectorRegistry) Find(connectorType string) (gatewayport.Connector, bool) {
	connector, ok := r[connectorType]
	return connector, ok
}
