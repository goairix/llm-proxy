package controlplane_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/goairix/llm-proxy/internal/di/provider"
	"github.com/goairix/llm-proxy/internal/infrastructure/config"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/database"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/entity"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/migration"
	"github.com/goairix/llm-proxy/internal/interfaces/http/middleware"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func TestControlPlaneIntegrationWithPostgresStoresOnlyProtectedSecrets(t *testing.T) {
	dsn := os.Getenv("LLM_PROXY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("未设置 LLM_PROXY_TEST_POSTGRES_DSN，跳过控制面 PostgreSQL 纵向测试")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		t.Fatal("测试 DSN 必须使用 postgres:// URL 格式")
	}
	base := openPostgres(t, dsn)
	schema := "llm_proxy_control_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := base.Exec(fmt.Sprintf(`CREATE SCHEMA "%s"`, schema)).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_ = base.Exec(fmt.Sprintf(`DROP SCHEMA "%s" CASCADE`, schema)).Error
		if sqlDB, err := base.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	schemaDSN := parsed.String()
	migrationDB := openPostgres(t, schemaDSN)
	if err := migration.Up(migrationDB); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	if sqlDB, err := migrationDB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	keyring := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cfg := &config.Config{
		Gateway: config.GatewayConfig{Enabled: true, RetryBackoff: 10 * time.Millisecond},
		Database: config.DatabaseConfig{
			Driver: "postgres", DSN: schemaDSN, MaxIdleConnections: 1, MaxOpenConnections: 4,
			ConnectionLifetime: time.Minute, ConnectTimeout: 5 * time.Second,
		},
		ControlPlane:         config.ControlPlaneConfig{Token: "integration-management-token-with-entropy"},
		CredentialEncryption: config.CredentialEncryptionConfig{CurrentKeyVersion: "v1", Keys: map[string]string{"v1": keyring}},
	}
	gateway := provider.NewGatewayRuntime(cfg, zap.NewNop())
	runtime, err := provider.NewControlPlaneRuntime(cfg, gateway)
	if err != nil {
		t.Fatal(err)
	}
	runContext, cancel := context.WithCancel(context.Background())
	gateway.Start(runContext)
	t.Cleanup(func() {
		cancel()
		stopContext, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = gateway.Stop(stopContext)
		_ = gateway.CloseDatabase()
	})
	waitForDatabase(t, gateway)
	handler := middleware.RequestID(middleware.ControlPlaneAuth(runtime.Authorizer)(runtime.Handler))

	organization := postResource(t, handler, "/v1/organizations", `{"name":"Acme"}`)
	project := postResource(t, handler, "/v1/organizations/"+organization.ID.String()+"/projects", `{"name":"Production"}`)
	virtualKey := postResource(t, handler, "/v1/projects/"+project.ID.String()+"/virtual-keys", `{"name":"ci"}`)
	providerResource := postResource(t, handler, "/v1/providers", `{"name":"OpenAI","connector_type":"openai","base_url":"https://api.openai.com/"}`)
	credentialBody := fmt.Sprintf(`{"provider_id":%q,"scope":{"kind":"platform"},"credential":{"api_key":"provider-secret-integration"}}`, providerResource.ID)
	credential := postResource(t, handler, "/v1/provider-credentials", credentialBody)
	fakeProvider := postResource(t, handler, "/v1/providers", `{"name":"Fake","connector_type":"fake","base_url":""}`)
	deploymentBody := fmt.Sprintf(`{"provider_id":%q,"name":"fake-primary","upstream_model":"fake-model","upstream_protocol":"fake","scope":{"kind":"platform"},"capabilities":{"text":true,"streaming":true}}`, fakeProvider.ID)
	deployment := postResource(t, handler, "/v1/deployments", deploymentBody)
	aliasBody := fmt.Sprintf(`{"project_id":%q,"name":"assistant"}`, project.ID)
	modelAlias := postResource(t, handler, "/v1/model-aliases", aliasBody)
	waitForGatewaySnapshot(t, gateway, modelAlias.Revision)
	targetBody := fmt.Sprintf(`{"deployment_id":%q,"priority":0,"weight":100}`, deployment.ID)
	target := postResource(t, handler, "/v1/model-aliases/"+modelAlias.ID.String()+"/route-targets", targetBody)
	waitForGatewaySnapshot(t, gateway, target.Revision)
	preparedInitialSession, err := gateway.Store().Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := preparedInitialSession.Resolve(project.ID, "assistant"); err == nil {
		t.Fatal("new model alias became resolvable before explicit activation")
	}
	activatedAlias := patchResource(t, handler, "/v1/model-aliases/"+modelAlias.ID.String(), `{"status":"active"}`)
	waitForGatewaySnapshot(t, gateway, activatedAlias.Revision)
	session, err := gateway.Store().Begin()
	if err != nil {
		t.Fatal(err)
	}
	access, err := session.Authenticate(virtualKey.Secret, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := session.Resolve(access.ProjectID, "assistant")
	if err != nil {
		t.Fatal(err)
	}
	if access.ProjectID != project.ID || plan.Deployment.ID != deployment.ID || plan.Deployment.ConnectorType != "fake" {
		t.Fatalf("shared gateway snapshot access=%+v plan=%+v", access, plan)
	}
	disabledAlias := patchResource(t, handler, "/v1/model-aliases/"+modelAlias.ID.String(), `{"status":"disabled"}`)
	waitForGatewaySnapshot(t, gateway, disabledAlias.Revision)
	revocationSession, err := gateway.Store().Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := revocationSession.Resolve(project.ID, "assistant"); err == nil {
		t.Fatal("disabled model alias remained resolvable in the published snapshot")
	}
	disabledTarget := patchResource(t, handler, "/v1/route-targets/"+target.ID.String(), `{"status":"disabled"}`)
	waitForGatewaySnapshot(t, gateway, disabledTarget.Revision)
	preparedTarget := patchResource(t, handler, "/v1/route-targets/"+target.ID.String(), `{"status":"active"}`)
	waitForGatewaySnapshot(t, gateway, preparedTarget.Revision)
	preparedSession, err := gateway.Store().Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := preparedSession.Resolve(project.ID, "assistant"); err == nil {
		t.Fatal("target prepared under disabled alias became prematurely resolvable")
	}
	reactivatedAlias := patchResource(t, handler, "/v1/model-aliases/"+modelAlias.ID.String(), `{"status":"active"}`)
	waitForGatewaySnapshot(t, gateway, reactivatedAlias.Revision)
	reactivatedSession, err := gateway.Store().Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reactivatedSession.Resolve(project.ID, "assistant"); err != nil {
		t.Fatalf("reactivated alias did not restore route: %v", err)
	}

	if organization.ID.Version() != 7 || project.ID.Version() != 7 || virtualKey.ID.Version() != 7 || credential.ID.Version() != 7 || deployment.ID.Version() != 7 || modelAlias.ID.Version() != 7 || target.ID.Version() != 7 {
		t.Fatalf("one or more control-plane IDs are not UUIDv7")
	}
	if virtualKey.Secret == "" {
		t.Fatal("virtual key create response did not contain the one-time secret")
	}
	if modelAlias.Revision != 8 || target.Revision != 9 || activatedAlias.Revision != 10 || disabledAlias.Revision != 11 || disabledTarget.Revision != 12 || preparedTarget.Revision != 13 || reactivatedAlias.Revision != 14 || !strings.Contains(modelAlias.Body, `"status":"disabled"`) || !strings.Contains(disabledTarget.Body, `"status":"disabled"`) {
		t.Fatalf(
			"route lifecycle: alias_create=%d target_create=%d alias_activate=%d alias_disable=%d target_disable=%d target_prepare=%d alias_reactivate=%d alias_body=%s target_body=%s",
			modelAlias.Revision, target.Revision, activatedAlias.Revision, disabledAlias.Revision, disabledTarget.Revision, preparedTarget.Revision, reactivatedAlias.Revision, modelAlias.Body, disabledTarget.Body,
		)
	}
	for _, path := range []string{
		"/v1/organizations/" + organization.ID.String(),
		"/v1/projects/" + project.ID.String(),
		"/v1/providers/" + providerResource.ID.String(),
		"/v1/deployments/" + deployment.ID.String(),
		"/v1/model-aliases/" + modelAlias.ID.String(),
		"/v1/route-targets/" + target.ID.String(),
	} {
		_ = getResource(t, handler, path)
	}

	db, err := gateway.Database.DB(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var storedKey entity.VirtualKey
	if err := db.First(&storedKey, "id = ?", virtualKey.ID).Error; err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256([]byte(virtualKey.Secret))
	if !bytes.Equal(storedKey.Hash, wantHash[:]) {
		t.Fatal("stored virtual key hash does not match the one-time secret")
	}
	var storedCredential entity.ProviderCredential
	if err := db.First(&storedCredential, "id = ?", credential.ID).Error; err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("provider-secret-integration")
	for name, value := range map[string][]byte{
		"wrapped_key_nonce": storedCredential.WrappedKeyNonce,
		"wrapped_data_key":  storedCredential.WrappedDataKey,
		"payload_nonce":     storedCredential.PayloadNonce,
		"ciphertext":        storedCredential.Ciphertext,
	} {
		if len(value) == 0 || bytes.Contains(value, plaintext) {
			t.Fatalf("unsafe credential field %s", name)
		}
	}

	credentialRead := getResource(t, handler, "/v1/provider-credentials/"+credential.ID.String())
	for _, forbidden := range []string{"provider-secret-integration", "ciphertext", "wrapped_data_key", "payload_nonce"} {
		if strings.Contains(strings.ToLower(credentialRead), forbidden) {
			t.Fatalf("credential response leaked %q: %s", forbidden, credentialRead)
		}
	}
	keyRead := getResource(t, handler, "/v1/virtual-keys/"+virtualKey.ID.String())
	if strings.Contains(keyRead, virtualKey.Secret) || strings.Contains(strings.ToLower(keyRead), `"hash"`) {
		t.Fatalf("virtual key response leaked protected data: %s", keyRead)
	}
}

type createdResource struct {
	ID       uuid.UUID
	Secret   string
	Revision int64
	Body     string
}

func postResource(t *testing.T, handler http.Handler, path, body string) createdResource {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer integration-management-token-with-entropy")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("POST %s = %d %s", path, recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data struct {
			ID     uuid.UUID `json:"id"`
			Secret string    `json:"secret"`
		} `json:"data"`
		Revision int64 `json:"config_revision"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return createdResource{ID: envelope.Data.ID, Secret: envelope.Data.Secret, Revision: envelope.Revision, Body: recorder.Body.String()}
}

func patchResource(t *testing.T, handler http.Handler, path, body string) createdResource {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer integration-management-token-with-entropy")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PATCH %s = %d %s", path, recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data struct {
			ID uuid.UUID `json:"id"`
		} `json:"data"`
		Revision int64 `json:"config_revision"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return createdResource{ID: envelope.Data.ID, Revision: envelope.Revision, Body: recorder.Body.String()}
}

func getResource(t *testing.T, handler http.Handler, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer integration-management-token-with-entropy")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, recorder.Code, recorder.Body.String())
	}
	return recorder.Body.String()
}

func waitForDatabase(t *testing.T, runtime *provider.GatewayRuntime) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		if runtime.Database.Available() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("database runtime did not become available")
		case <-ticker.C:
		}
	}
}

func waitForGatewaySnapshot(t *testing.T, runtime *provider.GatewayRuntime, revision int64) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		if current, ok := runtime.Store().Current(); ok && current.Revision() == revision {
			return
		}
		select {
		case <-deadline.C:
			current, _ := runtime.Store().Current()
			t.Fatalf("gateway snapshot = %v, want revision %d", current, revision)
		case <-ticker.C:
		}
	}
}

func openPostgres(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := database.Open(context.Background(), config.DatabaseConfig{
		Driver: "postgres", DSN: dsn, MaxIdleConnections: 1, MaxOpenConnections: 4,
		ConnectionLifetime: time.Minute, ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	return db
}
