package provider

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appRuntime "github.com/goairix/llm-proxy/internal/application/runtime"
	"github.com/goairix/llm-proxy/internal/infrastructure/config"
	"github.com/goairix/llm-proxy/internal/infrastructure/observability"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/database"
	"github.com/goairix/llm-proxy/internal/infrastructure/proxy/tokenusage"
	"github.com/goairix/llm-proxy/internal/interfaces/http/handler/dashboard"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func TestControlPlaneRuntimeDisabledDoesNotCreateDatabase(t *testing.T) {
	runtime, err := NewControlPlaneRuntime(&config.Config{}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Handler != nil || runtime.Database != nil || runtime.Authorizer != nil {
		t.Fatalf("disabled runtime = %+v", runtime)
	}
}

func TestOfflineControlPlaneDoesNotBreakTransparentProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cfg := &config.Config{
		Gateway: config.GatewayConfig{Enabled: true},
		Database: config.DatabaseConfig{
			Driver: "postgres", DSN: "postgres://invalid:invalid@127.0.0.1:1/offline?sslmode=disable",
		},
		ControlPlane:         config.ControlPlaneConfig{Token: "management-token-with-enough-entropy"},
		CredentialEncryption: config.CredentialEncryptionConfig{CurrentKeyVersion: "v1", Keys: map[string]string{"v1": key}},
		Providers: config.ProvidersConfig{
			OpenAI: config.ProviderConfig{BaseURL: upstream.URL}, Anthropic: config.ProviderConfig{BaseURL: upstream.URL},
		},
	}
	logger := zap.NewNop()
	controlPlane, err := NewControlPlaneRuntime(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	if controlPlane.Handler == nil || controlPlane.Database == nil || controlPlane.Authorizer == nil {
		t.Fatalf("enabled runtime = %+v", controlPlane)
	}

	telemetry, err := observability.New(context.Background(), config.ObservabilityConfig{}, appRuntime.Version, logger)
	if err != nil {
		t.Fatal(err)
	}
	openAI, err := NewOpenAIHandler(cfg, telemetry)
	if err != nil {
		t.Fatal(err)
	}
	anthropic, err := NewAnthropicHandler(cfg, telemetry)
	if err != nil {
		t.Fatal(err)
	}
	root := NewRootHandler(
		cfg, logger, telemetry, appRuntime.NewReadiness(), &dashboard.Stats{}, tokenusage.NewObserver,
		openAI, anthropic, controlPlane,
	)

	controlRequest := httptest.NewRequest(http.MethodPost, "/v1/organizations", strings.NewReader(`{"name":"Acme"}`))
	controlRequest.Header.Set("Authorization", "Bearer management-token-with-enough-entropy")
	controlResponse := httptest.NewRecorder()
	root.ServeHTTP(controlResponse, controlRequest)
	if controlResponse.Code != http.StatusServiceUnavailable || !strings.Contains(controlResponse.Body.String(), "dependency_unavailable") {
		t.Fatalf("control response = %d %s", controlResponse.Code, controlResponse.Body.String())
	}

	proxyRequest := httptest.NewRequest(http.MethodPost, "/openai/v1/models", nil)
	proxyRequest.Header.Set("Authorization", "Bearer transparent-proxy-key")
	proxyResponse := httptest.NewRecorder()
	root.ServeHTTP(proxyResponse, proxyRequest)
	if proxyResponse.Code != http.StatusOK || proxyResponse.Body.String() != `{"ok":true}` {
		t.Fatalf("proxy response = %d %s", proxyResponse.Code, proxyResponse.Body.String())
	}
}

func TestControlPlaneStopCancelsAndJoinsConnectionLoop(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	databaseRuntime := database.NewRuntime(func(ctx context.Context) (*gorm.DB, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	}, time.Second, zap.NewNop())
	runtime := &ControlPlaneRuntime{Database: databaseRuntime}

	runtime.Start(context.Background())
	<-started
	runtime.Stop()

	select {
	case <-stopped:
	default:
		t.Fatal("Stop returned before the connection loop exited")
	}
}
