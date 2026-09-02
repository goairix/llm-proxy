package gateway_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	gatewayservice "github.com/goairix/llm-proxy/internal/application/gateway/service"
	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
	appRuntime "github.com/goairix/llm-proxy/internal/application/runtime"
	"github.com/goairix/llm-proxy/internal/di/provider"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
	inferenceport "github.com/goairix/llm-proxy/internal/domain/inference/port"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
	tenantmodel "github.com/goairix/llm-proxy/internal/domain/tenancy/model"
	"github.com/goairix/llm-proxy/internal/infrastructure/config"
	fakeconnector "github.com/goairix/llm-proxy/internal/infrastructure/connector/fake"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/database"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/migration"
	"github.com/goairix/llm-proxy/internal/infrastructure/proxy/tokenusage"
	"github.com/goairix/llm-proxy/internal/interfaces/http/handler/dashboard"
	gatewayhandler "github.com/goairix/llm-proxy/internal/interfaces/http/handler/gateway"
	"github.com/goairix/llm-proxy/internal/interfaces/http/router"
)

const integrationManagementToken = "integration-management-token-with-enough-entropy"

func TestUnifiedGatewayProtocolGoldenWithCompiledSnapshot(t *testing.T) {
	server, virtualKey, store := newCompiledGatewayServer(t)
	defer server.Close()

	openAIText := gatewayRequest(t, server.Client(), server.URL+"/v1/chat/completions", virtualKey,
		`{"model":"assistant","messages":[{"role":"user","content":"hello"}]}`)
	assertGolden(t, "testdata/openai_text_response.golden.json", normalizeGatewayOutput(openAIText.Body))
	anthropicText := anthropicGatewayRequest(t, server.Client(), server.URL+"/v1/messages", virtualKey,
		`{"model":"assistant","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`)
	assertGolden(t, "testdata/anthropic_text_response.golden.json", normalizeGatewayOutput(anthropicText.Body))
	assertTextUsage(t, openAIText.Body, anthropicText.Body)
	assertSharedProtocolFeatures(t, server.Client(), server.URL, virtualKey)

	openAITool := gatewayRequest(t, server.Client(), server.URL+"/v1/chat/completions", virtualKey, openAIToolStreamRequest())
	assertGolden(t, "testdata/openai_tool_stream.golden", normalizeGatewayOutput(openAITool.Body))
	anthropicTool := anthropicGatewayRequest(t, server.Client(), server.URL+"/v1/messages", virtualKey, anthropicToolStreamRequest())
	assertGolden(t, "testdata/anthropic_tool_stream.golden", normalizeGatewayOutput(anthropicTool.Body))
	assertStreamUsage(t, openAITool.Body, anthropicTool.Body)

	wrongKey := gatewayRequest(t, server.Client(), server.URL+"/v1/chat/completions", "wrong-key",
		`{"model":"assistant","messages":[{"role":"user","content":"hello"}]}`)
	if wrongKey.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-key status=%d body=%s", wrongKey.StatusCode, wrongKey.Body)
	}
	unknownAlias := anthropicGatewayRequest(t, server.Client(), server.URL+"/v1/messages", virtualKey,
		`{"model":"missing","max_tokens":1,"messages":[{"role":"user","content":"hello"}]}`)
	if unknownAlias.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown-alias status=%d body=%s", unknownAlias.StatusCode, unknownAlias.Body)
	}
	assertStreamErrorUsesProtocolEnvelope(t, store, virtualKey)

	limited, limitedKey, _ := newCompiledGatewayServerWithCapabilities(t, catalogmodel.CapabilitySet{Text: true, Streaming: true})
	defer limited.Close()
	unsupported := gatewayRequest(t, limited.Client(), limited.URL+"/v1/chat/completions", limitedKey,
		`{"model":"assistant","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]}]}`)
	if unsupported.StatusCode != http.StatusBadRequest || !strings.Contains(unsupported.Body, "capability_unsupported") {
		t.Fatalf("unsupported status=%d body=%s", unsupported.StatusCode, unsupported.Body)
	}
}

func TestUnifiedGatewaySSEFlushesThroughFullRouter(t *testing.T) {
	compiledServer, virtualKey, store := newCompiledGatewayServer(t)
	compiledServer.Close()

	for _, test := range []struct {
		name, path, body, firstPrefix string
		anthropic                     bool
	}{
		{name: "openai", path: "/v1/chat/completions", body: `{"model":"assistant","messages":[{"role":"user","content":"hello"}],"stream":true}`, firstPrefix: "data: "},
		{name: "anthropic", path: "/v1/messages", body: `{"model":"assistant","max_tokens":128,"messages":[{"role":"user","content":"hello"}],"stream":true}`, firstPrefix: "event: message_start", anthropic: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			release := make(chan struct{})
			connector := &blockingConnector{delegate: fakeconnector.New(fakeconnector.Options{}), release: release}
			gateway := gatewayservice.New(store, integrationRegistry{"fake": connector}, gatewaysnapshot.NewCredentialSelector())
			root := unifiedGatewayTestRouter(gateway)
			server := httptest.NewServer(root)
			defer server.Close()

			request, err := http.NewRequest(http.MethodPost, server.URL+test.path, strings.NewReader(test.body))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			if test.anthropic {
				request.Header.Set("x-api-key", virtualKey)
			} else {
				request.Header.Set("Authorization", "Bearer "+virtualKey)
			}
			client := server.Client()
			client.Timeout = 2 * time.Second
			response, err := client.Do(request)
			if err != nil {
				t.Fatalf("first frame was not flushed: %v", err)
			}
			reader := bufio.NewReader(response.Body)
			first, err := reader.ReadString('\n')
			if err != nil || !strings.HasPrefix(first, test.firstPrefix) {
				response.Body.Close()
				t.Fatalf("first line=%q err=%v", first, err)
			}
			close(release)
			_, _ = io.Copy(io.Discard, reader)
			_ = response.Body.Close()
		})
	}
}

func TestConnectorInvocationNeverContainsRawVirtualKey(t *testing.T) {
	compiledServer, virtualKey, store := newCompiledGatewayServer(t)
	compiledServer.Close()
	capture := &capturingConnector{delegate: fakeconnector.New(fakeconnector.Options{}), invocations: make(chan gatewayport.Invocation, 1)}
	gateway := gatewayservice.New(store, integrationRegistry{"fake": capture}, gatewaysnapshot.NewCredentialSelector())
	server := httptest.NewServer(gatewayhandler.NewOpenAI(gateway))
	defer server.Close()
	response := gatewayRequest(t, server.Client(), server.URL, virtualKey,
		`{"model":"assistant","messages":[{"role":"user","content":"hello"}]}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, response.Body)
	}
	invocation := <-capture.invocations
	encoded, err := json.Marshal(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), virtualKey) {
		t.Fatalf("connector invocation leaked raw virtual key: %s", encoded)
	}
}

func unifiedGatewayTestRouter(gateway gatewayservice.Gateway) http.Handler {
	return router.New(router.Config{BaseURL: "http://localhost", Version: "test"}, router.Dependencies{
		Logger: zap.NewNop(), Instrumenter: integrationInstrumenter{}, Readiness: appRuntime.NewReadiness(),
		Stats: &dashboard.Stats{}, ObserverFactory: tokenusage.NewObserver,
		OpenAIProxy: http.NotFoundHandler(), AnthropicProxy: http.NotFoundHandler(),
		OpenAIGateway: gatewayhandler.NewOpenAI(gateway), OpenAIResponsesGateway: gatewayhandler.NewResponses(gateway), AnthropicGateway: gatewayhandler.NewAnthropic(gateway),
	})
}

func newCompiledGatewayServer(t *testing.T) (*httptest.Server, string, *gatewaysnapshot.Store) {
	t.Helper()
	return newCompiledGatewayServerWithCapabilities(t, catalogmodel.CapabilitySet{
		Text: true, ImageInput: true, Tools: true, StructuredOutput: true, Streaming: true,
	})
}

func newCompiledGatewayServerWithCapabilities(t *testing.T, capabilities catalogmodel.CapabilitySet) (*httptest.Server, string, *gatewaysnapshot.Store) {
	t.Helper()
	organization, err := tenantmodel.NewOrganization("Acme")
	if err != nil {
		t.Fatal(err)
	}
	project, err := tenantmodel.NewProject(organization.ID, "Production")
	if err != nil {
		t.Fatal(err)
	}
	secret := "llmp_v1_compiled_snapshot_test_key"
	hash := sha256.Sum256([]byte(secret))
	virtualKey, err := tenantmodel.NewVirtualKey(project.ID, "test", hash, "llmp_v1_test", "_key", nil)
	if err != nil {
		t.Fatal(err)
	}
	providerResource, err := catalogmodel.NewProvider("Fake", catalogmodel.ConnectorFake, "")
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := catalogmodel.NewDeployment(
		providerResource.ID, "fake-primary", "fake-model", catalogmodel.UpstreamFake,
		catalogmodel.Scope{Kind: catalogmodel.ScopePlatform},
		capabilities,
	)
	if err != nil {
		t.Fatal(err)
	}
	modelAlias, err := catalogmodel.NewModelAlias(project.ID, "assistant")
	if err != nil {
		t.Fatal(err)
	}
	modelAlias.Status = sharedmodel.StatusActive
	target, err := catalogmodel.NewRouteTarget(modelAlias.ID, deployment.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := gatewaysnapshot.NewCompiler().Compile(gatewaysnapshot.SourceConfig{
		Revision: 1, Organizations: []tenantmodel.Organization{*organization}, Projects: []tenantmodel.Project{*project},
		VirtualKeys: []tenantmodel.VirtualKey{*virtualKey}, Providers: []catalogmodel.Provider{*providerResource},
		Deployments: []catalogmodel.Deployment{*deployment}, ModelAliases: []catalogmodel.ModelAlias{*modelAlias},
		RouteTargets: []catalogmodel.RouteTarget{*target},
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	store := gatewaysnapshot.NewStore()
	store.Publish(compiled)
	gateway := gatewayservice.New(store, integrationRegistry{"fake": fakeconnector.New(fakeconnector.Options{})}, gatewaysnapshot.NewCredentialSelector())
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", gatewayhandler.NewOpenAI(gateway))
	mux.Handle("/v1/messages", gatewayhandler.NewAnthropic(gateway))
	return httptest.NewServer(mux), secret, store
}

func TestIntegrationUnifiedGatewayThroughPostgresAndBothProtocols(t *testing.T) {
	cfg := openGatewayIntegrationConfig(t)
	logger := zap.NewNop()
	runtime := provider.NewGatewayRuntime(cfg, logger)
	cipherRuntime, err := provider.NewCredentialCipherRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	controlPlane, err := provider.NewControlPlaneRuntime(cfg, runtime, cipherRuntime)
	if err != nil {
		t.Fatal(err)
	}
	dataPlane, err := provider.NewUnifiedGatewayHandlers(cfg, runtime, cipherRuntime, nil)
	if err != nil {
		t.Fatal(err)
	}
	root := router.New(router.Config{BaseURL: "http://localhost", Version: "integration"}, router.Dependencies{
		Logger: logger, Instrumenter: integrationInstrumenter{}, Readiness: appRuntime.NewReadiness(),
		Stats: &dashboard.Stats{}, ObserverFactory: tokenusage.NewObserver,
		OpenAIProxy: http.NotFoundHandler(), AnthropicProxy: http.NotFoundHandler(),
		OpenAIGateway: dataPlane.OpenAIChat, OpenAIResponsesGateway: dataPlane.OpenAIResponses, AnthropicGateway: dataPlane.Anthropic,
		ControlPlane: controlPlane.Handler, ControlAuth: controlPlane.Authorizer,
	})
	server := httptest.NewServer(root)
	t.Cleanup(server.Close)

	noSnapshot := gatewayRequest(t, server.Client(), server.URL+"/v1/chat/completions", "missing", `{"model":"assistant","messages":[{"role":"user","content":"hello"}]}`)
	if noSnapshot.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("no-snapshot status=%d body=%s", noSnapshot.StatusCode, noSnapshot.Body)
	}

	runContext, cancelRun := context.WithCancel(context.Background())
	runtime.Start(runContext)
	t.Cleanup(func() {
		server.Close()
		cancelRun()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runtime.Stop(ctx)
		_ = runtime.CloseDatabase()
	})
	waitForGatewayDatabase(t, runtime)

	organization := controlPost(t, root, "/v1/organizations", `{"name":"Acme"}`)
	project := controlPost(t, root, "/v1/organizations/"+organization.ID.String()+"/projects", `{"name":"Production"}`)
	virtualKey := controlPost(t, root, "/v1/projects/"+project.ID.String()+"/virtual-keys", `{"name":"integration"}`)
	providerResource := controlPost(t, root, "/v1/providers", `{"name":"Fake","connector_type":"fake"}`)
	deployment := controlPost(t, root, "/v1/deployments", fmt.Sprintf(
		`{"provider_id":%q,"name":"fake-primary","upstream_model":"fake-model","connector_type":"fake","scope":{"kind":"platform"},"capabilities":{"text":true,"image_input":true,"tools":true,"structured_output":true,"streaming":true}}`,
		providerResource.ID,
	))
	modelAlias := controlPost(t, root, "/v1/model-aliases", fmt.Sprintf(`{"project_id":%q,"name":"assistant"}`, project.ID))
	target := controlPost(t, root, "/v1/model-aliases/"+modelAlias.ID.String()+"/route-targets", fmt.Sprintf(
		`{"deployment_id":%q,"priority":0,"weight":100}`, deployment.ID,
	))
	activated := controlPatch(t, root, "/v1/model-aliases/"+modelAlias.ID.String(), `{"status":"active"}`)
	waitForGatewayRevision(t, runtime, activated.Revision)
	if virtualKey.Secret == "" || target.ID == uuid.Nil {
		t.Fatal("control plane did not return virtual key or route target")
	}

	openAIText := gatewayRequest(t, server.Client(), server.URL+"/v1/chat/completions", virtualKey.Secret,
		`{"model":"assistant","messages":[{"role":"user","content":"hello"}]}`)
	assertGolden(t, "testdata/openai_text_response.golden.json", normalizeGatewayOutput(openAIText.Body))
	anthropicText := anthropicGatewayRequest(t, server.Client(), server.URL+"/v1/messages", virtualKey.Secret,
		`{"model":"assistant","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`)
	assertGolden(t, "testdata/anthropic_text_response.golden.json", normalizeGatewayOutput(anthropicText.Body))

	assertSharedProtocolFeatures(t, server.Client(), server.URL, virtualKey.Secret)

	openAITool := gatewayRequest(t, server.Client(), server.URL+"/v1/chat/completions", virtualKey.Secret, openAIToolStreamRequest())
	assertGolden(t, "testdata/openai_tool_stream.golden", normalizeGatewayOutput(openAITool.Body))
	anthropicTool := anthropicGatewayRequest(t, server.Client(), server.URL+"/v1/messages", virtualKey.Secret, anthropicToolStreamRequest())
	assertGolden(t, "testdata/anthropic_tool_stream.golden", normalizeGatewayOutput(anthropicTool.Body))

	wrongKey := gatewayRequest(t, server.Client(), server.URL+"/v1/chat/completions", "wrong-key",
		`{"model":"assistant","messages":[{"role":"user","content":"hello"}]}`)
	if wrongKey.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-key status=%d body=%s", wrongKey.StatusCode, wrongKey.Body)
	}
	unknownAlias := anthropicGatewayRequest(t, server.Client(), server.URL+"/v1/messages", virtualKey.Secret,
		`{"model":"missing","max_tokens":1,"messages":[{"role":"user","content":"hello"}]}`)
	if unknownAlias.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown-alias status=%d body=%s", unknownAlias.StatusCode, unknownAlias.Body)
	}

	assertStreamErrorUsesProtocolEnvelope(t, runtime.Store(), virtualKey.Secret)
}

func TestIntegrationAnthropicProviderThroughPostgresAndAllClientProtocols(t *testing.T) {
	cfg := openGatewayIntegrationConfig(t)
	upstreamRequests := make(chan anthropicUpstreamRequest, 6)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		var decoded struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
			Stream    bool   `json:"stream"`
		}
		decodeErr := json.Unmarshal(body, &decoded)
		upstreamRequests <- anthropicUpstreamRequest{
			Path: request.URL.Path, Authorization: request.Header.Get("Authorization"),
			APIKey: request.Header.Get("x-api-key"), APIVersion: request.Header.Get("anthropic-version"),
			Body: string(body), Model: decoded.Model, MaxTokens: decoded.MaxTokens, Stream: decoded.Stream, DecodeErr: decodeErr,
		}
		if decoded.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		_, _ = io.WriteString(w, providerUpstreamFixture(catalogmodel.UpstreamAnthropicMessages, decoded.Stream))
	}))
	t.Cleanup(upstream.Close)

	logger := zap.NewNop()
	runtime := provider.NewGatewayRuntime(cfg, logger)
	cipherRuntime, err := provider.NewCredentialCipherRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	controlPlane, err := provider.NewControlPlaneRuntime(cfg, runtime, cipherRuntime)
	if err != nil {
		t.Fatal(err)
	}
	dataPlane, err := provider.NewUnifiedGatewayHandlers(cfg, runtime, cipherRuntime, nil)
	if err != nil {
		t.Fatal(err)
	}
	root := router.New(router.Config{BaseURL: "http://localhost", Version: "integration"}, router.Dependencies{
		Logger: logger, Instrumenter: integrationInstrumenter{}, Readiness: appRuntime.NewReadiness(),
		Stats: &dashboard.Stats{}, ObserverFactory: tokenusage.NewObserver,
		OpenAIProxy: http.NotFoundHandler(), AnthropicProxy: http.NotFoundHandler(),
		OpenAIGateway: dataPlane.OpenAIChat, OpenAIResponsesGateway: dataPlane.OpenAIResponses, AnthropicGateway: dataPlane.Anthropic,
		ControlPlane: controlPlane.Handler, ControlAuth: controlPlane.Authorizer,
	})
	server := httptest.NewServer(root)

	runContext, cancelRun := context.WithCancel(context.Background())
	runtime.Start(runContext)
	t.Cleanup(func() {
		server.Close()
		cancelRun()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runtime.Stop(ctx)
		_ = runtime.CloseDatabase()
	})
	waitForGatewayDatabase(t, runtime)

	organization := controlPost(t, root, "/v1/organizations", `{"name":"Anthropic Integration"}`)
	project := controlPost(t, root, "/v1/organizations/"+organization.ID.String()+"/projects", `{"name":"Production"}`)
	virtualKey := controlPost(t, root, "/v1/projects/"+project.ID.String()+"/virtual-keys", `{"name":"anthropic-integration"}`)
	providerResource := controlPost(t, root, "/v1/providers", fmt.Sprintf(
		`{"name":"Anthropic","connector_type":"anthropic","base_url":%q}`, upstream.URL,
	))
	credential := controlPost(t, root, "/v1/provider-credentials", fmt.Sprintf(
		`{"provider_id":%q,"scope":{"kind":"platform"},"credential":{"api_key":"integration-upstream-key"}}`, providerResource.ID,
	))
	deployment := controlPost(t, root, "/v1/deployments", fmt.Sprintf(
		`{"provider_id":%q,"name":"claude","upstream_model":"claude-upstream","upstream_protocol":"anthropic_messages","scope":{"kind":"platform"},"capabilities":{"text":true,"image_input":true,"tools":true,"structured_output":true,"streaming":true}}`,
		providerResource.ID,
	))
	modelAlias := controlPost(t, root, "/v1/model-aliases", fmt.Sprintf(`{"project_id":%q,"name":"assistant"}`, project.ID))
	target := controlPost(t, root, "/v1/model-aliases/"+modelAlias.ID.String()+"/route-targets", fmt.Sprintf(
		`{"deployment_id":%q,"priority":0,"weight":100}`, deployment.ID,
	))
	activated := controlPatch(t, root, "/v1/model-aliases/"+modelAlias.ID.String(), `{"status":"active"}`)
	waitForGatewayRevision(t, runtime, activated.Revision)
	if virtualKey.Secret == "" || credential.ID == uuid.Nil || target.ID == uuid.Nil {
		t.Fatal("control plane did not persist the Anthropic route and platform credential")
	}

	for _, clientProtocol := range []string{"chat", "responses", "anthropic"} {
		for _, stream := range []bool{false, true} {
			t.Run(clientProtocol+"/"+strconv.FormatBool(stream), func(t *testing.T) {
				path, body, anthropicClient := providerClientRequest(clientProtocol, stream)
				var response gatewayHTTPResponse
				if anthropicClient {
					response = anthropicGatewayRequest(t, server.Client(), server.URL+path, virtualKey.Secret, body)
				} else {
					response = gatewayRequest(t, server.Client(), server.URL+path, virtualKey.Secret, body)
				}
				if response.StatusCode != http.StatusOK || !strings.Contains(response.Body, "assistant") || !strings.Contains(response.Body, "call_one") || !strings.Contains(response.Body, "11") || !strings.Contains(response.Body, "7") {
					t.Fatalf("status=%d body=%s", response.StatusCode, response.Body)
				}
				for _, forbidden := range []string{"claude-upstream", "integration-upstream-key", upstream.URL} {
					if strings.Contains(response.Body, forbidden) {
						t.Fatalf("client response leaked %q: %s", forbidden, response.Body)
					}
				}
				if stream && !strings.Contains(response.Body, "event:") && !strings.Contains(response.Body, "data:") {
					t.Fatalf("stream body=%s", response.Body)
				}

				select {
				case captured := <-upstreamRequests:
					if captured.DecodeErr != nil || captured.Path != "/v1/messages" || captured.Authorization != "" || captured.APIKey != "integration-upstream-key" || captured.APIVersion != "2023-06-01" || captured.Model != "claude-upstream" || captured.MaxTokens <= 0 || captured.Stream != stream || strings.Contains(captured.Body, virtualKey.Secret) {
						t.Fatalf("upstream request=%+v", captured)
					}
				case <-time.After(time.Second):
					t.Fatal("Anthropic upstream request was not captured")
				}
			})
		}
	}
}

type anthropicUpstreamRequest struct {
	Path, Authorization, APIKey, APIVersion, Body, Model string
	Stream                                               bool
	MaxTokens                                            int
	DecodeErr                                            error
}

func openAIToolStreamRequest() string {
	return `{"model":"assistant","messages":[{"role":"user","content":"weather"}],"stream":true,"stream_options":{"include_usage":true},"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string","const":"Shanghai"}},"required":["city"],"additionalProperties":false}}}],"tool_choice":"required"}`
}

func anthropicToolStreamRequest() string {
	return `{"model":"assistant","max_tokens":128,"messages":[{"role":"user","content":"weather"}],"stream":true,"tools":[{"name":"weather","input_schema":{"type":"object","properties":{"city":{"type":"string","const":"Shanghai"}},"required":["city"],"additionalProperties":false}}],"tool_choice":{"type":"any"}}`
}

func assertSharedProtocolFeatures(t *testing.T, client *http.Client, baseURL, virtualKey string) {
	t.Helper()
	assertSuccessfulBodyContains(t, gatewayRequest(t, client, baseURL+"/v1/chat/completions", virtualKey,
		`{"model":"assistant","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]}]}`), "[image:url]")
	assertSuccessfulBodyContains(t, anthropicGatewayRequest(t, client, baseURL+"/v1/messages", virtualKey,
		`{"model":"assistant","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]}]}`), "[image:image/png]")

	structuredSchema := `{"type":"object","properties":{"answer":{"const":"ok"}},"required":["answer"],"additionalProperties":false}`
	assertSuccessfulBodyContains(t, gatewayRequest(t, client, baseURL+"/v1/chat/completions", virtualKey,
		`{"model":"assistant","messages":[{"role":"user","content":"json"}],"response_format":{"type":"json_schema","json_schema":{"name":"answer","strict":true,"schema":`+structuredSchema+`}}}`), `\"answer\":\"ok\"`)
	assertSuccessfulBodyContains(t, anthropicGatewayRequest(t, client, baseURL+"/v1/messages", virtualKey,
		`{"model":"assistant","max_tokens":128,"messages":[{"role":"user","content":"json"}],"output_config":{"format":{"type":"json_schema","schema":`+structuredSchema+`}}}`), `\"answer\":\"ok\"`)

	assertSuccessfulBodyContains(t, gatewayRequest(t, client, baseURL+"/v1/chat/completions", virtualKey,
		`{"model":"assistant","messages":[{"role":"user","content":"weather"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_known","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Shanghai\"}"}}]},{"role":"tool","tool_call_id":"call_known","content":"30"}]}`), "工具结果已接收: 30")
	assertSuccessfulBodyContains(t, anthropicGatewayRequest(t, client, baseURL+"/v1/messages", virtualKey,
		`{"model":"assistant","max_tokens":128,"messages":[{"role":"user","content":"weather"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_known","name":"weather","input":{"city":"Shanghai"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_known","content":"30"}]}]}`), "工具结果已接收: 30")
}

type integrationInstrumenter struct{}

func (integrationInstrumenter) WrapHandler(_ string, next http.Handler) http.Handler { return next }

type controlResource struct {
	ID       uuid.UUID
	Secret   string
	Revision int64
}

func controlPost(t *testing.T, handler http.Handler, path, body string) controlResource {
	t.Helper()
	return controlMutation(t, handler, http.MethodPost, path, body, http.StatusCreated)
}

func controlPatch(t *testing.T, handler http.Handler, path, body string) controlResource {
	t.Helper()
	return controlMutation(t, handler, http.MethodPatch, path, body, http.StatusOK)
}

func controlMutation(t *testing.T, handler http.Handler, method, path, body string, wantStatus int) controlResource {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+integrationManagementToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s status=%d body=%s", method, path, recorder.Code, recorder.Body.String())
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
	return controlResource{ID: envelope.Data.ID, Secret: envelope.Data.Secret, Revision: envelope.Revision}
}

type gatewayHTTPResponse struct {
	StatusCode int
	Body       string
}

func gatewayRequest(t *testing.T, client *http.Client, endpoint, virtualKey, body string) gatewayHTTPResponse {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+virtualKey)
	return doGatewayRequest(t, client, request)
}

func anthropicGatewayRequest(t *testing.T, client *http.Client, endpoint, virtualKey, body string) gatewayHTTPResponse {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-api-key", virtualKey)
	return doGatewayRequest(t, client, request)
}

func doGatewayRequest(t *testing.T, client *http.Client, request *http.Request) gatewayHTTPResponse {
	t.Helper()
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return gatewayHTTPResponse{StatusCode: response.StatusCode, Body: string(body)}
}

func assertSuccessfulBodyContains(t *testing.T, response gatewayHTTPResponse, want string) {
	t.Helper()
	if response.StatusCode != http.StatusOK || !strings.Contains(response.Body, want) {
		t.Fatalf("status=%d body=%s want=%q", response.StatusCode, response.Body, want)
	}
}

var (
	uuidPattern    = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`)
	createdPattern = regexp.MustCompile(`"created":[0-9]+`)
	tokenPattern   = regexp.MustCompile(`"(prompt_tokens|completion_tokens|total_tokens|input_tokens|output_tokens)":[0-9]+`)
)

func normalizeGatewayOutput(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = uuidPattern.ReplaceAllString(value, "<uuidv7>")
	value = createdPattern.ReplaceAllString(value, `"created":<created>`)
	value = tokenPattern.ReplaceAllString(value, `"$1":<tokens>`)
	return value
}

func assertTextUsage(t *testing.T, openAI, anthropic string) {
	t.Helper()
	var openAIRaw struct {
		Usage struct {
			Prompt     int64 `json:"prompt_tokens"`
			Completion int64 `json:"completion_tokens"`
			Total      int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(openAI), &openAIRaw); err != nil {
		t.Fatal(err)
	}
	if openAIRaw.Usage.Prompt <= 0 || openAIRaw.Usage.Completion <= 0 || openAIRaw.Usage.Total != openAIRaw.Usage.Prompt+openAIRaw.Usage.Completion {
		t.Fatalf("invalid OpenAI usage: %+v", openAIRaw.Usage)
	}
	var anthropicResponse struct {
		Usage struct {
			Input  int64 `json:"input_tokens"`
			Output int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(anthropic), &anthropicResponse); err != nil {
		t.Fatal(err)
	}
	if anthropicResponse.Usage.Input != openAIRaw.Usage.Prompt || anthropicResponse.Usage.Output != openAIRaw.Usage.Completion {
		t.Fatalf("cross-protocol usage openai=%+v anthropic=%+v", openAIRaw.Usage, anthropicResponse.Usage)
	}
}

func assertStreamUsage(t *testing.T, openAI, anthropic string) {
	t.Helper()
	var finalOpenAI struct {
		Prompt, Completion, Total int64
	}
	for _, line := range strings.Split(openAI, "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var frame struct {
			Choices []json.RawMessage `json:"choices"`
			Usage   *struct {
				Prompt     int64 `json:"prompt_tokens"`
				Completion int64 `json:"completion_tokens"`
				Total      int64 `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			t.Fatal(err)
		}
		if len(frame.Choices) == 0 && frame.Usage != nil {
			finalOpenAI.Prompt = frame.Usage.Prompt
			finalOpenAI.Completion = frame.Usage.Completion
			finalOpenAI.Total = frame.Usage.Total
		}
	}
	if finalOpenAI.Prompt <= 0 || finalOpenAI.Completion <= 0 || finalOpenAI.Total != finalOpenAI.Prompt+finalOpenAI.Completion {
		t.Fatalf("invalid OpenAI stream usage: %+v", finalOpenAI)
	}

	var sawStart bool
	var finalAnthropic struct{ Input, Output int64 }
	for _, line := range strings.Split(anthropic, "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var frame struct {
			Type  string `json:"type"`
			Usage struct {
				Input  int64 `json:"input_tokens"`
				Output int64 `json:"output_tokens"`
			} `json:"usage"`
			Message struct {
				Usage struct {
					Input  int64 `json:"input_tokens"`
					Output int64 `json:"output_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			t.Fatal(err)
		}
		switch frame.Type {
		case "message_start":
			sawStart = frame.Message.Usage.Input == 0 && frame.Message.Usage.Output == 0
		case "message_delta":
			finalAnthropic.Input, finalAnthropic.Output = frame.Usage.Input, frame.Usage.Output
		}
	}
	if !sawStart || finalAnthropic.Input != finalOpenAI.Prompt || finalAnthropic.Output != finalOpenAI.Completion {
		t.Fatalf("invalid Anthropic cumulative usage: start=%v final=%+v openai=%+v", sawStart, finalAnthropic, finalOpenAI)
	}
}

func assertGolden(t *testing.T, path, got string) {
	t.Helper()
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimRight(got, "\n") != strings.TrimRight(string(want), "\n") {
		t.Fatalf("golden mismatch %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

func waitForGatewayDatabase(t *testing.T, runtime *provider.GatewayRuntime) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.Database.Available() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("gateway database did not become available")
}

func waitForGatewayRevision(t *testing.T, runtime *provider.GatewayRuntime, revision int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if current, ok := runtime.Store().Current(); ok && current.Revision() == revision {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	current, _ := runtime.Store().Current()
	t.Fatalf("snapshot=%v want revision=%d", current, revision)
}

func openGatewayIntegrationConfig(t *testing.T) *config.Config {
	t.Helper()
	dsn := os.Getenv("LLM_PROXY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("未设置 LLM_PROXY_TEST_POSTGRES_DSN，跳过统一网关 PostgreSQL 纵向测试")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		t.Fatal("测试 DSN 必须使用 postgres:// URL 格式")
	}
	base := openGatewayPostgres(t, dsn)
	t.Cleanup(func() {
		if sqlDB, err := base.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	schema := "llm_proxy_gateway_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := base.Exec(fmt.Sprintf(`CREATE SCHEMA "%s"`, schema)).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Exec(fmt.Sprintf(`DROP SCHEMA "%s" CASCADE`, schema)).Error })
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	schemaDSN := parsed.String()
	migrationDB := openGatewayPostgres(t, schemaDSN)
	t.Cleanup(func() {
		if sqlDB, err := migrationDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := migration.Up(migrationDB); err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := migrationDB.DB(); err == nil {
		_ = sqlDB.Close()
	}
	keyring := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cfg := &config.Config{
		Gateway:              config.GatewayConfig{Enabled: true, SnapshotInterval: time.Hour, SnapshotTimeout: time.Second, RetryBackoff: 10 * time.Millisecond},
		Database:             config.DatabaseConfig{Driver: "postgres", DSN: schemaDSN, MaxIdleConnections: 1, MaxOpenConnections: 8, ConnectionLifetime: time.Minute, ConnectTimeout: 5 * time.Second},
		ControlPlane:         config.ControlPlaneConfig{Token: integrationManagementToken},
		CredentialEncryption: config.CredentialEncryptionConfig{CurrentKeyVersion: "v1", Keys: map[string]string{"v1": keyring}},
	}
	return cfg
}

func openGatewayPostgres(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := database.Open(context.Background(), config.DatabaseConfig{
		Driver: "postgres", DSN: dsn, MaxIdleConnections: 1, MaxOpenConnections: 8,
		ConnectionLifetime: time.Minute, ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

type integrationRegistry map[string]gatewayport.Connector

func (r integrationRegistry) Find(connectorType string) (gatewayport.Connector, bool) {
	connector, ok := r[connectorType]
	return connector, ok
}

type blockingConnector struct {
	delegate gatewayport.Connector
	release  <-chan struct{}
}

func (c *blockingConnector) Complete(ctx context.Context, invocation gatewayport.Invocation) (inference.Response, error) {
	return c.delegate.Complete(ctx, invocation)
}

func (c *blockingConnector) Stream(ctx context.Context, invocation gatewayport.Invocation) (inferenceport.Stream, error) {
	stream, err := c.delegate.Stream(ctx, invocation)
	if err != nil {
		return nil, err
	}
	return &blockingStream{delegate: stream, release: c.release}, nil
}

type blockingStream struct {
	delegate inferenceport.Stream
	release  <-chan struct{}
	first    bool
}

func (s *blockingStream) Recv(ctx context.Context) (inference.Event, error) {
	if !s.first {
		s.first = true
		return s.delegate.Recv(ctx)
	}
	select {
	case <-ctx.Done():
		return inference.Event{}, ctx.Err()
	case <-s.release:
		return s.delegate.Recv(ctx)
	}
}

func (s *blockingStream) Close() error { return s.delegate.Close() }

type capturingConnector struct {
	delegate    gatewayport.Connector
	invocations chan gatewayport.Invocation
}

func (c *capturingConnector) Complete(ctx context.Context, invocation gatewayport.Invocation) (inference.Response, error) {
	c.invocations <- invocation
	return c.delegate.Complete(ctx, invocation)
}

func (c *capturingConnector) Stream(ctx context.Context, invocation gatewayport.Invocation) (inferenceport.Stream, error) {
	c.invocations <- invocation
	return c.delegate.Stream(ctx, invocation)
}

func assertStreamErrorUsesProtocolEnvelope(t *testing.T, store *gatewaysnapshot.Store, virtualKey string) {
	t.Helper()
	gateway := gatewayservice.New(store, integrationRegistry{
		"fake": fakeconnector.New(fakeconnector.Options{StreamErrorAfter: 1, StreamErrorMessage: "safe stream failure"}),
	}, gatewaysnapshot.NewCredentialSelector())
	openAI := httptest.NewServer(gatewayhandler.NewOpenAI(gateway))
	defer openAI.Close()
	response := gatewayRequest(t, openAI.Client(), openAI.URL, virtualKey,
		`{"model":"assistant","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	if response.StatusCode != http.StatusOK || !strings.Contains(response.Body, `"error"`) || !strings.Contains(response.Body, "safe stream failure") || strings.Contains(response.Body, "connector received") {
		t.Fatalf("stream error status=%d body=%s", response.StatusCode, response.Body)
	}

	anthropic := httptest.NewServer(gatewayhandler.NewAnthropic(gateway))
	defer anthropic.Close()
	response = anthropicGatewayRequest(t, anthropic.Client(), anthropic.URL, virtualKey,
		`{"model":"assistant","max_tokens":128,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	if response.StatusCode != http.StatusOK || !strings.Contains(response.Body, "event: error") || !strings.Contains(response.Body, `"type":"error"`) || !strings.Contains(response.Body, "safe stream failure") {
		t.Fatalf("anthropic stream error status=%d body=%s", response.StatusCode, response.Body)
	}
}
