# Phase 1B-3：统一协议内核与 Fake Connector 实施计划

> **供执行代理使用：** 必须使用 `superpowers:subagent-driven-development`（推荐）或 `superpowers:executing-plans`，逐任务执行本计划。所有步骤使用 `- [ ]` 复选框跟踪。

**目标：** 新增统一推理语义内核、确定性 Fake Connector、`POST /v1/chat/completions` 和 `POST /v1/messages`，让同一个 Project、Virtual Key、ModelAlias 和 Fake Deployment 同时支持 OpenAI/Anthropic 兼容 JSON 与 SSE。

**架构：** 两个 HTTP Adapter 独立解析各自协议并映射到 UnifiedRequest，Gateway Use Case 在同一 Snapshot Session 中完成认证、Alias 解析和能力校验，再调用统一 Connector。Connector 返回 UnifiedResponse 或强类型事件流，入口 Adapter 只编码本协议响应，不进行 OpenAI 与 Anthropic DTO 互转。

**技术栈：** Go 1.25、标准库 `net/http`/JSON/SSE、Context、UUIDv7、Fake Connector、OpenTelemetry、Zap、Google Wire、Golden Test、`httptest`。

**前置计划：** `docs/superpowers/plans/2026-08-25-runtime-snapshot.md`

**设计依据：** `docs/superpowers/specs/2026-08-25-unified-protocol-kernel-postgresql-design.md`

**协议核对基线（2026-08-25）：** OpenAI 官方 Chat Completions API Reference；Claude 官方 Messages API、Streaming Messages 和 Structured Outputs 文档。实现固定在本文明确列出的兼容子集，不把未知字段透传给 Connector。

---

## 一、执行边界与最终文件结构

执行前必须位于 `feat/unified-protocol-kernel`，前两个计划的全量测试通过，工作区干净。禁止使用 worktree。

```text
internal/domain/inference/model/                         # 统一请求、响应、内容与事件
internal/domain/inference/port/                          # 统一事件 Stream
internal/application/gateway/port/connector.go           # Connector Registry
internal/application/gateway/service/gateway.go          # 认证、解析、能力校验、调用
internal/application/gateway/service/errors.go           # 稳定错误分类
internal/infrastructure/connector/fake/                   # 确定性 Fake Connector
internal/interfaces/http/protocol/openai/                 # Chat Completions Decoder/Encoder
internal/interfaces/http/protocol/anthropic/              # Messages Decoder/Encoder
internal/interfaces/http/handler/gateway/                 # 普通响应与 SSE 生命周期
internal/interfaces/http/handler/gateway/testdata/        # Golden JSON/SSE
internal/di/provider/gateway.go                           # Connector、Use Case、Handler 组装
```

Phase 1B 不增加真实厂商 Connector、重试、加权路由或熔断。现有 `/openai/*`、`/anthropic/*` 继续走 ReverseProxy，不复用本计划的协议 Decoder。

## 二、任务

### 任务 1：建立统一请求、响应、Optional 与内容块

**文件：**

- 新建：`internal/domain/inference/model/optional.go`
- 新建：`internal/domain/inference/model/content.go`
- 新建：`internal/domain/inference/model/request.go`
- 新建：`internal/domain/inference/model/response.go`
- 新建：`internal/domain/inference/model/model_test.go`

- [ ] **步骤 1：先写 Optional 和请求验证测试**

```go
func TestOptionalDistinguishesMissingAndExplicitZero(t *testing.T) {
	missing := None[float64]()
	zero := Some(0.0)
	if missing.Set || !zero.Set || zero.Value != 0 {
		t.Fatalf("missing=%+v zero=%+v", missing, zero)
	}
}

func TestRequestRequiredCapabilities(t *testing.T) {
	req := Request{
		Model: "vision-tools",
		Messages: []Message{{Role: RoleUser, Content: []ContentBlock{
			{Type: ContentImage, Image: &ImageContent{Source: ImageSource{Type: ImageURL, Data: "https://example.invalid/a.png"}}},
		}}},
		Tools: []Tool{{Name: "weather", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}
	got := req.RequiredCapabilities()
	if !got.Text || !got.ImageInput || !got.Tools {
		t.Fatalf("capabilities = %+v", got)
	}
}
```

- [ ] **步骤 2：运行并确认失败**

```bash
go test ./internal/domain/inference/model -v
```

预期：FAIL，统一模型尚不存在。

- [ ] **步骤 3：实现 Optional 与角色**

```go
type Optional[T any] struct {
	Value T
	Set   bool
}

func None[T any]() Optional[T] { return Optional[T]{} }
func Some[T any](value T) Optional[T] { return Optional[T]{Value: value, Set: true} }

type Role string
const (
	RoleSystem Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser Role = "user"
	RoleAssistant Role = "assistant"
)
```

Optional 只在领域内表达存在性，不直接实现 JSON Marshal；外部协议 DTO 使用指针或自定义 Unmarshal 决定是否调用 `Some`。

- [ ] **步骤 4：实现强类型内容块**

```go
type ContentType string
const (
	ContentText ContentType = "text"
	ContentImage ContentType = "image"
	ContentToolCall ContentType = "tool_call"
	ContentToolResult ContentType = "tool_result"
)

type ContentBlock struct {
	Type       ContentType
	Text       *TextContent
	Image      *ImageContent
	ToolCall   *ToolCallContent
	ToolResult *ToolResultContent
}
```

ImageSource 支持 URL 与 Base64，包含 MediaType、Data、Detail；ToolCall 包含 ID、Name 和原始 JSON Arguments；ToolResult 包含 ToolCallID、文本/JSON内容和 IsError。`Validate` 保证 Type 与唯一非空 Payload 一致。

- [ ] **步骤 5：实现 Request、Response 和 Usage**

Request 包含 Model、Messages、Tools、ToolChoice、StructuredOutput、Stream，以及 Optional Temperature/TopP/MaxTokens/Stop。StructuredOutput 保存 Name、Description、Strict 和 `json.RawMessage` Schema。

```go
type ToolChoiceMode string
const (
	ToolChoiceAuto ToolChoiceMode = "auto"
	ToolChoiceNone ToolChoiceMode = "none"
	ToolChoiceRequired ToolChoiceMode = "required"
	ToolChoiceSpecific ToolChoiceMode = "specific"
)

type ToolChoice struct {
	Mode ToolChoiceMode
	Name string
}

type StructuredOutputType string
const (
	StructuredJSONObject StructuredOutputType = "json_object"
	StructuredJSONSchema StructuredOutputType = "json_schema"
)

type Requirements struct {
	Text, ImageInput, Tools, StructuredOutput, Streaming bool
}
```

```go
type Usage struct {
	InputTokens           int64
	OutputTokens          int64
	CacheReadInputTokens  int64
	CacheWriteInputTokens int64
}

type Response struct {
	ID         uuid.UUID
	Model      string
	Content    []ContentBlock
	StopReason StopReason
	Usage      Usage
	CreatedAt  time.Time
}
```

`Request.Validate` 拒绝空 Model、空 Messages、非法角色/内容、空 Tool Name、非法 JSON Schema 和参数越界。`RequiredCapabilities` 从内容、Tools、StructuredOutput、Stream 计算能力集合。

- [ ] **步骤 6：测试、架构检查并提交**

```bash
gofmt -w internal/domain/inference
go test ./internal/domain/inference/...
go test ./internal/architecture
git add internal/domain/inference
git commit -m "feat: add provider neutral inference model"
```

### 任务 2：建立统一流事件、Connector 和 Gateway 错误模型

**文件：**

- 新建：`internal/domain/inference/model/event.go`
- 新建：`internal/domain/inference/model/event_test.go`
- 新建：`internal/domain/inference/port/stream.go`
- 新建：`internal/application/gateway/port/connector.go`
- 新建：`internal/application/gateway/service/errors.go`

- [ ] **步骤 1：先写事件顺序验证测试**

```go
func TestEventSequenceAcceptsTextAndToolCalls(t *testing.T) {
	events := []Event{
		NewResponseStart(uuid.Must(uuid.NewV7()), "fake-model"),
		NewContentBlockStart(0, ContentText),
		NewTextDelta(0, "hello"),
		NewContentBlockStop(0),
		NewToolCallStart(1, "call_1", "weather"),
		NewToolArgumentsDelta(1, `{"city":`),
		NewToolArgumentsDelta(1, `"Shanghai"}`),
		NewContentBlockStop(1),
		NewUsageUpdate(Usage{InputTokens: 10, OutputTokens: 5}),
		NewResponseFinish(StopToolUse),
	}
	if err := ValidateEventSequence(events); err != nil {
		t.Fatal(err)
	}
}
```

另测 Delta 出现在 BlockStart 之前、重复 Stop、Finish 后继续事件均失败。验证器实现增量 `Push(Event) error`，切片辅助函数只是对它的测试封装。

- [ ] **步骤 2：实现事件联合类型与 Stream**

事件类型固定为 ResponseStart、ContentBlockStart、TextDelta、ToolCallStart、ToolArgumentsDelta、ContentBlockStop、UsageUpdate、ResponseFinish、StreamError。每个 Event 只允许与 Type 对应的 Payload。

```go
type Stream interface {
	Recv(context.Context) (model.Event, error)
	Close() error
}
```

`io.EOF` 表示事件流自然结束；合法流必须在 EOF 前出现 ResponseFinish 或 StreamError。Close 必须幂等。

- [ ] **步骤 3：定义 Invocation 与 Registry**

```go
type Invocation struct {
	Request    model.Request
	Access     snapshot.AccessContext
	Deployment snapshot.Deployment
	Revision   int64
}

type Connector interface {
	Complete(context.Context, Invocation) (model.Response, error)
	Stream(context.Context, Invocation) (inferenceport.Stream, error)
}

type ConnectorRegistry interface {
	Find(connectorType string) (Connector, bool)
}
```

Connector 与 Invocation 都定义在 Application Port，避免 Domain 反向依赖 Snapshot Application。Connector 得到的是已解析 Deployment，不得接触 Virtual Key。真实 Connector 将来可通过 Deployment 中的 CredentialEnvelope 和注入的 CredentialCipher 解密厂商凭据；Fake Connector 不解密。

- [ ] **步骤 4：实现稳定错误分类**

定义 `ErrorCode`：InvalidRequest、AuthenticationFailed、PermissionDenied、ResourceNotFound、Conflict、CapabilityUnsupported、GatewayNotReady、ConnectorFailed、InternalError。`GatewayError` 包含 Code、SafeMessage、Param、Cause；`Error()` 不拼接密钥、请求正文或凭据。

- [ ] **步骤 5：验证并提交**

```bash
gofmt -w internal/domain/inference internal/application/gateway
go test ./internal/domain/inference/... ./internal/application/gateway/...
git add internal/domain/inference internal/application/gateway
git commit -m "feat: define connector stream and gateway errors"
```

### 任务 3：实现 Gateway Use Case 与能力校验

**文件：**

- 新建：`internal/application/gateway/service/gateway.go`
- 新建：`internal/application/gateway/service/gateway_test.go`

- [ ] **步骤 1：先写完整执行流程测试**

测试：无 Snapshot 返回 GatewayNotReady；错误 Key 返回 AuthenticationFailed；同一次调用只 Begin 一次 Session；Alias 按 Project 解析；图片/工具/结构化/流式能力不足在 Connector 前失败；Connector 类型不存在失败；非流式只调用 Complete；流式只调用 Stream；Virtual Key 不进入 Invocation。

```go
func TestCompleteUsesOneSnapshotSessionAndResolvedDeployment(t *testing.T) {
	fixture := newGatewayFixture(t)
	response, err := fixture.gateway.Complete(context.Background(), fixture.virtualKey, validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if fixture.store.beginCalls != 1 || fixture.connector.completeCalls != 1 {
		t.Fatalf("begin=%d complete=%d", fixture.store.beginCalls, fixture.connector.completeCalls)
	}
	if response.Model != validRequest().Model {
		t.Fatalf("model = %q", response.Model)
	}
}
```

- [ ] **步骤 2：运行并确认失败**

```bash
go test ./internal/application/gateway/service -v
```

预期：FAIL，Gateway 尚不存在。

- [ ] **步骤 3：实现 Gateway**

公开接口固定为：

```go
type Gateway interface {
	Complete(context.Context, string, inference.Request) (inference.Response, error)
	Stream(context.Context, string, inference.Request) (inferenceport.Stream, error)
}
```

执行顺序必须为：Request.Validate → Store.Begin → Session.Authenticate → Session.Resolve → RequiredCapabilities 校验 → Registry.Find → Connector。不要在 Complete/Stream 内访问 Repository、GORM 或数据库 Runtime。Stream 返回前用 `validatedStream` 包装 Connector Stream，对每个事件调用增量 SequenceValidator；非法序列关闭底层 Stream 并返回不泄露实现细节的 ConnectorFailed。

能力校验逐字段返回稳定 Param，例如 `messages.image`、`tools`、`structured_output`、`stream`。Connector 错误包装为 ConnectorFailed，Context Canceled/DeadlineExceeded 保留 `errors.Is` 语义。

- [ ] **步骤 4：运行测试、race 和架构检查**

```bash
gofmt -w internal/application/gateway/service
go test -race ./internal/application/gateway/service
go test ./internal/architecture
```

预期：PASS。

- [ ] **步骤 5：提交 Gateway Use Case**

```bash
git add internal/application/gateway/service
git commit -m "feat: execute inference through gateway use case"
```

### 任务 4：实现确定性 Fake Connector

**文件：**

- 新建：`internal/infrastructure/connector/fake/connector.go`
- 新建：`internal/infrastructure/connector/fake/stream.go`
- 新建：`internal/infrastructure/connector/fake/connector_test.go`

- [ ] **步骤 1：先写普通响应和流测试**

测试覆盖：纯文本；图片确认；存在 Tool 时返回 ToolCall；存在 ToolResult 时返回后续文本；StructuredOutput 返回符合测试 Schema 的确定 JSON；Usage 固定且非负；流事件顺序合法；Context 取消；Close 幂等；测试配置触发首事件前错误和 N 个事件后的 StreamError。

```go
func TestFakeStreamProducesValidSequence(t *testing.T) {
	connector := New(Options{ChunkSize: 3, IDGenerator: fixedIDs, Clock: fixedClock})
	stream, err := connector.Stream(context.Background(), validInvocation())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	events := collectEvents(t, stream)
	if err := inference.ValidateEventSequence(events); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **步骤 2：运行并确认失败**

```bash
go test ./internal/infrastructure/connector/fake -v
```

预期：FAIL，Fake Connector 尚不存在。

- [ ] **步骤 3：实现确定性响应策略**

响应规则固定：有 ToolResult 时输出确认文本；有 Tool 且无 ToolResult 时调用第一个 Tool 并生成稳定 JSON Arguments；有 StructuredOutput 时生成计划内固定 Schema 夹具结果；否则输出最后一个用户消息的确定性回显，图片块用 `[image:<media-type>]` 参与回显。生产默认不提供失败注入，测试通过构造 Options 注入。

Response ID 使用注入 IDGenerator；默认实现生成 UUIDv7。CreatedAt 使用注入 Clock；默认 `time.Now().UTC`。

- [ ] **步骤 4：实现背压 Stream**

Stream 不预先把所有事件写入无界 Channel。使用受互斥保护的事件切片索引或容量 1 Channel，在 `Recv(ctx)` 时推进；每次 Recv 先检查 Context。Close 设置关闭状态并释放等待者。Tool Arguments 按 ChunkSize 分块，UsageUpdate 为累计值。

- [ ] **步骤 5：运行 race 并提交**

```bash
gofmt -w internal/infrastructure/connector/fake
go test -race ./internal/infrastructure/connector/fake
git add internal/infrastructure/connector/fake
git commit -m "feat: add deterministic fake connector"
```

### 任务 5：实现 OpenAI Chat Completions 协议 Adapter

**文件：**

- 新建：`internal/interfaces/http/protocol/openai/request.go`
- 新建：`internal/interfaces/http/protocol/openai/decode.go`
- 新建：`internal/interfaces/http/protocol/openai/response.go`
- 新建：`internal/interfaces/http/protocol/openai/encode.go`
- 新建：`internal/interfaces/http/protocol/openai/error.go`
- 新建：`internal/interfaces/http/protocol/openai/protocol_test.go`
- 新建：`internal/interfaces/http/protocol/openai/testdata/text_request.json`
- 新建：`internal/interfaces/http/protocol/openai/testdata/tool_request.json`
- 新建：`internal/interfaces/http/protocol/openai/testdata/stream.golden`

- [ ] **步骤 1：先写请求解码 Golden Test**

覆盖：字符串 Content；数组 text/image_url；system/developer/user/assistant；assistant tool_calls；role=tool 的 tool_call_id；function Tool；tool_choice；`response_format.type=json_schema`；temperature=0 与字段缺失；max_tokens/max_completion_tokens 冲突；未知字段不透传。

```go
func TestDecodePreservesExplicitZeroTemperature(t *testing.T) {
	request, err := Decode(strings.NewReader(`{"model":"fake","messages":[{"role":"user","content":"hi"}],"temperature":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if !request.Temperature.Set || request.Temperature.Value != 0 {
		t.Fatalf("temperature = %+v", request.Temperature)
	}
}
```

- [ ] **步骤 2：运行并确认失败**

```bash
go test ./internal/interfaces/http/protocol/openai -v
```

预期：FAIL，OpenAI Adapter 尚不存在。

- [ ] **步骤 3：实现请求 DTO 与 Decoder**

Decoder 限制 Body 大小并使用显式联合类型解析 Message Content。只支持 `function` Tool；不支持的内置 Tool 返回 InvalidRequest。OpenAI `role=tool` 映射为统一 RoleUser + ToolResult Content，`tool_call_id` 保留；assistant `tool_calls` 映射为 RoleAssistant + ToolCall Content。`response_format` 映射 `json_object` 与 `json_schema`；JSON Schema 原样保留为 `json.RawMessage` 并验证 JSON 合法。

不使用 `Decoder.DisallowUnknownFields`，以兼容客户端新增无害字段；但未知字段不进入 UnifiedRequest，也不传给 Connector。

- [ ] **步骤 4：实现普通响应 Encoder**

返回 `chat.completion`：ID 使用 `chatcmpl-<uuidv7>`，同时包含 Created Unix 秒、ModelAlias、Choices、Assistant Message、FinishReason 和 Usage。统一 ToolCall 映射为 `message.tool_calls[].function{name,arguments}`；Usage 映射 prompt_tokens、completion_tokens、total_tokens，并在可用时输出缓存细分。

- [ ] **步骤 5：实现 SSE Encoder**

每个统一事件编码为 `data: {chat.completion.chunk}\n\n`，TextDelta 放入 `choices[0].delta.content`，ToolCallStart/ArgumentsDelta 放入 `delta.tool_calls`，ResponseFinish 输出 finish_reason；最终写 `data: [DONE]\n\n`。每个事件写完立即 Flush。UsageUpdate 只在请求的 `stream_options.include_usage=true` 时编码 Usage Chunk。

流开始后 StreamError 编码 OpenAI Error JSON 事件并结束，不再写 `[DONE]`；流开始前错误交给普通 Error Encoder。

- [ ] **步骤 6：实现 OpenAI 错误映射**

错误结构为 `{"error":{"message":...,"type":...,"param":...,"code":...}}`。AuthenticationFailed→401、PermissionDenied→403、ResourceNotFound→404、InvalidRequest/CapabilityUnsupported→400、GatewayNotReady→503、ConnectorFailed/InternalError→502/500。安全消息不能拼接 Cause。

- [ ] **步骤 7：运行 Golden Test 并提交**

```bash
gofmt -w internal/interfaces/http/protocol/openai
go test ./internal/interfaces/http/protocol/openai -v
git add internal/interfaces/http/protocol/openai
git commit -m "feat: add openai chat completions protocol adapter"
```

### 任务 6：实现 Anthropic Messages 协议 Adapter

**文件：**

- 新建：`internal/interfaces/http/protocol/anthropic/request.go`
- 新建：`internal/interfaces/http/protocol/anthropic/decode.go`
- 新建：`internal/interfaces/http/protocol/anthropic/response.go`
- 新建：`internal/interfaces/http/protocol/anthropic/encode.go`
- 新建：`internal/interfaces/http/protocol/anthropic/error.go`
- 新建：`internal/interfaces/http/protocol/anthropic/protocol_test.go`
- 新建：`internal/interfaces/http/protocol/anthropic/testdata/text_request.json`
- 新建：`internal/interfaces/http/protocol/anthropic/testdata/tool_request.json`
- 新建：`internal/interfaces/http/protocol/anthropic/testdata/stream.golden`

- [ ] **步骤 1：先写请求解码 Golden Test**

覆盖：顶层 system 字符串/文本块；user/assistant 消息；text/image base64/image URL；tool_use；tool_result；tools/input_schema/strict；tool_choice；`output_config.format.type=json_schema`；max_tokens 必填且允许显式 0；temperature/top_p/stop_sequences 缺失与零值。

```go
func TestDecodeMapsTopLevelSystemAndStructuredOutput(t *testing.T) {
	body := `{"model":"fake","max_tokens":128,"system":"be concise","messages":[{"role":"user","content":"hi"}],"output_config":{"format":{"type":"json_schema","schema":{"type":"object"}}}}`
	request, err := Decode(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if request.Messages[0].Role != inference.RoleSystem || request.StructuredOutput == nil {
		t.Fatalf("request = %+v", request)
	}
}
```

- [ ] **步骤 2：运行并确认失败**

```bash
go test ./internal/interfaces/http/protocol/anthropic -v
```

预期：FAIL，Anthropic Adapter 尚不存在。

- [ ] **步骤 3：实现请求 DTO 与 Decoder**

Messages API 没有 system Role，Decoder 将顶层 system 放入统一 RoleSystem 消息。工具输出使用 `tool_result.tool_use_id` 映射 ToolCallID。图片 source 支持 base64 与 url；不下载内容。Structured Output 只接受当前官方 `output_config.format`，不为旧 beta `output_format` 添加隐式兼容。

与 OpenAI 一样，未知字段可忽略但不得透传。缺失 `max_tokens`、非法 Content 联合类型或非法 JSON Schema 返回 InvalidRequest。

- [ ] **步骤 4：实现普通响应 Encoder**

返回 `type=message`、`id=msg_<uuidv7>`、`role=assistant`、Content Block、ModelAlias、stop_reason、stop_sequence 和 Usage。统一 Text 映射 text；ToolCall 映射 tool_use `{id,name,input}`，Arguments 必须在完整响应中解析为 JSON Object，非法时返回内部安全错误。

- [ ] **步骤 5：实现 Anthropic SSE Encoder**

严格输出命名事件：message_start；每个块的 content_block_start、content_block_delta、content_block_stop；message_delta；message_stop。TextDelta 使用 `text_delta`，Tool Arguments 使用 `input_json_delta.partial_json`，UsageUpdate 在 message_delta 中使用累计 Token。每个事件同时包含 `event:` 名和 data.type，并立即 Flush。

StreamError 输出 `event: error` 和 `{"type":"error","error":{"type":...,"message":...}}` 后结束。不要输出 OpenAI `[DONE]`。

- [ ] **步骤 6：实现 Anthropic 错误映射**

普通错误结构为 `{"type":"error","error":{"type":...,"message":...},"request_id":...}`。AuthenticationFailed→authentication_error/401；PermissionDenied→permission_error/403；InvalidRequest/CapabilityUnsupported→invalid_request_error/400；ResourceNotFound→not_found_error/404；GatewayNotReady→overloaded_error/503；内部错误使用 api_error 且不泄露 Cause。

- [ ] **步骤 7：运行 Golden Test 并提交**

```bash
gofmt -w internal/interfaces/http/protocol/anthropic
go test ./internal/interfaces/http/protocol/anthropic -v
git add internal/interfaces/http/protocol/anthropic
git commit -m "feat: add anthropic messages protocol adapter"
```

### 任务 7：实现数据面 HTTP Handler、SSE 取消与密钥提取

**文件：**

- 新建：`internal/interfaces/http/handler/gateway/dependencies.go`
- 新建：`internal/interfaces/http/handler/gateway/openai.go`
- 新建：`internal/interfaces/http/handler/gateway/anthropic.go`
- 新建：`internal/interfaces/http/handler/gateway/stream.go`
- 新建：`internal/interfaces/http/handler/gateway/handler_test.go`
- 修改：`internal/interfaces/http/middleware/request_id.go`
- 新建：`internal/interfaces/http/middleware/gateway_logging.go`
- 修改：`internal/interfaces/http/middleware/logging_test.go`

- [ ] **步骤 1：先写 Handler 与 Flusher 测试**

覆盖：仅 POST；Content-Type；Body 上限；OpenAI Bearer；Anthropic x-api-key 优先且 Bearer 回退；空 Key；Virtual Key 从不进入 Gateway Request/Connector；普通响应；首 SSE 事件在 Connector 未结束前到达客户端；客户端断开取消 Context 并 Close Stream；底层不支持 Flusher 时在写 Header 前返回安全 500。

首事件测试必须使用 `httptest.Server` 和真实 HTTP Client，不只使用 `httptest.ResponseRecorder`：

```go
func TestOpenAIStreamFlushesBeforeConnectorFinishes(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(newStreamingHandler(blockingStream(release)))
	defer server.Close()

	response, err := server.Client().Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(streamRequest))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	first, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || !strings.HasPrefix(first, "data: ") {
		t.Fatalf("first line = %q, err = %v", first, err)
	}
	close(release)
}
```

- [ ] **步骤 2：运行并确认失败**

```bash
go test ./internal/interfaces/http/handler/gateway ./internal/interfaces/http/middleware -v
```

预期：FAIL，Handler 尚不存在。

- [ ] **步骤 3：复用请求 ID 并实现协议固定日志中间件**

复用控制面计划已经实现的 RequestID Middleware。Gateway Logging 接收固定 `protocol` 参数，不再从 `/v1` Path 猜测；只记录协议、静态 Endpoint、状态、耗时、字节数、脱敏末四位和 Trace ID。现有 `Logging` 行为与测试不变。

- [ ] **步骤 4：实现两个 Handler**

OpenAI Handler 只从 Bearer 提取；Anthropic 优先 x-api-key 后回退 Bearer。提取后删除请求副本中的 Authorization/x-api-key，再调用 Decoder 与 Gateway。不要修改原请求供其他中间件读取；日志只能读取脱敏值。两个 Handler 都使用 `http.MaxBytesReader` 把请求正文上限固定为 4 MiB，超限返回各自协议的 InvalidRequest。

非流式调用 `Gateway.Complete` 并编码。流式先调用 `Gateway.Stream`，成功后确认 Flusher、设置 `text/event-stream`、`Cache-Control: no-cache`、`Connection: keep-alive`，再交给协议 Encoder；defer Close Stream。

- [ ] **步骤 5：运行 Handler、race 和泄漏测试**

```bash
gofmt -w internal/interfaces/http/handler/gateway internal/interfaces/http/middleware
go test -race ./internal/interfaces/http/handler/gateway ./internal/interfaces/http/middleware
```

预期：PASS。

- [ ] **步骤 6：提交数据面 Handler**

```bash
git add internal/interfaces/http/handler/gateway internal/interfaces/http/middleware
git commit -m "feat: handle unified gateway json and sse requests"
```

### 任务 8：注册新路由、扩展 OTel 归一化并完成 Wire 组装

**文件：**

- 修改：`internal/interfaces/http/router/router.go`
- 修改：`internal/interfaces/http/router/router_test.go`
- 修改：`internal/infrastructure/observability/http.go`
- 修改：`internal/infrastructure/observability/http_test.go`
- 新建：`internal/di/provider/gateway.go`
- 修改：`internal/di/modules/app.go`
- 修改：`internal/di/provider/transparent.go`
- 修改：`internal/di/wire.go`
- 生成：`internal/di/wire_gen.go`

- [ ] **步骤 1：先写路由隔离和 OTel 归一化测试**

路由测试验证：精确 POST `/v1/chat/completions` 到 OpenAI Gateway；精确 POST `/v1/messages` 到 Anthropic Gateway；其他方法 405；控制面资源仍正常；`/openai/v1/chat/completions` 与 `/anthropic/v1/messages` 仍走透明代理；未知路径仍由 Dashboard 接住。

OTel 测试验证直接 `/v1` 路由只暴露静态 `/openai/chat.completions` 或 `/anthropic/messages`，Query、模型、VirtualKey、ProjectID 都不进入 Span/Metric Attribute。

- [ ] **步骤 2：扩展 Router Dependencies**

```go
type Dependencies struct {
	// 保留现有字段
	OpenAIGateway    http.Handler
	AnthropicGateway http.Handler
	ControlPlane     http.Handler
}
```

Gateway 开启时在 Dashboard 根 Handler 前注册两个精确模式。分别使用固定协议的 Logging 和 `Instrumenter.WrapHandler("openai", ...)`/`WrapHandler("anthropic", ...)`。不套用现有透明代理 RateLimiter 和 Token Observer，避免混淆旧 Dashboard 语义。

- [ ] **步骤 3：扩展 OTel Endpoint 归一化**

`normalizeEndpoint` 同时识别带透明代理前缀和直接兼容路径：OpenAI `/v1/chat/completions`→chat.completions；Anthropic `/v1/messages`→messages。保持 provider 维度只允许 openai/anthropic/unknown，禁止加入 model、alias、tenant 或 UUID。

- [ ] **步骤 4：组装 Connector Registry 与 Gateway**

`gateway.go` 创建只含 `fake` 的 Registry、Gateway Service 和两个 Handler。Fake Connector 默认配置不启用测试故障。所有数据面依赖复用同一个 Snapshot Store。

Gateway 关闭时 Router 不注册两个新精确路由，数据库/快照/Connector 不启动；透明代理仍工作。

- [ ] **步骤 5：生成 Wire 并运行测试**

```bash
go run github.com/google/wire/cmd/wire@v0.7.0 ./internal/di
gofmt -w internal/di internal/interfaces/http/router internal/infrastructure/observability
go test ./internal/interfaces/http/router ./internal/infrastructure/observability ./internal/di/...
go test ./internal/architecture
```

预期：PASS。

- [ ] **步骤 6：提交路由和组装**

```bash
git add internal/interfaces/http/router internal/infrastructure/observability internal/di
git commit -m "feat: register unified openai and anthropic endpoints"
```

### 任务 9：完成双协议纵向、故障和回归验收

**文件：**

- 新建：`internal/interfaces/http/handler/gateway/integration_test.go`
- 新建：`internal/interfaces/http/handler/gateway/testdata/openai_text_response.golden.json`
- 新建：`internal/interfaces/http/handler/gateway/testdata/openai_tool_stream.golden`
- 新建：`internal/interfaces/http/handler/gateway/testdata/anthropic_text_response.golden.json`
- 新建：`internal/interfaces/http/handler/gateway/testdata/anthropic_tool_stream.golden`
- 修改：`internal/architecture/dependencies_test.go`

- [ ] **步骤 1：写真实 PostgreSQL 双协议纵向测试**

测试先执行迁移，再通过控制面 HTTP API 创建 Organization、Project、VirtualKey、Fake Provider、Fake Deployment、ModelAlias 和 RouteTarget；等待对应 Revision 发布后，用同一个 Virtual Key 分别调用两个数据面 Endpoint。

必须验证：

- OpenAI/Anthropic 纯文本普通响应。
- 图片输入映射到统一 ImageContent。
- 工具定义、ToolCall、ToolResult。
- OpenAI `response_format.json_schema` 与 Anthropic `output_config.format`。
- 两种 SSE 的首事件及时 Flush、Tool Arguments Delta、累计 Usage 和正确终止。
- 错误 Key、未知 Alias、能力不足、无快照、流中错误。
- Connector 收不到 Virtual Key。

- [ ] **步骤 2：添加依赖边界规则**

扩展架构测试，额外禁止：

- `domain/inference` 导入 HTTP Protocol 或 Snapshot Infrastructure。
- OpenAI Protocol 导入 Anthropic Protocol，反向同样禁止。
- Connector 导入 Interfaces DTO。
- 透明代理包导入新 Gateway Protocol。
- Application Gateway 导入 GORM/PostgreSQL。

- [ ] **步骤 3：运行受影响测试**

```bash
go test ./internal/domain/inference/... \
  ./internal/application/gateway/... \
  ./internal/infrastructure/connector/fake/... \
  ./internal/interfaces/http/protocol/openai/... \
  ./internal/interfaces/http/protocol/anthropic/... \
  ./internal/interfaces/http/handler/gateway/... \
  ./internal/interfaces/http/router/... \
  ./internal/infrastructure/observability/...
```

预期：PASS。

- [ ] **步骤 4：运行真实 PostgreSQL 纵向测试**

```bash
LLM_PROXY_TEST_POSTGRES_DSN="$LLM_PROXY_TEST_POSTGRES_DSN" \
  go test ./internal/interfaces/http/handler/gateway -run Integration -v
```

预期：所有 JSON/SSE Golden Test 和故障场景 PASS。

- [ ] **步骤 5：运行最终验证**

```bash
gofmt -w cmd internal
go test ./...
go test -race ./...
go vet ./...
go build -o /tmp/llm-proxy ./cmd/proxy
go build -o /tmp/llm-proxy-migrate ./cmd/migrate
go run github.com/google/wire/cmd/wire@v0.7.0 ./internal/di
git diff --check
git status --short
```

预期：全部 PASS；Wire 生成后工作区无意外差异；没有二进制、日志、真实 DSN、Virtual Key、管理令牌或主密钥进入 Git。

- [ ] **步骤 6：提交最终纵向测试**

```bash
git add internal/interfaces/http/handler/gateway internal/architecture/dependencies_test.go
git commit -m "test: verify unified gateway vertical paths"
```

## 三、本计划完成检查点

- 两个外部协议只在 UnifiedRequest/Response/Event 汇合，不互相转换 DTO。
- Optional 保留字段缺失与显式零值。
- 文本、图片、工具、工具结果、结构化输出和 Usage 均有双协议测试。
- Fake Connector 普通响应和流式响应确定、可取消且有背压。
- 一次请求使用同一 Snapshot Revision，热路径不查询数据库。
- Virtual Key 不转发给 Connector，日志和 OTel 不含敏感高基数字段。
- SSE 首事件及时 Flush，客户端断开后 Connector 收到取消。
- 现有透明代理、Dashboard、健康检查、限流、Token Usage 和 OTel 契约保持通过。

## 四、官方协议参考

- OpenAI Chat Completions API：<https://platform.openai.com/docs/api-reference/chat/create>
- Claude Messages API：<https://platform.claude.com/docs/en/api/messages/create>
- Claude Streaming Messages：<https://platform.claude.com/docs/en/build-with-claude/streaming>
- Claude Structured Outputs：<https://platform.claude.com/docs/en/build-with-claude/structured-outputs>
