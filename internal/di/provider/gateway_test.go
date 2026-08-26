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
	cfg := &config.Config{}
	runtime := NewGatewayRuntime(cfg, zap.NewNop())
	cipher, err := NewCredentialCipherRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := NewUnifiedGatewayHandlers(cfg, runtime, cipher, nil)
	if err != nil {
		t.Fatal(err)
	}
	if handlers == nil || handlers.OpenAIChat != nil || handlers.OpenAIResponses != nil || handlers.Anthropic != nil {
		t.Fatalf("disabled handlers=%+v", handlers)
	}
}

func TestUnifiedGatewayHandlersUseSharedEmptySnapshotStore(t *testing.T) {
	cfg := gatewayProviderConfig()
	runtime := NewGatewayRuntime(cfg, zap.NewNop())
	cipher, err := NewCredentialCipherRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := NewUnifiedGatewayHandlers(cfg, runtime, cipher, nil)
	if err != nil {
		t.Fatal(err)
	}
	if handlers == nil || handlers.OpenAIChat == nil || handlers.OpenAIResponses == nil || handlers.Anthropic == nil || handlers.cipher != cipher.Cipher {
		t.Fatalf("enabled handlers=%+v", handlers)
	}
	for _, connectorType := range []string{"fake", "openai", "openai_compatible"} {
		if connector, ok := handlers.registry.Find(connectorType); !ok || connector == nil {
			t.Fatalf("connector %s is not registered", connectorType)
		}
	}

	for _, test := range []struct {
		name, path, body string
		handler          http.Handler
	}{
		{name: "openai", path: "/v1/chat/completions", body: `{"model":"assistant","messages":[{"role":"user","content":"hello"}]}`, handler: handlers.OpenAIChat},
		{name: "responses", path: "/v1/responses", body: `{"model":"assistant","input":"hello"}`, handler: handlers.OpenAIResponses},
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

func mustCipherRuntime(t *testing.T, cfg *config.Config) *CredentialCipherRuntime {
	t.Helper()
	runtime, err := NewCredentialCipherRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}
