# Anthropic Provider Connector 设计

## 1. 文档目的

本文定义多厂商统一网关 Phase 1C 的 Anthropic 原生上游接入。在现有多租户控制面、不可变运行时快照、平台凭据池、统一推理模型、OpenAI/OpenAI-Compatible Connector，以及 OpenAI Chat Completions、OpenAI Responses、Anthropic Messages 三个数据面入口之上，新增原生 Anthropic Messages Connector。

本阶段只实现现有统一推理模型能够稳定表达的 Anthropic 核心能力。Developer 指令层级和图片 Detail 等没有 Anthropic 对等字段的提示按本文明确规则规范化；Prompt Caching 的请求控制语义和 Extended Thinking 留到下一轮设计，不在本阶段做字段透传或静默降级。

本文继承并细化：

- `2026-08-24-multi-provider-unified-gateway-ddd-design.md`
- `2026-08-25-unified-protocol-kernel-postgresql-design.md`
- `2026-08-26-openai-provider-connectors-design.md`

若旧文档对 Anthropic Connector 的范围、协议版本或错误处理有不同描述，以本文为准。

## 2. 已确认决策

- 新增独立的原生 Anthropic Connector，不复用接口层 DTO，也不先重构为通用厂商 Connector 框架。
- 新增 Connector 类型 `anthropic` 和上游协议 `anthropic_messages`。
- Anthropic Provider 继续使用平台统一配置的单个 `base_url`；Connector 固定追加 `/v1/messages`。
- Anthropic Deployment 只能选择 `anthropic_messages`，不能选择 OpenAI 的 `responses` 或 `chat_completions`。
- `anthropic-version` 固定为经过契约测试的 `2023-06-01`，不进入 Provider 或全局配置。
- 首版支持文本、图片、工具调用与结果、结构化输出、Usage、普通响应和 SSE。
- 首版不启用 Prompt Caching 请求控制、Extended Thinking、服务端工具、Citations 或其他 Anthropic 私有扩展。
- 请求侧不启用统一模型无法表达的能力；响应中出现未知内容块时安全失败，不忽略、不转成文本。
- 不根据 `upstream_model` 字符串猜测模型能力，也不自动发送 `thinking.type=disabled`。默认或强制产生 Thinking Block 的模型在本阶段不兼容，直到统一 Thinking 语义完成。
- `ping` 和未来新增的未知顶层 SSE 事件按 Anthropic 版本策略忽略；已知内容事件中的未知 Block 或 Delta 仍安全失败。
- 凭据继续由平台控制面管理，复用 Platform、Organization、Project 三级加密凭据池；租户感知不到上游凭据。
- 凭据明文格式继续严格限制为只包含非空 `api_key` 的 JSON 对象。
- Connector 使用标准库 `net/http`，不引入 Anthropic SDK。
- 不实现重试、换凭据、协议回退、多地址、健康检查、故障转移或熔断。
- 现有 OpenAI、OpenAI-Compatible、Fake Connector 和透明代理行为保持不变。
- 本功能从 `v0.1-develop` 创建普通分支 `feat/anthropic-connector`，禁止使用 worktree。

## 3. 范围

### 3.1 本阶段包含

- Anthropic Connector 类型和 Messages 上游协议值。
- Provider 与 Deployment 协议支持矩阵校验。
- 控制面创建、更新、查询和快照编译对新类型的支持。
- 加密 `api_key` 凭据打开和 `x-api-key` 鉴权。
- 固定 `anthropic-version: 2023-06-01`。
- `/v1/messages` 请求编解码和安全 URL 拼接。
- Anthropic 必填参数和统一模型可映射子集的前置校验。
- Anthropic Messages 普通响应到统一 Response 的映射。
- Anthropic Messages SSE 到统一 Event 的增量映射。
- 三个客户端入口到 Anthropic 上游的普通与流式纵向测试。
- 错误分类、取消传播、响应大小限制、流空闲超时、重定向拦截和 OTel 脱敏。

### 3.2 本阶段不包含

- Prompt Caching 请求控制和缓存策略。
- Extended Thinking、Adaptive Thinking、thinking/signature/redacted-thinking 内容块。
- Anthropic 服务端工具、Citations、Documents、Files API、Batch API 或 Token Counting API。
- Anthropic Beta Header 或可配置 API 版本。
- 默认或强制开启 Thinking 的模型兼容保证。
- Amazon Bedrock、Google Vertex AI 或 Microsoft Foundry 的 Anthropic 接入。
- 任意自定义 Header、请求路径、鉴权模板或厂商脚本。
- ProviderEndpoint、多 BaseURL、权重、优先级或健康状态。
- 自动重试、跨凭据切换、故障转移、请求对冲或熔断。
- 租户 BYOK、租户自建 Provider 或租户查看上游配置。
- 管理 UI、计费、配额或请求正文留存。

## 4. 领域模型与关系

### 4.1 Connector 类型

目录领域增加：

```text
ConnectorAnthropic = "anthropic"
```

Anthropic Provider 必须具有合法、规范化的绝对 `http` 或 `https` BaseURL。它继续遵守既有约束：禁止 userinfo、query 和 fragment，允许平台配置路径前缀，持久化时移除尾部斜杠。

### 4.2 上游协议

目录领域增加：

```text
UpstreamAnthropicMessages = "anthropic_messages"
```

协议支持矩阵固定为：

| Connector | 允许的 UpstreamProtocol |
| --- | --- |
| `fake` | `fake` |
| `openai` | `responses`、`chat_completions` |
| `openai_compatible` | `responses`、`chat_completions` |
| `anthropic` | `anthropic_messages` |

Provider 和 Deployment 激活、快照编译以及 Connector 调用前都要校验该矩阵，不能依靠上游 4xx 发现错误配置。

### 4.3 调用关系

```text
Virtual Key
  -> Organization / Project AccessContext
  -> ModelAlias
  -> RouteTarget
  -> Anthropic Deployment
  -> Anthropic Provider
  -> Eligible ProviderCredential
  -> Anthropic Connector
  -> Provider BaseURL + /v1/messages
```

租户只能选择 ModelAlias，不能指定 Provider、Deployment、Credential、上游模型、BaseURL、协议版本或鉴权 Header。

## 5. DDD 边界

### 5.1 Domain

Domain 只增加 Connector 类型和 UpstreamProtocol 值，并维护 Provider 与协议的支持矩阵。统一 Request、Response、Event 不依赖 Anthropic DTO、HTTP、SSE 或 SDK。

本阶段现有统一模型已经能够表达文本、图片、工具调用、工具结果、结构化输出、Refusal、Usage 和停止原因，因此不为 Anthropic 新增厂商专属领域类型。

### 5.2 Application

Application 继续在同一个 Snapshot Session 内完成认证、Alias 路由、Provider 解析、凭据选择和 Connector 查找。它不构造 Anthropic HTTP 请求，不解析厂商响应，也不接触明文 API Key。

控制面 Application Service 在同一事务内显式校验 Provider 存在、Scope 匹配、协议矩阵和状态，不依赖数据库外键。

### 5.3 Infrastructure

`internal/infrastructure/connector/anthropic` 负责：

- 严格打开和解析加密凭据。
- 生成 Messages API 请求。
- 使用安全 HTTP Client 调用上游。
- 解析普通 JSON 响应和 SSE。
- 把 Anthropic 内容、停止原因、Usage 和错误映射为稳定的统一类型。

允许复用已经稳定且不含厂商语义的基础能力，例如 Credential Cipher、安全 Transport、超时配置和 OTel Transport。不得为了本阶段先重构 OpenAI Connector 或建立尚无第二个真实使用者的复杂抽象。

### 5.4 Interfaces

三个数据面入口不增加 Anthropic 上游分支。它们继续只把客户端协议解码为统一 Request，并把统一 Response/Event 编码回各自协议。

依赖方向保持：

```text
Interfaces -> Application -> Domain
Infrastructure -----------> Application / Domain
DI assembles all concrete implementations
```

## 6. Provider BaseURL 与请求构造

Anthropic 请求地址使用 URL 语义拼接，不做字符串连接：

```text
base_url = https://api.anthropic.com
endpoint = https://api.anthropic.com/v1/messages
```

若平台配置路径前缀：

```text
base_url = https://gateway.example.com/anthropic
endpoint = https://gateway.example.com/anthropic/v1/messages
```

请求固定使用 `POST`，并携带：

```text
content-type: application/json
x-api-key: <decrypted platform credential>
anthropic-version: 2023-06-01
```

客户端 Header、Body、Query 和 Path 都不能覆盖这些字段。HTTP Client 禁止跟随重定向，避免把凭据发送到其他 Host。

## 7. 请求映射

### 7.1 模型与生成参数

- `model` 使用 Deployment 的 `upstream_model`。
- Anthropic Messages 要求 `max_tokens`。统一 Request 必须显式提供大于零的 MaxTokens；缺失或为零时在网络调用前返回 `ParameterUnsupported("max_tokens")`，不得猜测平台默认值。
- `temperature` 仅接受 Anthropic 可表达的 0 到 1；统一 Request 中大于 1 的值返回 `ParameterUnsupported("temperature")`。
- `top_p` 和停止序列按统一 Optional 语义映射。
- `stream` 由 Connector 方法决定，必须与 Invocation.Request.Stream 一致。
- Connector 不按模型名自动改写 Temperature、Thinking 或 Effort；模型特有约束由上游 4xx 进入稳定的 `UpstreamRequestRejected`。

### 7.2 System、Developer 与消息

- 请求只允许在首个 User/Assistant 消息之前出现 System 或 Developer 消息。
- 所有前导 System/Developer 文本块保持顺序，映射为 Anthropic 顶层 `system` 文本块数组；Anthropic 没有独立 Developer Role，因此两者在该上游使用相同指令层级。
- 首个 User/Assistant 消息之后再次出现 System 或 Developer 时，返回 `ParameterUnsupported("messages")`，不得重排到请求顶部。
- User 和 Assistant 消息映射到 `messages`。
- 保持消息和内容块顺序。
- 不把系统提示、普通消息或工具结果拼接成不可逆字符串。
- 若统一请求形态无法合法映射为 Messages API，则在发起网络请求前返回安全的请求不兼容错误。

### 7.3 内容块

- Text -> `text`。
- Base64 Image -> `image` + `source.type=base64`。
- URL Image -> `image` + `source.type=url`。
- ToolCall -> Assistant `tool_use`。
- ToolResult -> User `tool_result`，保留 `tool_use_id` 和 `is_error`；统一 JSON ToolResult 使用紧凑 JSON 文本作为 Anthropic `content`，不把 JSON Object 塞入只接受文本或内容块数组的位置。

Anthropic 要求 User 消息中的 ToolResult 位于其他内容之前。若统一 User 消息中 ToolResult 出现在 Text/Image 之后，Connector 返回 `ParameterUnsupported("messages")`，不得静默重排。

OpenAI 风格图片 Detail 在 Anthropic 没有等价字段；它作为非语义质量提示不发送给 Anthropic，图片数据本身必须完整保留。

### 7.4 工具与结构化输出

- 工具映射为 `name`、`description`、`input_schema`。
- ToolChoice `auto`、`none`、`required`、`specific` 分别映射到 Anthropic `auto`、禁用工具、`any`、`tool`。
- Tool.Strict 直接映射为 Anthropic 标准 `strict`；不能用私有 Prompt 模拟。
- `StructuredJSONSchema` 使用 `output_config.format` 和统一 JSON Schema，不使用已废弃的 `output_format`。
- Anthropic 没有无 Schema 的 `json_object` 等价语义；`StructuredJSONObject` 返回 `ParameterUnsupported("response_format")`。

## 8. 普通响应映射

### 8.1 响应标识

上游 Anthropic ID 仅用于内部诊断，不越过统一领域边界。统一 Response ID 继续由代码生成 UUIDv7；面向客户端的 Model 保持客户端请求的逻辑 ModelAlias。

### 8.2 内容

- Anthropic `text` -> 统一 Text。
- Anthropic `tool_use` -> 统一 ToolCall，保留 ID、名称和完整 JSON Object 参数。
- 其他内容块，包括 thinking、redacted-thinking、server tool、citation 或未知块 -> `UpstreamInvalidResponse`。

当 Anthropic 停止原因为 `refusal` 时，普通响应丢弃可能存在的未完成输出。若 `stop_details.explanation` 非空，则把它映射为唯一统一 Refusal 内容；否则返回无内容的 `StopContentFilter` 响应。`stop_details.category` 不向租户暴露。

### 8.3 停止原因

| Anthropic | 统一 StopReason |
| --- | --- |
| `end_turn` | `end_turn` |
| `max_tokens` | `max_tokens` |
| `stop_sequence` | `stop_sequence` |
| `tool_use` | `tool_use` |
| `refusal` | `content_filter` |
| `model_context_window_exceeded` | `max_tokens` |

`pause_turn` 只服务于本阶段未启用的服务端工具，因此按 `UpstreamInvalidResponse` 处理。其他未知停止原因也安全失败，不能猜测为 `end_turn`。

### 8.4 Usage

- `input_tokens` -> InputTokens。
- `output_tokens` -> OutputTokens。
- `cache_read_input_tokens` -> CacheReadInputTokens。
- `cache_creation_input_tokens` -> CacheWriteInputTokens。

本阶段不允许客户端配置 Prompt Caching，但若上游返回缓存 Usage，仍按统一已有字段准确记录。缺失的可选缓存字段为零；负数或不一致的 Usage 视为无效上游响应。

## 9. SSE 流式映射

### 9.1 事件序列

Anthropic 标准序列映射为：

```text
message_start       -> ResponseStart + initial presence-aware UsageUpdate
content_block_start -> ContentBlockStart or ToolCallStart
content_block_delta -> TextDelta or ToolArgumentsDelta
content_block_stop  -> ContentBlockStop
message_delta       -> cache StopReason/StopDetails + presence-aware cumulative UsageUpdate
message_stop        -> ResponseFinish
```

`ResponseFinish` 只在 `message_stop` 产生一次。`message_delta` 只更新累计状态，不能提前结束统一事件流。

### 9.2 文本与工具增量

- `text_delta` 只能写入活动 Text 块。
- `input_json_delta.partial_json` 只能写入活动 ToolCall 块。
- ToolCall 在 `content_block_stop` 时必须已经累积成合法 JSON Object。
- Index 不得重复打开、关闭后复用或跨类型写入。
- 流结束前必须关闭所有活动块。

### 9.3 Usage

Anthropic `message_delta.usage` 是累计值。Connector 用新值覆盖对应累计字段，再产生统一 UsageUpdate；禁止把多个事件的 Token 数相加。任何字段回退都视为非法事件序列。

Usage DTO 必须保留字段是否出现：`message_start` 初始化 Input、Output 和缓存字段；后续 `message_delta` 只覆盖 JSON 中实际出现的字段，省略字段沿用前值。不得因为 Delta 只包含 `output_tokens` 就把 Input 或缓存字段清零。

### 9.4 Refusal

流式 Refusal 只会在 `message_delta` 才确定，之前的 Text Delta 已经 Flush，无法撤回。Connector 采用以下确定规则：

- 保留已经发送的 Text 事件，但最终 StopReason 必须是 `StopContentFilter`；三个入口客户端必须据此把先前内容视为不完整。
- 若 `stop_details.explanation` 非空，在所有上游内容块关闭后追加一个统一 Refusal Block，再产生 `ResponseFinish`。
- 若 Refusal 在任何输出前发生且 explanation 为空，则直接以无内容的 `StopContentFilter` 结束。
- 不缓存完整 SSE 来等待 StopReason，也不尝试在已发送事件上回写内容类型。

### 9.5 可忽略与不可忽略事件

- `ping` 可忽略。
- 未来新增的未知顶层事件可忽略，但仍受单事件大小限制。
- 已知 `content_block_start`/`content_block_delta` 中的未知内容类型不能忽略，必须安全终止。
- 上游 `error` 事件产生统一 StreamError，消息使用稳定安全文本，不包含原始错误正文。

### 9.6 流生命周期

- SSE Reader 支持任意网络分块、CRLF、多行 `data:` 和无尾随空行。
- 第一个有效统一事件到达后立即交给入口 Encoder Flush，不缓存完整响应。
- 客户端取消、Context Deadline、流空闲超时或 Close 必须关闭上游 Response Body 并停止读取 Goroutine。
- 非法流只影响当前请求，不得污染 Connector 或 RuntimeSnapshot 的共享状态。

## 10. 未支持能力策略

本阶段不主动发送 thinking、cache_control、server tools、citations 或 Beta Header。

如果模型或上游仍返回统一模型无法表达的内容：

1. Connector 不忽略内容块。
2. Connector 不把未知内容转成 Text。
3. Connector 返回 `UpstreamInvalidResponse` 或安全 StreamError。
4. 内部 Cause 只记录内容类型、事件类型和 Index 等结构信息，禁止携带未知内容正文、Thinking、Signature 或上游原始错误体。

该规则避免内容缺失、工具关联损坏、思考内容泄露和不同客户端协议产生不一致响应。

## 11. 凭据与作用域

Anthropic 复用现有 ProviderCredential：

```json
{"api_key":"上游密钥"}
```

加密前和解密后都要严格校验：

- 必须是 JSON Object。
- 必须且只能包含 `api_key`。
- `api_key` 去除首尾空白后不能为空。
- 不接受自定义 Header、Token Prefix、API Version 或 BaseURL。

选择顺序保持 Project -> Organization -> Platform，同一级池内进程内轮询。明文 Key 只存在于单次调用的局部可清理字节缓冲区中；快照只保存加密信封。

## 12. 错误分类

错误继续使用 Application 稳定分类，固定映射为：

| 来源 | 内部分类 |
| --- | --- |
| 401、403 | `UpstreamAuthentication` |
| 408、504 或 Context Deadline | `UpstreamTimeout` |
| 429 | `UpstreamRateLimited` |
| 500、502、503、529 | `UpstreamUnavailable` |
| 其他 4xx | `UpstreamRequestRejected` |
| DNS、连接、TLS 或其他网络错误 | `UpstreamUnavailable` |
| 非法 JSON、非法事件序列、未知内容块 | `UpstreamInvalidResponse` |
| 凭据缺失、解密失败或格式非法 | `CredentialUnavailable` |

实现直接复用现有 `gateway/port/errors.go` 的稳定枚举，不为同义错误创建重复枚举。

HTTP 错误响应体只能在既有小上限内读取后丢弃。原始错误体、BaseURL、UpstreamModel、Credential ID 和内部资源 ID 不进入租户错误。

首版不消费 `Retry-After` 执行重试，但错误结构应允许后续在不改变租户错误契约的前提下增加内部重试提示。

## 13. 配置与可观测性

Anthropic Connector 复用现有上游 Transport 配置：

- 完整响应超时。
- 流空闲超时。
- 连接池与 TLS 配置。
- 成功响应和错误响应大小上限。

不新增 Anthropic 私有环境变量。API 版本由代码常量和测试固定。

OTel Client Instrumentation 使用静态低基数语义：

```text
provider = anthropic
endpoint = messages
```

插桩 URL 使用脱敏静态路径，底层请求发送前再恢复真实 Provider URL。禁止在 span、metrics 或日志中记录 Prompt、输出、API Key、真实 BaseURL、UpstreamModel、原始动态路径、资源 ID 或 Query。

现有 `normalizeGatewayProvider` 必须加入 `anthropic`，`normalizeGatewayEndpoint` 必须加入 `messages`；测试要证明 OTel 看到的 URL 为静态 `/anthropic/messages`，而底层 Transport 收到真实 Provider URL。

## 14. 持久化与迁移

当前 Provider Connector 类型和 Deployment 上游协议均以 `varchar` 普通字符串持久化，数据库没有写死旧枚举约束，因此本阶段不新增表、字段或迁移。持久化测试仍须覆盖新值的逐字段往返，防止 Repository 或 Mapper 引入隐式白名单。

若真实源码存在限制新值的数据库单表约束，则增加版本化迁移修改该约束；迁移仍必须：

- 不含 `FOREIGN KEY`、`REFERENCES` 或 GORM Relationship。
- 保留 UUID 字段和普通关联索引。
- 保留 `timestamp(0) without time zone`。
- 业务写入继续由代码生成 UUIDv7。
- PostgreSQL DSN 继续要求 `TimeZone=Asia/Shanghai`。

## 15. DI 与运行时发布

- 为 Anthropic Messages 创建带安全 OTel Transport 的 HTTP Client。
- 复用控制面和数据面共享的 Credential Cipher/Opener。
- 构造 Anthropic Connector，并以 `anthropic` 注册到 ConnectorRegistry。
- Router 不新增路径；三个现有数据面 Handler 自动通过 Gateway 路由到新 Connector。
- 修改 Wire Provider 后重新生成并提交 `internal/di/wire_gen.go`。
- Provider、Deployment 或凭据变更继续递增 ConfigRevision；新请求获取新快照，进行中的请求继续使用已捕获 Session。

## 16. 测试策略

### 16.1 领域与控制面

- Connector 类型与协议值校验。
- Provider/Deployment 支持矩阵。
- 控制面创建、更新、停用与关联状态校验。
- Repository 逐字段往返和快照编译。
- Platform、Organization、Project 凭据选择优先级与轮询回归。
- 无数据库外键和无 GORM Relationship 自动检查。

### 16.2 Connector Contract Test

全部使用 `httptest.Server`，不访问真实 Anthropic：

- 请求路径、方法、固定 Header、上游模型和鉴权。
- System、消息、文本、图片、工具和结构化输出编码。
- Developer/System 前导规则、ToolResult 顺序、缺失 MaxTokens、Temperature 上界和 `json_object` 拒绝。
- 普通 Text、ToolUse、Refusal、停止原因和 Usage 解码。
- SSE 文本与工具事件、字段存在性感知的累计 Usage、流式 Refusal 和终止顺序。
- 任意分块、CRLF、多行 Data、Ping、未知顶层事件和首事件及时到达。
- 未知内容块、未知 Delta、事件乱序、重复 Index、非法工具 JSON 和提前 EOF。
- 4xx、429、5xx、529、非法 JSON、超限响应、空闲超时、取消和重定向。
- API Key、原始错误体、BaseURL、UpstreamModel 和资源 ID 泄露检查。

### 16.3 纵向矩阵

同一个 Anthropic Deployment 覆盖：

| 客户端入口 | 上游 | 模式 |
| --- | --- | --- |
| `/v1/chat/completions` | Anthropic Messages | 普通、SSE |
| `/v1/responses` | Anthropic Messages | 普通、SSE |
| `/v1/messages` | Anthropic Messages | 普通、SSE |

至少覆盖文本和工具调用两类主链路，验证客户端 Model 保持逻辑 Alias，内部上游模型和凭据不泄露。

### 16.4 回归与发布前验证

- OpenAI、OpenAI-Compatible、Fake Connector 测试。
- 现有透明代理 Path/RawPath、Host、SSE、限流、统计与 OTel 测试。
- 受影响包测试和 Race 测试。
- `go test ./...`。
- `go test -race ./...`。
- `go vet ./...`。
- Wire 生成一致性检查。
- 架构依赖测试。
- 无外键、敏感信息和真实凭据扫描。
- `git diff --check`。
- `go build -o /tmp/llm-proxy ./cmd/proxy`。

## 17. 验收标准

本阶段完成必须同时满足：

1. 平台管理员可以创建 `anthropic` Provider，并为其创建 `anthropic_messages` Deployment。
2. 错误的 Provider/协议组合在控制面或快照编译阶段被拒绝。
3. Anthropic Connector 使用平台配置 BaseURL、上游模型和分层凭据，租户不能覆盖。
4. 上游请求固定发送 `anthropic-version: 2023-06-01` 和 `x-api-key`。
5. 三个客户端入口都能通过同一个 Anthropic Deployment 完成普通和流式文本调用。
6. 三个客户端入口都能正确表达 Anthropic 工具调用结果。
7. 图片、结构化输出、停止原因、Refusal 和 Usage 在统一模型能力范围内正确映射。
8. SSE 首事件及时 Flush，取消和 Close 能终止上游读取。
9. 未知内容、Thinking、服务端工具或 Citation 不被静默丢弃或转成普通文本。
10. 原始错误体、API Key、BaseURL、UpstreamModel 和内部资源 ID 不进入租户响应或不允许的遥测字段。
11. 无自动重试、换凭据、协议回退、多地址选择或 Anthropic Beta 功能。
12. 数据库无外键、UUIDv7、时间类型和 PostgreSQL 时区约束保持不变。
13. OpenAI、OpenAI-Compatible、Fake Connector 和透明代理无回归。
14. 全量、Race、Vet、Wire、架构检查和构建通过。
15. 缺失/零 MaxTokens、Temperature 大于 1、非前导 System/Developer、非法 ToolResult 顺序和 `json_object` 在访问上游前稳定失败。
16. 默认或强制产生 Thinking Block 的模型明确返回不兼容错误，不静默丢弃 Thinking；该限制在下一轮 Extended Thinking 中解除。

## 18. 后续完善

下一轮优先设计以下两项，不得直接在 Connector 中透传：

### 18.1 Prompt Caching

- 扩展统一请求内容或消息的缓存控制语义。
- 定义 OpenAI/Anthropic 入口之间的能力矩阵和降级规则。
- 明确 CacheWrite/CacheRead Usage、计费和可观测语义。
- 设计不同模型与 Provider 对缓存 TTL、作用域和 Beta/稳定 Header 的支持。

### 18.2 Extended Thinking

- 扩展统一 Content 和 Event，表达 thinking、signature 和 redacted-thinking。
- 明确哪些客户端协议可以原生返回，哪些需要显式拒绝或受控降级。
- 防止思考内容进入普通日志、Trace、Metrics 或错误。
- 定义多轮工具调用时 thinking/signature 的完整保留规则。

在这两项之前，也可以独立接入 Gemini 原生 Connector；它继续复用统一 Gateway、Snapshot、凭据池和安全 Transport，不影响本设计的 Anthropic Deployment 语义。

## 19. 官方协议依据

本设计复审核对以下 Anthropic 官方资料：

- [Messages API](https://platform.claude.com/docs/en/api/messages/create)
- [Streaming Messages](https://platform.claude.com/docs/en/build-with-claude/streaming)
- [Structured Outputs](https://platform.claude.com/docs/en/build-with-claude/structured-outputs)
- [Stop Reasons](https://platform.claude.com/docs/en/build-with-claude/handling-stop-reasons)
- [Refusals and Fallback](https://platform.claude.com/docs/en/build-with-claude/refusals-and-fallback)
- [Extended Thinking](https://platform.claude.com/docs/en/docs/build-with-claude/extended-thinking)
