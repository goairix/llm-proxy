package provider

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/goairix/llm-proxy/internal/infrastructure/config"
)

func TestUnifiedGatewayHandlersAreDisabledWithoutGatewayRuntime(t *testing.T) {
	runtime := NewGatewayRuntime(&config.Config{}, zap.NewNop())
	handlers := NewUnifiedGatewayHandlers(&config.Config{}, runtime)
	if handlers == nil || handlers.OpenAI != nil || handlers.Anthropic != nil {
		t.Fatalf("disabled handlers=%+v", handlers)
	}
}

func TestUnifiedGatewayHandlersUseSharedEmptySnapshotStore(t *testing.T) {
	cfg := gatewayProviderConfig()
	runtime := NewGatewayRuntime(cfg, zap.NewNop())
	handlers := NewUnifiedGatewayHandlers(cfg, runtime)
	if handlers == nil || handlers.OpenAI == nil || handlers.Anthropic == nil {
		t.Fatalf("enabled handlers=%+v", handlers)
	}

	for _, test := range []struct {
		name, path, body string
		handler          http.Handler
	}{
		{name: "openai", path: "/v1/chat/completions", body: `{"model":"assistant","messages":[{"role":"user","content":"hello"}]}`, handler: handlers.OpenAI},
		{name: "anthropic", path: "/v1/messages", body: `{"model":"assistant","max_tokens":1,"messages":[{"role":"user","content":"hello"}]}`, handler: handlers.Anthropic},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			test.handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}
