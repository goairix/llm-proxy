package provider

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/goairix/llm-proxy/internal/infrastructure/config"
)

func TestGatewayRuntimeDisabledIsNoOpWithEmptyStore(t *testing.T) {
	runtime := NewGatewayRuntime(&config.Config{}, zap.NewNop())
	if runtime.Database != nil || runtime.Manager != nil || runtime.Store() == nil {
		t.Fatalf("disabled gateway runtime = %+v", runtime)
	}
	runtime.Start(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runtime.CloseDatabase(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayRuntimeEnabledSharesDatabaseManagerAndSnapshotStore(t *testing.T) {
	cfg := gatewayProviderConfig()
	runtime := NewGatewayRuntime(cfg, zap.NewNop())
	if runtime.Database == nil || runtime.Manager == nil || runtime.Store() == nil {
		t.Fatalf("enabled gateway runtime = %+v", runtime)
	}
}

func gatewayProviderConfig() *config.Config {
	return &config.Config{
		Gateway: config.GatewayConfig{
			Enabled: true, SnapshotInterval: time.Hour, SnapshotTimeout: time.Second, RetryBackoff: time.Second,
			Upstream: config.UpstreamTransportConfig{ConnectTimeout: time.Second, TLSHandshakeTimeout: time.Second, ResponseHeaderTimeout: time.Second, CompleteTimeout: time.Second, StreamIdleTimeout: time.Second, IdleConnectionTimeout: time.Second, MaxIdleConnections: 10, MaxIdleConnectionsPerHost: 2},
		},
		Database:             config.DatabaseConfig{Driver: "postgres", DSN: "postgres://invalid/offline"},
		CredentialEncryption: config.CredentialEncryptionConfig{CurrentKeyVersion: "v1", Keys: map[string]string{"v1": base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))}},
	}
}
