# Anthropic Provider Connector Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为统一网关加入平台托管的原生 Anthropic Messages 上游，使 OpenAI Chat Completions、OpenAI Responses 和 Anthropic Messages 三个客户端入口都能调用 Anthropic Deployment，并保持普通响应、SSE、凭据隔离和遥测脱敏语义。

**Architecture:** 在目录领域新增 `anthropic` Connector 类型和 `anthropic_messages` 上游协议；控制面、Repository 和 RuntimeSnapshot 继续使用现有 Provider、Deployment 和分层凭据池。新增独立 `internal/infrastructure/connector/anthropic` Wire Adapter，使用标准库 HTTP 调用固定 `/v1/messages`，把统一 Request/Response/Event 与 Anthropic JSON/SSE 互相映射；接口层不依赖厂商实现，也不新增数据面路径。

**Tech Stack:** Go 1.25、标准库 `net/http`/`encoding/json`/`bufio`、Google UUIDv7、GORM/PostgreSQL、Google Wire、OpenTelemetry `otelhttp`、`httptest.Server`。

---

## 实施约束

- 基线分支是 `v0.1-develop`，实现分支固定为普通分支 `feat/anthropic-connector`。
- 禁止使用 `git worktree`；当前分支已经从正确基线创建。
- 严格按任务顺序执行，每个功能任务先写失败测试，再写最小实现，再提交。
- 不修改 `/openai/*`、`/anthropic/*` 透明代理语义。
- 不新增数据库字段或迁移；现有 Connector/Protocol 列是 `varchar`。若测试证明源码事实不同，立即停止并回到设计评审，不能自行扩大迁移范围。
- 禁止数据库外键、GORM Relationship、Association、Preload 和级联。
- 不引入 Anthropic SDK 或其他第三方依赖，不运行无必要的 `go mod tidy`。
- 不实现 Prompt Caching 请求控制、Extended Thinking、服务端工具、Citation、重试、换 Key、多 BaseURL、故障转移或熔断。
- 所有 Go 变更运行 `gofmt`；每个任务先跑目标包，最终运行全量、Race、Vet、Wire 和 Build。

## 文件职责总览

### 领域、控制面与快照

- `internal/domain/catalog/model/provider.go`：新增 Anthropic Connector，并维护协议支持矩阵。
- `internal/domain/catalog/model/upstream_protocol.go`：新增 `anthropic_messages`。
- `internal/domain/catalog/model/model_test.go`：Provider、Deployment 和协议矩阵测试。
- `internal/application/controlplane/service/service_test.go`：Anthropic Provider、Credential 和 Deployment 的控制面关联测试。
- `internal/application/gateway/snapshot/compiler_test.go`：Anthropic 运行时 Provider/Deployment 编译测试。
- `internal/infrastructure/persistence/repository/catalog/repository_test.go`：新字符串值的 Mapper/PostgreSQL 往返测试；不修改 Entity 或迁移。
- `internal/interfaces/http/handler/controlplane/handler_test.go`：控制面 HTTP DTO 对新值的契约测试。

### Anthropic Connector

- `internal/infrastructure/connector/anthropic/credential.go`：打开加密信封并严格解析 `api_key`。
- `internal/infrastructure/connector/anthropic/request.go`：Messages 请求 DTO、参数前置校验与编码。
- `internal/infrastructure/connector/anthropic/response.go`：普通响应 DTO、Refusal、StopReason 和 Usage 映射。
- `internal/infrastructure/connector/anthropic/sse.go`：有单事件大小和空闲超时边界的 SSE Reader。
- `internal/infrastructure/connector/anthropic/stream.go`：Messages SSE 状态机和统一 Event 输出。
- `internal/infrastructure/connector/anthropic/client.go`：安全 URL、固定 Header、Body 上限和禁止重定向。
- `internal/infrastructure/connector/anthropic/errors.go`：复用稳定 ConnectorErrorKind 的错误分类。
- `internal/infrastructure/connector/anthropic/connector.go`：Complete/Stream 编排与 Invocation 校验。
- 同目录 `*_test.go`：单元、契约、取消、流式和泄露测试。
- `internal/infrastructure/connector/anthropic/testdata/`：普通与流式固定夹具，不包含真实密钥或私有地址。

### 组装、遥测与纵向测试

- `internal/infrastructure/observability/http.go`、`http_test.go`：注册静态 `anthropic/messages` 出站遥测路由。
- `internal/di/provider/gateway.go`、`gateway_test.go`：构造并注册 Anthropic Connector。
- `internal/di/wire_gen.go`：重新生成并验证无漂移。
- `internal/interfaces/http/handler/gateway/provider_integration_test.go`：三个客户端入口 × Anthropic 上游 × 普通/SSE 纵向矩阵。
- `internal/interfaces/http/handler/gateway/integration_test.go`：可选真实 PostgreSQL 控制面到数据面链路。

---

### Task 1: 扩展目录领域、控制面、快照和持久化字符串契约

**Files:**
- Modify: `internal/domain/catalog/model/provider.go`
- Modify: `internal/domain/catalog/model/upstream_protocol.go`
- Modify: `internal/domain/catalog/model/model_test.go`
- Modify: `internal/application/controlplane/service/service_test.go`
- Modify: `internal/application/gateway/snapshot/compiler_test.go`
- Modify: `internal/infrastructure/persistence/repository/catalog/repository_test.go`
- Modify: `internal/interfaces/http/handler/controlplane/handler_test.go`

- [ ] **Step 1: 写领域与协议矩阵失败测试**

在 `model_test.go` 增加：

```go
func TestAnthropicProviderSupportsMessagesOnly(t *testing.T) {
	provider, err := NewProvider("Anthropic", ConnectorAnthropic, "https://api.anthropic.com/")
	if err != nil {
		t.Fatal(err)
	}
	if provider.BaseURL != "https://api.anthropic.com" {
		t.Fatalf("BaseURL=%q", provider.BaseURL)
	}
	if !provider.Supports(UpstreamAnthropicMessages) || provider.Supports(UpstreamResponses) ||
		provider.Supports(UpstreamChatCompletions) || provider.Supports(UpstreamFake) {
		t.Fatal("unexpected Anthropic protocol matrix")
	}
}

func TestAnthropicDeploymentUsesMessagesProtocol(t *testing.T) {
	provider, err := NewProvider("Anthropic", ConnectorAnthropic, "https://api.anthropic.com")
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := NewDeployment(
		provider.ID, "claude", "claude-sonnet", UpstreamAnthropicMessages,
		Scope{Kind: ScopePlatform}, CapabilitySet{Text: true, Streaming: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if deployment.UpstreamProtocol != UpstreamAnthropicMessages || !provider.Supports(deployment.UpstreamProtocol) {
		t.Fatalf("deployment=%+v", deployment)
	}
}
```

- [ ] **Step 2: 写控制面、快照、Mapper 和 HTTP 契约失败测试**

在 `service_test.go` 增加：

```go
func TestCreateAnthropicDeploymentUsesProviderProtocol(t *testing.T) {
	provider, _ := catalogmodel.NewProvider("Anthropic", catalogmodel.ConnectorAnthropic, "https://api.anthropic.com")
	service := NewCatalogService(
		&providerRepo{items: map[uuid.UUID]*catalogmodel.Provider{provider.ID: provider}}, &credentialRepo{},
		&deploymentRepo{}, &aliasRepo{}, &targetRepo{}, &organizationRepo{}, &projectRepo{},
		&revisionRepo{}, &testTransactions{}, &recordingCipher{}, nil,
	)
	result, err := service.CreateDeployment(context.Background(), dto.CreateDeployment{
		ProviderID: provider.ID, Name: "claude", UpstreamModel: "claude-sonnet",
		UpstreamProtocol: catalogmodel.UpstreamAnthropicMessages,
		Scope: catalogmodel.Scope{Kind: catalogmodel.ScopePlatform},
		Capabilities: catalogmodel.CapabilitySet{Text: true, Streaming: true},
	})
	if err != nil || result.Deployment.UpstreamProtocol != catalogmodel.UpstreamAnthropicMessages {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
```

在 `compiler_test.go` 增加：

```go
func TestCompilerPublishesAnthropicProvider(t *testing.T) {
	source := validOpenAISourceConfig(t)
	source.Providers[0].Name = "Anthropic"
	source.Providers[0].ConnectorType = catalogmodel.ConnectorAnthropic
	source.Providers[0].BaseURL = "https://api.anthropic.com"
	source.Deployments[0].UpstreamProtocol = catalogmodel.UpstreamAnthropicMessages

	compiled, err := NewCompiler().Compile(source, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	session := Session{snapshot: compiled}
	access, err := session.Authenticate(snapshotTestVirtualKey, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := session.Resolve(access.ProjectID, "assistant")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := session.Provider(plan.Deployment.ProviderID)
	if err != nil || provider.ConnectorType != catalogmodel.ConnectorAnthropic ||
		plan.Deployment.UpstreamProtocol != catalogmodel.UpstreamAnthropicMessages {
		t.Fatalf("provider=%+v plan=%+v err=%v", provider, plan, err)
	}
}
```

把 `TestProviderAndDeploymentMappersPreserveConnectorConfiguration` 改成表驱动，并加入：

```go
{
	name: "anthropic", connectorType: catalogmodel.ConnectorAnthropic,
	baseURL: "https://api.anthropic.com/", protocol: catalogmodel.UpstreamAnthropicMessages,
}
```

在 `handler_test.go` 增加 Anthropic Provider 与 Deployment 请求，断言响应分别包含：

```json
{"connector_type":"anthropic","base_url":"https://api.anthropic.com"}
```

```json
{"upstream_protocol":"anthropic_messages"}
```

- [ ] **Step 3: 运行测试并确认编译失败**

Run:

```bash
go test -count=1 ./internal/domain/catalog/model ./internal/application/controlplane/service ./internal/application/gateway/snapshot ./internal/infrastructure/persistence/repository/catalog ./internal/interfaces/http/handler/controlplane
```

Expected: FAIL，提示 `ConnectorAnthropic` 和 `UpstreamAnthropicMessages` 未定义。

- [ ] **Step 4: 实现新值与协议矩阵**

在 `provider.go` 增加常量，并扩展现有分支：

```go
const (
	ConnectorFake             = "fake"
	ConnectorOpenAI           = "openai"
	ConnectorOpenAICompatible = "openai_compatible"
	ConnectorAnthropic        = "anthropic"
)

// Validate:
case ConnectorOpenAI, ConnectorOpenAICompatible, ConnectorAnthropic:
	normalized, err := normalizeBaseURL(p.ConnectorType, p.BaseURL)
	if err != nil {
		return err
	}
	if normalized != p.BaseURL {
		return fmt.Errorf("%w: provider base URL must be normalized", sharederrors.ErrInvalid)
	}

// Supports:
case ConnectorAnthropic:
	return protocol == UpstreamAnthropicMessages
```

在 `upstream_protocol.go` 增加：

```go
const UpstreamAnthropicMessages UpstreamProtocol = "anthropic_messages"

func (p UpstreamProtocol) Validate() error {
	switch p {
	case UpstreamResponses, UpstreamChatCompletions, UpstreamAnthropicMessages, UpstreamFake:
		return nil
	default:
		return fmt.Errorf("%w: unsupported upstream protocol %q", sharederrors.ErrInvalid, p)
	}
}
```

不要修改 Entity、Migration 或 Repository Mapper；它们已经逐字段保存字符串。

- [ ] **Step 5: 格式化并运行目标测试**

Run:

```bash
gofmt -w internal/domain/catalog/model internal/application/controlplane/service internal/application/gateway/snapshot internal/infrastructure/persistence/repository/catalog internal/interfaces/http/handler/controlplane
go test -count=1 ./internal/domain/catalog/model ./internal/application/controlplane/service ./internal/application/gateway/snapshot ./internal/infrastructure/persistence/repository/catalog ./internal/interfaces/http/handler/controlplane
```

Expected: PASS；未配置 `LLM_PROXY_TEST_POSTGRES_DSN` 时只有 PostgreSQL 用例明确 Skip。

- [ ] **Step 6: 提交目录契约**

```bash
git add internal/domain/catalog/model internal/application/controlplane/service/service_test.go internal/application/gateway/snapshot/compiler_test.go internal/infrastructure/persistence/repository/catalog/repository_test.go internal/interfaces/http/handler/controlplane/handler_test.go
git commit -m "feat: add anthropic catalog protocol"
```

---

### Task 2: 实现 Anthropic 凭据打开与错误助手

**Files:**
- Create: `internal/infrastructure/connector/anthropic/credential.go`
- Create: `internal/infrastructure/connector/anthropic/credential_test.go`
- Create: `internal/infrastructure/connector/anthropic/errors.go`
- Create: `internal/infrastructure/connector/anthropic/test_helpers_test.go`

- [ ] **Step 1: 写凭据严格解析与信封参数失败测试**

```go
func TestOpenAPIKeyAcceptsOnlyOneNonEmptyField(t *testing.T) {
	envelope := credentialEnvelope(t)
	for _, test := range []struct {
		name, payload string
		want         string
		wantError    bool
	}{
		{name: "valid", payload: `{"api_key":"anthropic-secret"}`, want: "anthropic-secret"},
		{name: "empty", payload: `{"api_key":""}`, wantError: true},
		{name: "unknown", payload: `{"api_key":"x","header":"y"}`, wantError: true},
		{name: "trailing", payload: `{"api_key":"x"}{}`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			opener := &recordingOpener{plaintext: []byte(test.payload)}
			key, err := openAPIKey(context.Background(), opener, envelope)
			if test.wantError {
				assertConnectorErrorKind(t, err, gatewayport.CredentialUnavailable, "")
				return
			}
			if err != nil || string(key) != test.want {
				t.Fatalf("key=%q err=%v", key, err)
			}
			if opener.credentialID != envelope.CredentialID || opener.providerID != envelope.ProviderID ||
				opener.scope != envelope.Scope {
				t.Fatalf("opener=%+v", opener)
			}
			clear(key)
		})
	}
}
```

`test_helpers_test.go` 先固定后续测试会复用的边界，避免各测试自行构造不一致的数据：

```go
var (
	fixedResponseID = uuid.Must(uuid.NewV7())
	fixedNow        = time.Date(2026, 9, 2, 10, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
)

type recordingOpener struct {
	plaintext    []byte
	err          error
	credentialID uuid.UUID
	providerID   uuid.UUID
	scope        catalogmodel.Scope
}

func (o *recordingOpener) Open(
	_ context.Context,
	credentialID, providerID uuid.UUID,
	scope catalogmodel.Scope,
	_ catalogmodel.SealedCredential,
) ([]byte, error) {
	o.credentialID, o.providerID, o.scope = credentialID, providerID, scope
	return o.plaintext, o.err
}

func credentialEnvelope(t *testing.T) gatewaysnapshot.CredentialEnvelope {
	t.Helper()
	return gatewaysnapshot.CredentialEnvelope{
		CredentialID: uuid.Must(uuid.NewV7()),
		ProviderID:   uuid.Must(uuid.NewV7()),
		Scope:        catalogmodel.Scope{Kind: catalogmodel.ScopePlatform},
		Sealed: catalogmodel.SealedCredential{
			KeyVersion: "v1", WrappedKeyNonce: []byte{1}, WrappedDataKey: []byte{2},
			PayloadNonce: []byte{3}, Ciphertext: []byte{4},
		},
	}
}

func fullInvocation(t *testing.T) gatewayport.Invocation {
	t.Helper()
	providerID := uuid.Must(uuid.NewV7())
	credential := credentialEnvelope(t)
	credential.ProviderID = providerID
	return gatewayport.Invocation{
		Request: inference.Request{
			Model: "assistant",
			Messages: []inference.Message{{
				Role: inference.RoleUser,
				Content: []inference.ContentBlock{textBlock("hello")},
			}},
			MaxTokens: inference.Some[int64](128),
		},
		Provider: gatewaysnapshot.Provider{
			ID: providerID, ConnectorType: catalogmodel.ConnectorAnthropic,
			BaseURL: "https://api.anthropic.example",
		},
		Deployment: gatewaysnapshot.Deployment{
			ID: uuid.Must(uuid.NewV7()), ProviderID: providerID,
			UpstreamModel: "claude-upstream", UpstreamProtocol: catalogmodel.UpstreamAnthropicMessages,
			Capabilities: catalogmodel.CapabilitySet{Text: true, ImageInput: true, Tools: true, StructuredOutput: true, Streaming: true},
		},
		Credential: &credential,
		Revision:   1,
	}
}

func textBlock(value string) inference.ContentBlock {
	return inference.ContentBlock{Type: inference.ContentText, Text: &inference.TextContent{Text: value}}
}

func fixedIDGenerator() (uuid.UUID, error) { return fixedResponseID, nil }
func fixedClock() time.Time                { return fixedNow }

func assertConnectorErrorKind(t *testing.T, err error, kind gatewayport.ConnectorErrorKind, param string) {
	t.Helper()
	var connectorErr *gatewayport.ConnectorError
	if !errors.As(err, &connectorErr) || connectorErr.Kind != kind || connectorErr.Param != param {
		t.Fatalf("error=%v kind=%q param=%q", err, kind, param)
	}
}

func collectEvents(t *testing.T, stream interface {
	Recv(context.Context) (inference.Event, error)
	Close() error
}) []inference.Event {
	t.Helper()
	defer stream.Close()
	var events []inference.Event
	for {
		event, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			return events
		}
		if err != nil {
			t.Fatalf("receive event: %v", err)
		}
		events = append(events, event)
	}
}

func lastUsage(t *testing.T, events []inference.Event) inference.Usage {
	t.Helper()
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type == inference.EventUsageUpdate {
			return events[index].UsageUpdate.Usage
		}
	}
	t.Fatal("usage update not found")
	return inference.Usage{}
}
```

测试数据只能使用 canary secret；`recordingOpener` 返回的明文字节也必须在凭据测试中断言已被清零。

- [ ] **Step 2: 运行测试并确认失败**

Run: `go test -count=1 ./internal/infrastructure/connector/anthropic`

Expected: FAIL，包或 `openAPIKey` 尚不存在。

- [ ] **Step 3: 实现 CredentialOpener、错误助手和严格解析**

```go
type CredentialOpener interface {
	Open(context.Context, uuid.UUID, uuid.UUID, catalogmodel.Scope, catalogmodel.SealedCredential) ([]byte, error)
}

func openAPIKey(ctx context.Context, opener CredentialOpener, envelope gatewaysnapshot.CredentialEnvelope) ([]byte, error) {
	if opener == nil {
		return nil, connectorError(gatewayport.CredentialUnavailable, fmt.Errorf("credential opener is nil"))
	}
	plaintext, err := opener.Open(ctx, envelope.CredentialID, envelope.ProviderID, envelope.Scope, envelope.Sealed)
	if err != nil {
		clear(plaintext)
		return nil, connectorError(gatewayport.CredentialUnavailable, err)
	}
	defer clear(plaintext)
	var value struct {
		APIKey string `json:"api_key"`
	}
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil || strings.TrimSpace(value.APIKey) == "" {
		return nil, connectorError(gatewayport.CredentialUnavailable, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, connectorError(gatewayport.CredentialUnavailable, fmt.Errorf("credential must contain one JSON value"))
	}
	key := append([]byte(nil), value.APIKey...)
	value.APIKey = ""
	return key, nil
}
```

`errors.go` 定义：

```go
func connectorError(kind gatewayport.ConnectorErrorKind, cause error) *gatewayport.ConnectorError {
	return &gatewayport.ConnectorError{Kind: kind, Cause: cause}
}

func parameterUnsupported(param string, cause error) *gatewayport.ConnectorError {
	return &gatewayport.ConnectorError{Kind: gatewayport.ParameterUnsupported, Param: param, Cause: cause}
}

func invalidResponseError(cause error) *gatewayport.ConnectorError {
	return connectorError(gatewayport.UpstreamInvalidResponse, cause)
}
```

- [ ] **Step 4: 格式化并运行测试**

Run:

```bash
gofmt -w internal/infrastructure/connector/anthropic
go test -count=1 ./internal/infrastructure/connector/anthropic
```

Expected: PASS。

- [ ] **Step 5: 提交凭据边界**

```bash
git add internal/infrastructure/connector/anthropic
git commit -m "feat: open anthropic provider credentials"
```

---

### Task 3: 实现 Anthropic Messages 请求适配器

**Files:**
- Create: `internal/infrastructure/connector/anthropic/request.go`
- Create: `internal/infrastructure/connector/anthropic/request_test.go`

- [ ] **Step 1: 写完整请求映射失败测试**

构造包含前导 System/Developer、User 的 Base64/URL 两种图片、Assistant ToolCall、User ToolResult、工具、Strict、Specific ToolChoice、JSON Schema、Temperature、TopP、MaxTokens、Stop 和 Stream 的 Invocation，并断言图片 Source 数据完整、URL/Base64 类型正确且请求中不出现统一模型的 `detail` 字段：

```go
payload, err := encodeRequest(invocation)
if err != nil {
	t.Fatal(err)
}
var got messageRequest
if err := json.Unmarshal(payload, &got); err != nil {
	t.Fatal(err)
}
if got.Model != "claude-upstream" || got.MaxTokens != 128 || !got.Stream || len(got.System) != 2 {
	t.Fatalf("request=%+v", got)
}
if len(got.Tools) != 1 || !got.Tools[0].Strict ||
	got.ToolChoice.Type != "tool" || got.ToolChoice.Name != "weather" {
	t.Fatalf("tools=%+v choice=%+v", got.Tools, got.ToolChoice)
}
if got.OutputConfig == nil || got.OutputConfig.Format.Type != "json_schema" {
	t.Fatalf("output_config=%+v", got.OutputConfig)
}
```

- [ ] **Step 2: 写前置拒绝失败测试**

```go
tests := []struct {
	name, param string
	mutate      func(*gatewayport.Invocation)
}{
	{name: "missing max", param: "max_tokens", mutate: func(v *gatewayport.Invocation) {
		v.Request.MaxTokens = inference.Optional[int64]{}
	}},
	{name: "zero max", param: "max_tokens", mutate: func(v *gatewayport.Invocation) {
		v.Request.MaxTokens = inference.Some[int64](0)
	}},
	{name: "temperature above one", param: "temperature", mutate: func(v *gatewayport.Invocation) {
		v.Request.Temperature = inference.Some(1.1)
	}},
	{name: "late developer", param: "messages", mutate: func(v *gatewayport.Invocation) {
		v.Request.Messages = append(v.Request.Messages, inference.Message{
			Role: inference.RoleDeveloper, Content: []inference.ContentBlock{textBlock("late")},
		})
	}},
	{name: "late tool result", param: "messages", mutate: func(v *gatewayport.Invocation) {
		result := "sunny"
		v.Request.Messages = []inference.Message{
			{Role: inference.RoleAssistant, Content: []inference.ContentBlock{{
				Type: inference.ContentToolCall,
				ToolCall: &inference.ToolCallContent{ID: "call_one", Name: "weather", Arguments: json.RawMessage(`{}`)},
			}}},
			{Role: inference.RoleUser, Content: []inference.ContentBlock{
				textBlock("content before tool result"),
				{Type: inference.ContentToolResult, ToolResult: &inference.ToolResultContent{ToolCallID: "call_one", Text: &result}},
			}},
		}
	}},
	{name: "json object", param: "response_format", mutate: func(v *gatewayport.Invocation) {
		v.Request.StructuredOutput = &inference.StructuredOutput{Type: inference.StructuredJSONObject}
	}},
}
```

每个用例调用 `encodeRequest` 并使用 `assertConnectorErrorKind` 验证 `ParameterUnsupported` 和 Param。

- [ ] **Step 3: 运行测试并确认失败**

Run: `go test -count=1 ./internal/infrastructure/connector/anthropic -run 'TestEncodeRequest'`

Expected: FAIL，`encodeRequest` 和 DTO 尚不存在。

- [ ] **Step 4: 定义请求 DTO 与编码入口**

```go
type messageRequest struct {
	Model         string               `json:"model"`
	MaxTokens     int64                `json:"max_tokens"`
	System        []requestContent     `json:"system,omitempty"`
	Messages      []requestMessage     `json:"messages"`
	Tools         []requestTool        `json:"tools,omitempty"`
	ToolChoice    *requestToolChoice   `json:"tool_choice,omitempty"`
	OutputConfig  *requestOutputConfig `json:"output_config,omitempty"`
	Temperature   *float64             `json:"temperature,omitempty"`
	TopP          *float64             `json:"top_p,omitempty"`
	StopSequences *[]string            `json:"stop_sequences,omitempty"`
	Stream        bool                 `json:"stream"`
}

type requestContent struct {
	Type      string              `json:"type"`
	Text      string              `json:"text,omitempty"`
	Source    *requestImageSource `json:"source,omitempty"`
	ID        string              `json:"id,omitempty"`
	Name      string              `json:"name,omitempty"`
	Input     json.RawMessage     `json:"input,omitempty"`
	ToolUseID string              `json:"tool_use_id,omitempty"`
	Content   any                 `json:"content,omitempty"`
	IsError   bool                `json:"is_error,omitempty"`
}
```

前置校验：

```go
if err := invocation.Request.Validate(); err != nil {
	return nil, fmt.Errorf("validate Anthropic request: %w", err)
}
if invocation.Deployment.UpstreamProtocol != catalogmodel.UpstreamAnthropicMessages {
	return nil, fmt.Errorf("deployment does not match Anthropic Messages provider")
}
if !invocation.Request.MaxTokens.Set || invocation.Request.MaxTokens.Value <= 0 {
	return nil, parameterUnsupported("max_tokens", fmt.Errorf("Anthropic Messages requires max_tokens greater than zero"))
}
if invocation.Request.Temperature.Set && invocation.Request.Temperature.Value > 1 {
	return nil, parameterUnsupported("temperature", fmt.Errorf("Anthropic temperature must not exceed one"))
}
```

- [ ] **Step 5: 实现消息、工具和结构化输出映射**

```go
seenConversation := false
for _, message := range invocation.Request.Messages {
	if message.Role == inference.RoleSystem || message.Role == inference.RoleDeveloper {
		if seenConversation {
			return nil, parameterUnsupported("messages", fmt.Errorf("system and developer messages must precede conversation messages"))
		}
		for _, block := range message.Content {
			request.System = append(request.System, requestContent{Type: "text", Text: block.Text.Text})
		}
		continue
	}
	seenConversation = true
	mapped, err := encodeMessage(message)
	if err != nil {
		return nil, err
	}
	request.Messages = append(request.Messages, mapped)
}
```

`encodeMessage` 对 ToolResult 维护 `seenRegular`；ToolResult 在 Text/Image 后出现时返回 `ParameterUnsupported("messages")`。JSON ToolResult 使用 `json.Compact` 后作为字符串发送。

ToolChoice 映射固定为：`auto -> {type:"auto"}`、`required -> {type:"any"}`、`specific -> {type:"tool",name:...}`；`none` 不发送 `tools` 和 `tool_choice`，不能生成 Anthropic 不支持的 `{type:"none"}`。Tool.Strict 映射为 Anthropic `strict`。结构化输出：

```go
switch output.Type {
case inference.StructuredJSONSchema:
	request.OutputConfig = &requestOutputConfig{Format: requestOutputFormat{
		Type: "json_schema", Schema: output.Schema,
	}}
case inference.StructuredJSONObject:
	return nil, parameterUnsupported("response_format", fmt.Errorf("Anthropic requires a JSON schema"))
}
```

- [ ] **Step 6: 格式化并运行请求测试**

Run:

```bash
gofmt -w internal/infrastructure/connector/anthropic
go test -count=1 ./internal/infrastructure/connector/anthropic -run 'TestEncodeRequest'
```

Expected: PASS。

- [ ] **Step 7: 提交请求适配器**

```bash
git add internal/infrastructure/connector/anthropic/request.go internal/infrastructure/connector/anthropic/request_test.go
git commit -m "feat: encode anthropic messages requests"
```

---

### Task 4: 实现 Anthropic 普通响应适配器

**Files:**
- Create: `internal/infrastructure/connector/anthropic/response.go`
- Create: `internal/infrastructure/connector/anthropic/response_test.go`
- Create: `internal/infrastructure/connector/anthropic/testdata/message_text_tool.json`
- Create: `internal/infrastructure/connector/anthropic/testdata/message_refusal.json`

- [ ] **Step 1: 写文本、工具、Usage 和停止原因失败测试**

`message_text_tool.json`：

```json
{
  "id":"msg_fixture",
  "type":"message",
  "role":"assistant",
  "model":"claude-upstream",
  "content":[
    {"type":"text","text":"hello"},
    {"type":"tool_use","id":"call_one","name":"weather","input":{"city":"Shanghai"}}
  ],
  "stop_reason":"tool_use",
  "stop_details":null,
  "usage":{"input_tokens":11,"output_tokens":7,"cache_read_input_tokens":3,"cache_creation_input_tokens":2}
}
```

```go
fixture, err := os.ReadFile("testdata/message_text_tool.json")
if err != nil {
	t.Fatal(err)
}
response, err := decodeResponse(bytes.NewReader(fixture), invocation, fixedIDGenerator, fixedClock)
if err != nil {
	t.Fatal(err)
}
if response.ID != fixedResponseID || response.Model != invocation.Request.Model ||
	response.StopReason != inference.StopToolUse {
	t.Fatalf("response=%+v", response)
}
if response.Content[1].ToolCall.ID != "call_one" ||
	response.Usage.InputTokens != 11 || response.Usage.CacheWriteInputTokens != 2 {
	t.Fatalf("response=%+v", response)
}
```

表驱动覆盖 `end_turn`、`max_tokens`、`stop_sequence`、`tool_use`、`refusal`、`model_context_window_exceeded`、`pause_turn` 和未知值。

- [ ] **Step 2: 写 Refusal、Thinking 和泄露失败测试**

`message_refusal.json` 使用空 Content 与 `stop_details.explanation="无法处理该请求"`：

```go
if response.StopReason != inference.StopContentFilter || len(response.Content) != 1 ||
	response.Content[0].Refusal.Text != "无法处理该请求" {
	t.Fatalf("response=%+v", response)
}
```

另构造包含 `thinking`、`redacted_thinking` 和未知块的响应，断言 `UpstreamInvalidResponse`，并断言 Error/Cause 不含 Thinking 正文、Signature、BaseURL 和上游模型。

- [ ] **Step 3: 运行测试并确认失败**

Run: `go test -count=1 ./internal/infrastructure/connector/anthropic -run 'TestDecodeResponse|TestMapStopReason'`

Expected: FAIL，`decodeResponse` 尚不存在。

- [ ] **Step 4: 实现普通响应映射**

```go
func decodeResponse(
	reader io.Reader,
	invocation gatewayport.Invocation,
	idGenerator func() (uuid.UUID, error),
	clock func() time.Time,
) (inference.Response, error)
```

停止原因：

```go
func mapStopReason(value string) (inference.StopReason, error) {
	switch value {
	case "end_turn":
		return inference.StopEndTurn, nil
	case "max_tokens", "model_context_window_exceeded":
		return inference.StopMaxTokens, nil
	case "stop_sequence":
		return inference.StopSequence, nil
	case "tool_use":
		return inference.StopToolUse, nil
	case "refusal":
		return inference.StopContentFilter, nil
	case "pause_turn":
		return "", invalidResponseError(fmt.Errorf("unsupported Anthropic stop reason %q", value))
	default:
		return "", invalidResponseError(fmt.Errorf("unknown Anthropic stop reason %q", value))
	}
}
```

Refusal：

```go
if source.StopReason == "refusal" {
	content = nil
	if explanation := strings.TrimSpace(source.StopDetails.Explanation); explanation != "" {
		content = append(content, inference.ContentBlock{
			Type: inference.ContentRefusal,
			Refusal: &inference.RefusalContent{Text: explanation},
		})
	}
}
```

解码时还要验证顶层 `type == "message"`、`role == "assistant"`、非空上游 ID/Model 和非负 Usage。未知块的 Cause 只写 Type 与 Index，不能格式化整个 DTO；不把上游 ID/Model 写入统一 Response 或错误。最终调用 `response.Validate()`。

- [ ] **Step 5: 格式化并运行响应测试**

Run:

```bash
gofmt -w internal/infrastructure/connector/anthropic
go test -count=1 ./internal/infrastructure/connector/anthropic -run 'TestDecodeResponse|TestMapStopReason'
```

Expected: PASS。

- [ ] **Step 6: 提交普通响应适配器**

```bash
git add internal/infrastructure/connector/anthropic/response.go internal/infrastructure/connector/anthropic/response_test.go internal/infrastructure/connector/anthropic/testdata
git commit -m "feat: decode anthropic messages responses"
```

---

### Task 5: 实现有边界的 Anthropic SSE Reader

**Files:**
- Create: `internal/infrastructure/connector/anthropic/sse.go`
- Create: `internal/infrastructure/connector/anthropic/sse_test.go`

- [ ] **Step 1: 写网络分块、CRLF、多 Data、尾事件失败测试**

```go
func TestSSEReaderHandlesCRLFMultipleDataChunksAndTailEvent(t *testing.T) {
	reader := newSSEReader(io.NopCloser(&chunkReader{chunks: []string{
		"event: message_start\r\n", "data: {\"type\":", "\"message_start\"}\r\n",
		"data: second\r\n\r\n", "event: message_stop\ndata: {\"type\":\"message_stop\"}",
	}}), time.Second, 2<<20)
	t.Cleanup(func() { _ = reader.Close() })

	event, err := reader.Next(context.Background())
	if err != nil || event.Event != "message_start" ||
		string(event.Data) != "{\"type\":\"message_start\"}\nsecond" {
		t.Fatalf("event=%+v err=%v", event, err)
	}
	event, err = reader.Next(context.Background())
	if err != nil || event.Event != "message_stop" {
		t.Fatalf("event=%+v err=%v", event, err)
	}
}
```

测试文件加入可重复返回指定网络分块的 Reader：

```go
type chunkReader struct {
	chunks  []string
	pending []byte
}

func (r *chunkReader) Read(buffer []byte) (int, error) {
	if len(r.pending) == 0 {
		if len(r.chunks) == 0 {
			return 0, io.EOF
		}
		r.pending = []byte(r.chunks[0])
		r.chunks = r.chunks[1:]
	}
	written := copy(buffer, r.pending)
	r.pending = r.pending[written:]
	return written, nil
}
```

再写 32 字节单事件上限、20ms 空闲超时、Context Cancel、Close 解除阻塞读取和无尾随空行。

- [ ] **Step 2: 运行测试并确认失败**

Run: `go test -count=1 ./internal/infrastructure/connector/anthropic -run 'TestSSEReader'`

Expected: FAIL，`newSSEReader` 尚不存在。

- [ ] **Step 3: 实现 Reader 生命周期**

```go
type sseEvent struct {
	Event string
	Data  []byte
}

type sseReader struct {
	source    io.ReadCloser
	idle      time.Duration
	maxEvent  int
	chunks    chan readChunk
	done      chan struct{}
	pending   []byte
	terminal  error
	closeOnce sync.Once
	closeErr  error
}
```

`Next` 必须忽略注释、拼接多个 `data:` 行、处理 CRLF、限制完整事件、EOF 时返回尾事件。空闲超时返回 `UpstreamTimeout`，超限/非法读取返回 `UpstreamInvalidResponse`；`Close` 关闭源并使 Pump 退出。

- [ ] **Step 4: 运行 Race 测试**

Run:

```bash
gofmt -w internal/infrastructure/connector/anthropic
go test -race -count=1 ./internal/infrastructure/connector/anthropic -run 'TestSSEReader'
```

Expected: PASS，无 Race 和 Goroutine 卡住。

- [ ] **Step 5: 提交 SSE Reader**

```bash
git add internal/infrastructure/connector/anthropic/sse.go internal/infrastructure/connector/anthropic/sse_test.go
git commit -m "feat: parse bounded anthropic sse"
```

---

### Task 6: 实现 Anthropic 流式状态机

**Files:**
- Create: `internal/infrastructure/connector/anthropic/stream.go`
- Create: `internal/infrastructure/connector/anthropic/stream_test.go`
- Create: `internal/infrastructure/connector/anthropic/testdata/message_tool_stream.sse`
- Create: `internal/infrastructure/connector/anthropic/testdata/message_refusal_stream.sse`

- [ ] **Step 1: 写文本、工具、Usage 和终止序列失败测试**

夹具包含 `message_start`、`ping`、Text Block、ToolUse Block、分段 `input_json_delta`、`message_delta` 和 `message_stop`。`message_delta.usage` 只给 `output_tokens`：

```go
stream, err := newMessagesStream(reader, invocation, fixedIDGenerator, fixedClock)
if err != nil {
	t.Fatal(err)
}
events := collectEvents(t, stream)
if err := inference.ValidateEventSequence(events); err != nil {
	t.Fatal(err)
}
usage := lastUsage(t, events)
if usage.InputTokens != 11 || usage.OutputTokens != 7 ||
	usage.CacheReadInputTokens != 3 || usage.CacheWriteInputTokens != 2 {
	t.Fatalf("usage=%+v", usage)
}
```

- [ ] **Step 2: 写未知事件、未知内容和 Refusal 失败测试**

顶层未知事件被忽略，后续标准事件完成：

```text
event: future_event
data: {"type":"future_event","opaque":"not-logged"}
```

已知内容事件里的 Thinking 安全失败，Cause 不含正文和 Signature：

```text
event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"private","signature":"secret"}}
```

Refusal 夹具先发送部分 Text，再在 `message_delta` 发送 `stop_reason=refusal` 和 Explanation。断言顺序为：Text Stop -> Refusal Start/Delta/Stop -> Usage -> ResponseFinish(ContentFilter)，并断言已发 Text 仍存在。

再增加两个契约测试：

- `event:error` 返回唯一的 `EventStreamError`，消息固定为 `供应商流式响应失败`，下一次 `Recv` 返回 EOF；事件原始正文不能进入安全消息或 Cause。
- 使用 `io.Pipe` 只写完 `message_start` 后暂停上游，断言 250ms 内收到 `ResponseStart`，随后才释放并写完剩余事件，证明 Connector 不缓冲完整 SSE。

- [ ] **Step 3: 运行测试并确认失败**

Run: `go test -count=1 ./internal/infrastructure/connector/anthropic -run 'TestMessagesStream'`

Expected: FAIL，`newMessagesStream` 尚不存在。

- [ ] **Step 4: 实现字段存在性感知的 Usage**

```go
type streamUsage struct {
	InputTokens              *int64 `json:"input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
}

func mergeUsage(current inference.Usage, delta streamUsage) (inference.Usage, error) {
	next := current
	if delta.InputTokens != nil {
		next.InputTokens = *delta.InputTokens
	}
	if delta.OutputTokens != nil {
		next.OutputTokens = *delta.OutputTokens
	}
	if delta.CacheReadInputTokens != nil {
		next.CacheReadInputTokens = *delta.CacheReadInputTokens
	}
	if delta.CacheCreationInputTokens != nil {
		next.CacheWriteInputTokens = *delta.CacheCreationInputTokens
	}
	if err := next.Validate(); err != nil {
		return inference.Usage{}, invalidResponseError(err)
	}
	if next.InputTokens < current.InputTokens || next.OutputTokens < current.OutputTokens ||
		next.CacheReadInputTokens < current.CacheReadInputTokens ||
		next.CacheWriteInputTokens < current.CacheWriteInputTokens {
		return inference.Usage{}, invalidResponseError(fmt.Errorf("Anthropic usage decreased"))
	}
	return next, nil
}
```

- [ ] **Step 5: 实现事件映射和 Refusal 排队**

`messagesStream.Recv` 从 Reader 拉取事件；`message_start` 立即排 `ResponseStart` 和首个 presence-aware `UsageUpdate`；`error` 排安全 `StreamError` 并进入终止状态；未知顶层事件继续读取，标准事件放入 `queue []inference.Event`。`message_delta` 必须先处理 Refusal Block，再排 Usage：

```go
s.stopReason, err = mapStopReason(event.Delta.StopReason)
if err != nil {
	return inference.Event{}, err
}
s.usage, err = mergeUsage(s.usage, event.Usage)
if err != nil {
	return inference.Event{}, err
}
if s.stopReason == inference.StopContentFilter &&
	strings.TrimSpace(event.StopDetails.Explanation) != "" {
	index := s.nextSyntheticIndex()
	s.queue = append(s.queue,
		inference.NewContentBlockStart(index, inference.ContentRefusal),
		inference.NewRefusalDelta(index, event.StopDetails.Explanation),
		inference.NewContentBlockStop(index),
	)
}
s.queue = append(s.queue, inference.NewUsageUpdate(s.usage))
```

每个已知事件都校验 SSE `event:` 名称与 JSON `type` 一致；`message_start.message.role` 必须为 `assistant`，但上游 ID/Model 只用于结构校验，不进入统一 Event。`message_stop` 只有在已经看到 StopReason、没有活动块时才产生 `NewResponseFinish`。EOF 前未完成、重复 Index、跨类型 Delta、非法工具 JSON、未知内容块和未知 Delta 都返回 `UpstreamInvalidResponse`。

- [ ] **Step 6: 运行流式 Race 测试**

Run:

```bash
gofmt -w internal/infrastructure/connector/anthropic
go test -race -count=1 ./internal/infrastructure/connector/anthropic -run 'TestMessagesStream|TestSSEReader'
```

Expected: PASS。

- [ ] **Step 7: 提交流式适配器**

```bash
git add internal/infrastructure/connector/anthropic/stream.go internal/infrastructure/connector/anthropic/stream_test.go internal/infrastructure/connector/anthropic/testdata
git commit -m "feat: adapt anthropic message streams"
```

---

### Task 7: 实现 HTTP Client、错误分类和 Connector 编排

**Files:**
- Create: `internal/infrastructure/connector/anthropic/client.go`
- Modify: `internal/infrastructure/connector/anthropic/errors.go`
- Create: `internal/infrastructure/connector/anthropic/connector.go`
- Create: `internal/infrastructure/connector/anthropic/connector_test.go`
- Create: `internal/infrastructure/connector/anthropic/integration_test.go`

- [ ] **Step 1: 写构造、Header、路径和重定向失败测试**

```go
func TestConnectorCallsMessagesWithFixedHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/prefix/v1/messages" {
			t.Fatalf("method=%s path=%s", r.Method, r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "anthropic-secret" ||
			r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Fatalf("headers=%v", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		payload, err := os.ReadFile("testdata/message_text_tool.json")
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()

	connector := newConnectorForClient(t, upstream.Client(), time.Second)
	invocation := fullInvocation(t)
	invocation.Provider.BaseURL = upstream.URL + "/prefix"
	response, err := connector.Complete(context.Background(), invocation)
	if err != nil || response.Model != invocation.Request.Model {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}
```

同一测试文件明确加入构造 Helper；每次构造独立 Opener，避免测试间复用已清零的明文：

```go
func newConnectorForClient(t *testing.T, client *http.Client, timeout time.Duration) *Connector {
	t.Helper()
	connector, err := New(Options{
		Client:            client,
		CredentialOpener:  &recordingOpener{plaintext: []byte(`{"api_key":"anthropic-secret"}`)},
		CompleteTimeout:   timeout,
		StreamIdleTimeout: timeout,
		IDGenerator:       fixedIDGenerator,
		Clock:             fixedClock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return connector
}
```

构造测试覆盖 nil Client、nil CredentialOpener、非正 CompleteTimeout/StreamIdleTimeout，并断言 `CheckRedirect` 已设置。

- [ ] **Step 2: 写错误分类、Body 上限、取消和泄露失败测试**

表驱动状态映射：401/403 Authentication，408/504 Timeout，429 RateLimited，500/502/503/529 Unavailable，其他 4xx RequestRejected，3xx 因禁止重定向归 Unavailable。

每个错误服务返回 `private upstream detail`，并给 Invocation 放入 canary BaseURL、UpstreamModel、CredentialID、ProviderID 和 DeploymentID；断言 Error、Cause 和租户可见 SafeMessage 不含正文、API Key、URL、模型或任一资源 ID。另测 DNS/TLS、Complete Deadline、Context Cancel、16 MiB 成功体上限、64 KiB 错误体读取上限和 nil Body。

- [ ] **Step 3: 运行测试并确认失败**

Run: `go test -count=1 ./internal/infrastructure/connector/anthropic -run 'TestConnector|TestClassify|TestNewUpstreamRequest'`

Expected: FAIL，Connector 和 HTTP Client 尚不存在。

- [ ] **Step 4: 实现安全请求与错误分类**

```go
const (
	apiVersion          = "2023-06-01"
	maxSuccessBodyBytes = 16 << 20
	maxErrorBodyBytes   = 64 << 10
)

func newUpstreamRequest(
	ctx context.Context,
	provider gatewaysnapshot.Provider,
	body, apiKey []byte,
	stream bool,
) (*http.Request, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(provider.BaseURL) == "" {
		return nil, fmt.Errorf("Anthropic provider base URL is required")
	}
	if len(apiKey) == 0 {
		return nil, connectorError(gatewayport.CredentialUnavailable, fmt.Errorf("Anthropic API key is empty"))
	}
	joined, err := url.JoinPath(provider.BaseURL, "v1", "messages")
	if err != nil {
		return nil, fmt.Errorf("join provider URL: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, joined, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create Anthropic request: %w", err)
	}
	request.Host = request.URL.Host
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-api-key", string(apiKey))
	request.Header.Set("anthropic-version", apiVersion)
	if stream {
		request.Header.Set("Accept", "text/event-stream")
	} else {
		request.Header.Set("Accept", "application/json")
	}
	return request, nil
}
```

`classifyHTTPStatus` 和 `classifyRequestError` 直接使用既有 `gatewayport` 枚举；错误响应正文有界读取后丢弃，不放入 Cause。

- [ ] **Step 5: 实现 Connector Complete/Stream**

```go
type Options struct {
	Client            *http.Client
	CredentialOpener  CredentialOpener
	CompleteTimeout   time.Duration
	StreamIdleTimeout time.Duration
	IDGenerator       func() (uuid.UUID, error)
	Clock             func() time.Time
}

type Connector struct {
	options Options
}

func (c *Connector) Complete(context.Context, gatewayport.Invocation) (inference.Response, error)
func (c *Connector) Stream(context.Context, gatewayport.Invocation) (inferenceport.Stream, error)
```

`validateInvocation` 固定要求 Provider Connector 为 `ConnectorAnthropic`、Deployment Protocol 为 `UpstreamAnthropicMessages`、Provider/Deployment ID 一致、Credential 非空且属于 Provider、Stream 标志与方法一致。

Complete：编码 -> `context.WithTimeout` -> 打开 Key -> HTTP -> 有界读取 -> `decodeResponse`。

Stream：编码 -> 可取消 Context -> 打开 Key -> HTTP -> `newSSEReader` -> `newMessagesStream` -> 返回带 Cancel/CloseOnce 的包装 Stream。任何构造失败立即关闭 Body 并取消 Context。

- [ ] **Step 6: 运行 Connector Race 测试**

Run:

```bash
gofmt -w internal/infrastructure/connector/anthropic
go test -race -count=1 ./internal/infrastructure/connector/anthropic
```

Expected: PASS。

- [ ] **Step 7: 提交完整 Connector**

```bash
git add internal/infrastructure/connector/anthropic
git commit -m "feat: invoke anthropic messages provider"
```

---

### Task 8: 接入脱敏 OTel Transport 和 DI Registry

**Files:**
- Modify: `internal/infrastructure/observability/http.go`
- Modify: `internal/infrastructure/observability/http_test.go`
- Modify: `internal/di/provider/gateway.go`
- Modify: `internal/di/provider/gateway_test.go`
- Regenerate if changed: `internal/di/wire_gen.go`

- [ ] **Step 1: 写 Anthropic 出站遥测失败测试**

```go
func TestTransportForHidesAnthropicBaseURL(t *testing.T) {
	runtime, spans, _ := newHTTPTestRuntime(t)
	transport := runtime.TransportFor("anthropic", "messages", roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://private.internal/prefix/v1/messages" {
			t.Fatalf("url=%s", request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader("{}")), Request: request,
		}, nil
	}))
	request, _ := http.NewRequest(
		http.MethodPost,
		"https://private.internal/prefix/v1/messages",
		strings.NewReader(`{"model":"private-model"}`),
	)
	request.Header.Set("x-api-key", "private-key")
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	var clientSpan sdktrace.ReadOnlySpan
	for _, span := range spans.Ended() {
		if span.SpanKind() == trace.SpanKindClient {
			clientSpan = span
			break
		}
	}
	if clientSpan == nil {
		t.Fatalf("client span not found: %d spans", len(spans.Ended()))
	}
	assertSpanDoesNotContain(t, clientSpan, "private.internal", "/prefix", "private-model", "private-key")
	var attributes strings.Builder
	for _, attr := range clientSpan.Attributes() {
		attributes.WriteString(attr.Value.Emit())
	}
	if !strings.Contains(attributes.String(), "/anthropic/messages") {
		t.Fatalf("static upstream route missing: %s", attributes.String())
	}
}
```

- [ ] **Step 2: 写 DI Registry 失败测试**

在 `gateway_test.go` 的 Connector 列表加入 `anthropic`，断言 `handlers.registry.Find(catalogmodel.ConnectorAnthropic)` 返回非 nil。

- [ ] **Step 3: 运行测试并确认失败**

Run:

```bash
go test -count=1 ./internal/infrastructure/observability ./internal/di/provider
```

Expected: FAIL，出站遥测把 Anthropic 归为 unknown，Registry 未注册 Connector。

- [ ] **Step 4: 扩展 OTel 白名单并注册 Connector**

```go
func normalizeGatewayProvider(provider string) string {
	if provider == "openai" || provider == "openai_compatible" || provider == "anthropic" {
		return provider
	}
	return "unknown"
}

func normalizeGatewayEndpoint(endpoint string) string {
	if endpoint == "responses" || endpoint == "chat.completions" || endpoint == "messages" {
		return endpoint
	}
	return "other"
}
```

在 `gateway.go` 构造：

```go
anthropicConnector, err := anthropicconnector.New(anthropicconnector.Options{
	Client: &http.Client{
		Transport: telemetry.TransportFor(catalogmodel.ConnectorAnthropic, "messages", base),
	},
	CredentialOpener:  cipherRuntime.Cipher,
	CompleteTimeout:   upstream.CompleteTimeout,
	StreamIdleTimeout: upstream.StreamIdleTimeout,
})
if err != nil {
	return nil, err
}
registry[catalogmodel.ConnectorAnthropic] = anthropicConnector
```

- [ ] **Step 5: 生成 Wire、格式化并运行 Race 测试**

Run:

```bash
gofmt -w internal/infrastructure/observability internal/di/provider
go generate ./internal/di
go test -race -count=1 ./internal/infrastructure/observability ./internal/di/provider
```

Expected: PASS；检查 `git diff -- internal/di/wire_gen.go`，只有真实组装根变化才保留生成差异。

- [ ] **Step 6: 提交 DI 与遥测**

```bash
git add internal/infrastructure/observability internal/di/provider internal/di/wire_gen.go
git commit -m "feat: wire anthropic provider connector"
```

---

### Task 9: 扩展三个客户端入口的 Anthropic 纵向矩阵

**Files:**
- Modify: `internal/interfaces/http/handler/gateway/provider_integration_test.go`

- [ ] **Step 1: 写 Anthropic 上游矩阵失败测试**

把现有矩阵扩展为：

```go
for _, upstreamProtocol := range []catalogmodel.UpstreamProtocol{
	catalogmodel.UpstreamChatCompletions,
	catalogmodel.UpstreamResponses,
	catalogmodel.UpstreamAnthropicMessages,
} {
```

所有客户端请求都提供正 MaxTokens：Chat 用 `max_completion_tokens:16`，Responses 用 `max_output_tokens:16`，Messages 用 `max_tokens:16`。

Anthropic 普通夹具：

```json
{"id":"msg_fixture","type":"message","role":"assistant","model":"claude-upstream","content":[{"type":"tool_use","id":"call_one","name":"weather","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":11,"output_tokens":7}}
```

Anthropic SSE 夹具依次发送：

```text
message_start(input=11, output=1)
content_block_start(tool_use call_one/weather)
content_block_delta(input_json_delta "{}")
content_block_stop
message_delta(stop_reason=tool_use, output=7)
message_stop
```

- [ ] **Step 2: 扩展测试组装与上游断言**

Anthropic Case 创建 `ConnectorAnthropic` Provider、`UpstreamAnthropicMessages` Deployment 和 Anthropic Connector Registry 项。`capturedUpstream` 增加 `xAPIKey` 和 `anthropicVersion`，断言：

```go
if capture.path != "/v1/messages" ||
	capture.xAPIKey != "provider-matrix-upstream-key" ||
	capture.authorization != "" ||
	capture.anthropicVersion != "2023-06-01" {
	t.Fatalf("capture=%+v", capture)
}
```

所有 Case 继续断言 Virtual Key 不进入上游 Header/Body、客户端响应 Model 为 `assistant`、工具 ID 为 `call_one`、Usage 为 11/7。

- [ ] **Step 3: 运行矩阵并确认首次失败**

Run:

```bash
go test -count=1 ./internal/interfaces/http/handler/gateway -run TestProviderConnectorCrossProtocolMatrix
```

Expected: 测试组装尚未加入 Anthropic Connector 时 FAIL；完成 Step 2 后 PASS，共 3 客户端 × 3 上游协议 × 2 模式 = 18 个子用例。

- [ ] **Step 4: 运行矩阵 Race 测试并提交**

Run:

```bash
gofmt -w internal/interfaces/http/handler/gateway/provider_integration_test.go
go test -race -count=1 ./internal/interfaces/http/handler/gateway -run TestProviderConnectorCrossProtocolMatrix
```

Expected: PASS，18/18 子用例通过。

```bash
git add internal/interfaces/http/handler/gateway/provider_integration_test.go
git commit -m "test: verify anthropic vertical protocol paths"
```

---

### Task 10: 增加 PostgreSQL 控制面纵向回归与最终验证

**Files:**
- Modify: `internal/interfaces/http/handler/gateway/integration_test.go`

- [ ] **Step 1: 写条件式 PostgreSQL Anthropic 控制面链路测试**

复用现有 `LLM_PROXY_TEST_POSTGRES_DSN`、测试 Schema、Control Plane 和 GatewayRuntime Helper，不创建第二套数据库启动逻辑。通过控制面依次创建 Organization、Project、VirtualKey、ProviderCredential、Deployment、ModelAlias 和 RouteTarget。

Provider：

```json
{"name":"Anthropic","connector_type":"anthropic","base_url":"<httptest server URL>"}
```

Credential：

```json
{"provider_id":"<uuid>","scope":{"kind":"platform"},"credential":{"api_key":"integration-upstream-key"}}
```

Deployment：

```json
{"provider_id":"<uuid>","name":"claude","upstream_model":"claude-upstream","upstream_protocol":"anthropic_messages","scope":{"kind":"platform"},"capabilities":{"text":true,"image_input":true,"tools":true,"structured_output":true,"streaming":true}}
```

等待 ConfigRevision 发布后，通过三个客户端入口各发一个普通请求和一个 SSE 请求。上游 `httptest.Server` 断言固定 Header 和 `/v1/messages`；客户端断言逻辑模型，不泄露上游模型、Key 或 BaseURL。

- [ ] **Step 2: 运行条件式 PostgreSQL 测试**

Run:

```bash
go test -count=1 ./internal/interfaces/http/handler/gateway -run 'TestIntegration.*Anthropic'
```

Expected: 配置 `LLM_PROXY_TEST_POSTGRES_DSN` 时 PASS；未配置时明确 Skip。DSN 已配置但失败时不得当作 Skip。

- [ ] **Step 3: 运行目标包 Race 回归**

Run:

```bash
go test -race -count=1 ./internal/domain/catalog/model ./internal/application/controlplane/service ./internal/application/gateway/snapshot ./internal/infrastructure/persistence/entity ./internal/infrastructure/persistence/migration ./internal/infrastructure/persistence/repository/catalog ./internal/infrastructure/connector/anthropic ./internal/infrastructure/connector/openai ./internal/infrastructure/observability ./internal/di/provider ./internal/interfaces/http/handler/controlplane ./internal/interfaces/http/handler/gateway ./internal/interfaces/http/router ./internal/infrastructure/server/http
```

Expected: PASS；只有未设置 DSN 的 PostgreSQL 用例允许明确 Skip。

- [ ] **Step 4: 运行全量、Race、Vet、Wire 和 Build**

Run:

```bash
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go generate ./internal/di
git diff --exit-code -- internal/di/wire_gen.go
go build -o /tmp/llm-proxy ./cmd/proxy
```

Expected: 所有命令退出码为 0，Wire 无生成漂移，Build 产生 `/tmp/llm-proxy`。

- [ ] **Step 5: 运行架构、安全和 Git 检查**

Run:

```bash
go test -count=1 ./internal/architecture
git diff --check v0.1-develop...HEAD
rg -n 'FOREIGN KEY|REFERENCES|constraint:|foreignKey|Preload\(|Association\(' internal/infrastructure/persistence --glob '*.go'
rg -n 'anthropic-secret|integration-upstream-key|private upstream detail' internal --glob '*.go' --glob '!**/*_test.go'
git status --short --branch
```

Expected:

- 架构测试 PASS。
- `git diff --check` 无输出。
- 外键/GORM 关联扫描无生产代码匹配。
- Canary Secret 扫描无生产代码匹配。
- 工作区只包含本任务预期变更；提交后为空。

- [ ] **Step 6: 提交 PostgreSQL 纵向测试**

```bash
git add internal/interfaces/http/handler/gateway/integration_test.go
git commit -m "test: verify anthropic postgres gateway path"
```

- [ ] **Step 7: 最终提交后重新验证**

Run:

```bash
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go generate ./internal/di
git diff --exit-code -- internal/di/wire_gen.go
go build -o /tmp/llm-proxy ./cmd/proxy
git diff --check v0.1-develop...HEAD
git status --short --branch
```

Expected: 全部退出码为 0，最终 `git status` 只显示 `## feat/anthropic-connector`，没有未提交文件。

---

## 完成定义

- `anthropic` Provider 与 `anthropic_messages` Deployment 可由平台控制面创建、持久化、编译和发布。
- 三个客户端协议都能以普通和 SSE 方式调用原生 Anthropic Messages 上游。
- Header、路径、模型、凭据、Scope、Refusal、Usage、StopReason 和未知事件策略符合设计规格。
- 缺失/零 MaxTokens、Temperature 大于 1、晚出现的 System/Developer、非法 ToolResult 顺序、`json_object` 和 Thinking Block 都按稳定错误在明确阶段失败。
- 流式 Usage 使用字段存在性感知的累计合并，首事件及时 Flush，取消和 Close 不泄漏 Goroutine。
- 明文 Key、Virtual Key、BaseURL、UpstreamModel、原始错误体、Thinking 和 Signature 不进入租户响应或不允许的遥测字段。
- 无数据库外键、无 Schema 迁移、UUIDv7 和 `TimeZone=Asia/Shanghai` 约束保持不变。
- OpenAI、OpenAI-Compatible、Fake Connector 和透明代理无回归。
- 全量测试、Race、Vet、Wire、架构、安全扫描、Build 与 Git 检查全部通过。
