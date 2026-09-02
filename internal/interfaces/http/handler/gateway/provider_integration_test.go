package gateway_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gatewayservice "github.com/goairix/llm-proxy/internal/application/gateway/service"
	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
	tenantmodel "github.com/goairix/llm-proxy/internal/domain/tenancy/model"
	anthropicconnector "github.com/goairix/llm-proxy/internal/infrastructure/connector/anthropic"
	openconnector "github.com/goairix/llm-proxy/internal/infrastructure/connector/openai"
	credentialsecurity "github.com/goairix/llm-proxy/internal/infrastructure/security/credential"
)

func TestProviderConnectorCrossProtocolMatrix(t *testing.T) {
	for _, clientProtocol := range []string{"chat", "responses", "anthropic"} {
		for _, upstreamProtocol := range []catalogmodel.UpstreamProtocol{catalogmodel.UpstreamChatCompletions, catalogmodel.UpstreamResponses, catalogmodel.UpstreamAnthropicMessages} {
			for _, stream := range []bool{false, true} {
				t.Run(clientProtocol+"/"+string(upstreamProtocol)+"/"+strconv.FormatBool(stream), func(t *testing.T) {
					runProviderMatrixCase(t, clientProtocol, upstreamProtocol, stream)
				})
			}
		}
	}
}

type capturedUpstream struct {
	mu                                                      sync.Mutex
	count                                                   int
	path, authorization, anthropicVersion, body, virtualKey string
}

func runProviderMatrixCase(t *testing.T, clientProtocol string, upstreamProtocol catalogmodel.UpstreamProtocol, stream bool) {
	t.Helper()
	capture := &capturedUpstream{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		capture.mu.Lock()
		capture.count++
		capture.path = request.URL.Path
		capture.authorization = request.Header.Get("Authorization")
		capture.anthropicVersion = request.Header.Get("anthropic-version")
		capture.virtualKey = request.Header.Get("x-api-key")
		capture.body = string(body)
		capture.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		_, _ = io.WriteString(w, providerUpstreamFixture(upstreamProtocol, stream))
	}))
	t.Cleanup(upstream.Close)

	organization, _ := tenantmodel.NewOrganization("Acme")
	project, _ := tenantmodel.NewProject(organization.ID, "Production")
	virtualSecret := "llmp_v1_provider_matrix_virtual_key"
	hash := sha256.Sum256([]byte(virtualSecret))
	virtualKey, _ := tenantmodel.NewVirtualKey(project.ID, "matrix", hash, "llmp_v1_provider", "_key", nil)
	connectorType := catalogmodel.ConnectorOpenAI
	providerName := "OpenAI"
	if upstreamProtocol == catalogmodel.UpstreamAnthropicMessages {
		connectorType = catalogmodel.ConnectorAnthropic
		providerName = "Anthropic"
	}
	providerResource, err := catalogmodel.NewProvider(providerName, connectorType, upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	keyring := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cipher, err := credentialsecurity.NewCipher("v1", map[string]string{"v1": keyring})
	if err != nil {
		t.Fatal(err)
	}
	credentialEntity, err := sharedmodel.NewEntity()
	if err != nil {
		t.Fatal(err)
	}
	scope := catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}
	sealed, err := cipher.Seal(context.Background(), credentialEntity.ID, providerResource.ID, scope, []byte(`{"api_key":"provider-matrix-upstream-key"}`))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := catalogmodel.NewProviderCredentialWithEntity(credentialEntity, providerResource.ID, scope, sealed)
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := catalogmodel.NewDeployment(providerResource.ID, "primary", "upstream-model", upstreamProtocol, scope, catalogmodel.CapabilitySet{Text: true, ImageInput: true, Tools: true, StructuredOutput: true, Streaming: true})
	if err != nil {
		t.Fatal(err)
	}
	alias, _ := catalogmodel.NewModelAlias(project.ID, "assistant")
	alias.Status = sharedmodel.StatusActive
	target, _ := catalogmodel.NewRouteTarget(alias.ID, deployment.ID, 0, 100)
	compiled, err := gatewaysnapshot.NewCompiler().Compile(gatewaysnapshot.SourceConfig{Revision: 1, Organizations: []tenantmodel.Organization{*organization}, Projects: []tenantmodel.Project{*project}, VirtualKeys: []tenantmodel.VirtualKey{*virtualKey}, Providers: []catalogmodel.Provider{*providerResource}, Credentials: []catalogmodel.ProviderCredential{*credential}, Deployments: []catalogmodel.Deployment{*deployment}, ModelAliases: []catalogmodel.ModelAlias{*alias}, RouteTargets: []catalogmodel.RouteTarget{*target}}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	store := gatewaysnapshot.NewStore()
	store.Publish(compiled)
	registry := integrationRegistry{}
	if connectorType == catalogmodel.ConnectorAnthropic {
		connector, connectorErr := anthropicconnector.New(anthropicconnector.Options{Client: upstream.Client(), CredentialOpener: cipher, CompleteTimeout: time.Second, StreamIdleTimeout: time.Second})
		if connectorErr != nil {
			t.Fatal(connectorErr)
		}
		registry[connectorType] = connector
	} else {
		connector, connectorErr := openconnector.New(openconnector.Options{ConnectorType: catalogmodel.ConnectorOpenAI, ResponsesClient: upstream.Client(), ChatClient: upstream.Client(), CredentialOpener: cipher, CompleteTimeout: time.Second, StreamIdleTimeout: time.Second})
		if connectorErr != nil {
			t.Fatal(connectorErr)
		}
		registry[connectorType] = connector
	}
	gateway := gatewayservice.New(store, registry, gatewaysnapshot.NewCredentialSelector())
	server := httptest.NewServer(unifiedGatewayTestRouter(gateway))
	t.Cleanup(server.Close)
	path, body, anthropic := providerClientRequest(clientProtocol, stream)
	request, _ := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if anthropic {
		request.Header.Set("x-api-key", virtualSecret)
	} else {
		request.Header.Set("Authorization", "Bearer "+virtualSecret)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(payload), "assistant") || !strings.Contains(string(payload), "call_one") || !strings.Contains(string(payload), "11") || !strings.Contains(string(payload), "7") {
		t.Fatalf("status=%d body=%s", response.StatusCode, payload)
	}
	if stream && !strings.Contains(string(payload), "event:") && !strings.Contains(string(payload), "data:") {
		t.Fatalf("stream body=%s", payload)
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	wantPath := "/v1/chat/completions"
	if upstreamProtocol == catalogmodel.UpstreamResponses {
		wantPath = "/v1/responses"
	}
	wantAuthorization := "Bearer provider-matrix-upstream-key"
	wantVirtualKey := ""
	wantAnthropicVersion := ""
	if upstreamProtocol == catalogmodel.UpstreamAnthropicMessages {
		wantPath = "/v1/messages"
		wantAuthorization = ""
		wantVirtualKey = "provider-matrix-upstream-key"
		wantAnthropicVersion = "2023-06-01"
	}
	if capture.count != 1 || capture.path != wantPath || capture.authorization != wantAuthorization || capture.virtualKey != wantVirtualKey || capture.anthropicVersion != wantAnthropicVersion || strings.Contains(capture.body, virtualSecret) || !strings.Contains(capture.body, "upstream-model") {
		t.Fatalf("capture=%+v", capture)
	}
}

func providerClientRequest(protocol string, stream bool) (string, string, bool) {
	flag := strconv.FormatBool(stream)
	switch protocol {
	case "responses":
		return "/v1/responses", fmt.Sprintf(`{"model":"assistant","input":"hello","max_output_tokens":16,"stream":%s}`, flag), false
	case "anthropic":
		return "/v1/messages", fmt.Sprintf(`{"model":"assistant","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":%s}`, flag), true
	default:
		return "/v1/chat/completions", fmt.Sprintf(`{"model":"assistant","messages":[{"role":"user","content":"hello"}],"max_completion_tokens":16,"stream":%s,"stream_options":{"include_usage":true}}`, flag), false
	}
}

func providerUpstreamFixture(protocol catalogmodel.UpstreamProtocol, stream bool) string {
	if protocol == catalogmodel.UpstreamAnthropicMessages {
		if stream {
			return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fixture\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-upstream\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":11,\"output_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_one\",\"name\":\"weather\",\"input\":{}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":7}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		}
		return `{"id":"msg_fixture","type":"message","role":"assistant","model":"claude-upstream","content":[{"type":"tool_use","id":"call_one","name":"weather","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":11,"output_tokens":7}}`
	}
	if protocol == catalogmodel.UpstreamResponses {
		if stream {
			return "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{}}\n\nevent: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_one\",\"name\":\"weather\",\"arguments\":\"{}\"}}\n\nevent: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":2,\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_one\",\"name\":\"weather\",\"arguments\":\"{}\"}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":3,\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"call_id\":\"call_one\",\"name\":\"weather\",\"arguments\":\"{}\"}],\"usage\":{\"input_tokens\":11,\"output_tokens\":7,\"total_tokens\":18}}}\n\n"
		}
		return `{"status":"completed","output":[{"type":"function_call","call_id":"call_one","name":"weather","arguments":"{}"}],"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}}`
	}
	if stream {
		return "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_one\",\"type\":\"function\",\"function\":{\"name\":\"weather\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":7,\"total_tokens\":18}}\n\ndata: [DONE]\n\n"
	}
	return `{"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_one","type":"function","function":{"name":"weather","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`
}
