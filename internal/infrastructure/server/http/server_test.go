package httpserver

import (
	"context"
	"net/http"
	"testing"

	appRuntime "github.com/goairix/llm-proxy/internal/application/runtime"
	"go.uber.org/zap"
)

func TestShutdownMarksServerNotReady(t *testing.T) {
	readiness := appRuntime.NewReadiness()
	readiness.SetReady(true)
	server := New(":0", http.NotFoundHandler(), zap.NewNop(), readiness)

	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if readiness.Ready() {
		t.Fatal("server remained ready after shutdown")
	}
}
