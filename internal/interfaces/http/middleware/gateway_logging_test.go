package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestGatewayLoggingUsesFixedSafeMetadataAndMasksKey(t *testing.T) {
	core, observed := observer.New(zap.DebugLevel)
	logger := zap.New(core)
	handler := RequestID(GatewayLogging(logger, "openai", "chat.completions")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?model=secret-model", nil)
	request.Header.Set("Authorization", "Bearer vk-super-secret-1234")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if observed.Len() != 1 {
		t.Fatalf("entries=%d", observed.Len())
	}
	entry := observed.All()[0]
	fields := entry.ContextMap()
	if fields["protocol"] != "openai" || fields["endpoint"] != "chat.completions" || fields["virtual_key"] != "****1234" {
		t.Fatalf("fields=%+v", fields)
	}
	if _, exists := fields["client_ip"]; exists {
		t.Fatalf("gateway log must not contain client_ip: fields=%+v", fields)
	}
	encoded := entry.Message
	for key, value := range fields {
		encoded += key + valueString(value)
	}
	if strings.Contains(encoded, "vk-super-secret") || strings.Contains(encoded, "secret-model") || strings.Contains(encoded, "model=") {
		t.Fatalf("unsafe log=%s fields=%+v", entry.Message, fields)
	}
}

func TestGatewayLoggingPreservesUnderlyingFlusherCapability(t *testing.T) {
	flushing := GatewayLogging(zap.NewNop(), "anthropic", "messages")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Error("flusher was not preserved")
		}
	}))
	flushing.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))

	nonFlushing := GatewayLogging(zap.NewNop(), "openai", "chat.completions")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, ok := w.(http.Flusher); ok {
			t.Error("flusher was invented")
		}
	}))
	nonFlushing.ServeHTTP(&plainResponseWriter{header: make(http.Header)}, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
}

type plainResponseWriter struct{ header http.Header }

func (w *plainResponseWriter) Header() http.Header           { return w.header }
func (*plainResponseWriter) WriteHeader(int)                 {}
func (*plainResponseWriter) Write(value []byte) (int, error) { return len(value), nil }
