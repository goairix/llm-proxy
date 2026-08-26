# OpenAI 与 OpenAI-Compatible Provider Connector 设计

## 1. 文档目的

本文定义多厂商统一网关 Phase 1C 第一阶段的详细设计。在 Phase 1B 已完成的多租户控制面、统一推理模型、不可变运行时快照、Fake Connector、OpenAI Chat Completions 入口和 Anthropic Messages 入口之上，接入真实 OpenAI 与 OpenAI-Compatible 上游，并补齐 OpenAI Responses 兼容入口。

本阶段只解决真实上游调用、两套 OpenAI 协议转换、平台凭据池和必要的安全边界。多上游地址、自动重试、健康检查、故障转移、熔断和厂商专属兼容逻辑在后续阶段单独设计。

本文继承并细化：

- `2026-08-24-multi-provider-unified-gateway-ddd-design.md`
- `2026-08-25-unified-protocol-kernel-postgresql-design.md`

若旧文档中的 Deployment 固定凭据、真实 Connector 范围或 Phase 1C 划分与本文冲突，以本文为准。

## 2. 已确认决策

- Provider 是平台配置的逻辑上游服务，不是只包含厂商名称的枚举。
- Provider 统一保存 `connector_type` 和单个 `base_url`。
- Deployment 表示 Provider 下的一个具体模型部署，保存上游模型、上游协议、作用域和能力，不保存地址，不固定绑定某个凭据。
- OpenAI 与 OpenAI-Compatible 都必须支持 `responses` 和 `chat_completions` 两套上游协议。
- Deployment 显式选择上游协议；禁止运行时探测、自动降级或协议回退。
- Provider、Deployment、ProviderCredential 全部由平台控制面管理。租户不能创建、修改或查看上游配置。
- 租户只持有平台签发的 Virtual Key，只感知当前 Project 可用的 ModelAlias。
- 上游凭据对租户完全透明；平台可以把凭据限定给 Platform、Organization 或 Project 使用。
- 凭据池按 Project、Organization、Platform 的优先级选择，同一级池内做进程内轮询。
- 第一版 OpenAI-Compatible 只支持标准 `/v1/responses` 与 `/v1/chat/completions`，不支持厂商专属路径、特殊字段、自定义鉴权头或响应修补。
- Connector 直接使用标准库 `net/http`，不引入厂商 SDK。
- 第一阶段不实现 Endpoint 池、负载均衡、健康检查、自动重试、故障转移或熔断。
- 现有 `/openai/*` 与 `/anthropic/*` 透明代理保持不变。
- 所有业务 ID 由代码生成 UUIDv7；数据库不创建外键，关联约束由 Application Service 在事务中保证。
- 本功能使用普通 Git 分支 `feat/provider-connectors`，禁止使用 worktree。

## 3. 范围

### 3.1 本阶段包含

- Provider 增加统一 `base_url`。
- Deployment 增加 `upstream_protocol`，移除固定 `credential_id` 和重复的 `connector_type`。
- ProviderCredential 从“被 Deployment 固定引用”调整为按 Provider 与 Scope 形成运行时凭据池。
- RuntimeSnapshot 增加 Provider 运行时配置和按作用域组织的凭据索引。
- OpenAI 官方上游 Connector。
- 标准 OpenAI-Compatible 上游 Connector。
- 两类 Connector 的 Responses API 普通响应和 SSE 流式适配。
- 两类 Connector 的 Chat Completions 普通响应和 SSE 流式适配。
- 新增数据面 `POST /v1/responses` OpenAI Responses 兼容入口。
- 为真实 OpenAI 拒绝响应补充统一 Refusal 内容块和 RefusalDelta 流事件。
- 保留并复用现有 `POST /v1/chat/completions` 与 `POST /v1/messages`。
- 上游错误分类、安全映射、取消传播、HTTP Transport 与 OTel 脱敏。
- 控制面、数据库迁移、快照编译、真实 Connector 和跨协议纵向测试。

### 3.2 本阶段不包含

- 一个 Provider 下配置多个 `base_url`。
- ProviderEndpoint、权重、优先级、主动或被动健康检查。
- 自动重试、跨凭据重试、请求对冲、故障转移和熔断。
- Responses 与 Chat Completions 的自动探测或运行时回退。
- OpenAI-Compatible 厂商专属 Profile。
- 自定义请求路径、自定义鉴权 Header、任意 Header 模板或任意字段映射。
- 原生 Anthropic、Gemini 或其他真实上游 Connector。
- 租户 BYOK、租户自建 Provider、租户自定义地址或租户查看上游凭据元数据。
- Responses retrieve、cancel、delete、input items、WebSocket 和后台任务接口。
- 管理 UI、计费、配额、审计日志和请求正文留存。
- 现有透明代理链路的重构或行为调整。

## 4. 术语与关系

### 4.1 Provider

Provider 表示平台管理的一个逻辑上游服务实例，例如“OpenAI 官方”或“公司内部标准 OpenAI-Compatible 集群”。它拥有统一 Connector 类型和基础地址。

```text
Provider
  ID
  Name
  ConnectorType
  BaseURL
  Status
```

同一厂商未来存在多个等价地址时，不复制 Deployment。后续通过 ProviderEndpoint 扩展 Provider；本阶段一个 Provider 只有一个 `base_url`。

### 4.2 Deployment

Deployment 表示 Provider 下一个具体、可调用的模型配置：

```text
Deployment
  ID
  ProviderID
  Name
  UpstreamModel
  UpstreamProtocol
  Scope
  Capabilities
  Status
```

`UpstreamProtocol` 首版取值：

- `responses`
- `chat_completions`
- `fake`，仅供测试用 Fake Provider 使用

OpenAI 与 OpenAI-Compatible Provider 均允许前两个取值。Fake Provider 只允许 `fake`。

Deployment 不保存 `base_url`、`connector_type` 或 `credential_id`。Provider 是 Connector 类型和地址的唯一事实来源，凭据由每次调用的访问上下文决定。

### 4.3 ProviderCredential

ProviderCredential 保存平台加密后的上游密钥及授权作用域：

```text
ProviderCredential
  ID
  ProviderID
  Scope
  SealedCredential
  Status
```

凭据作用域保持三种：

- `platform`
- `organization`
- `project`

同一 Provider、同一作用域目标下所有启用凭据组成一个逻辑凭据池。本阶段不增加只有分组作用、没有独立业务规则的 CredentialPool 表。

OpenAI 与 OpenAI-Compatible 的凭据明文格式固定为：

```json
{
  "api_key": "上游密钥"
}
```

控制面在加密前校验它是只包含非空 `api_key` 的 JSON 对象；首版不接受额外 Header 或厂商私有认证字段。

### 4.4 完整调用关系

```text
Virtual Key
  → Organization / Project AccessContext
  → ModelAlias
  → RouteTarget
  → Deployment
  → Provider
  → Eligible ProviderCredential
  → Connector
  → Provider BaseURL
```

租户只能选择 ModelAlias，不能从请求中指定 Provider、Deployment、Credential、Connector 类型、上游协议或上游地址。

## 5. DDD 边界与组件

### 5.1 Domain

Domain 负责：

- Provider URL 和 Connector 类型的领域不变量。
- Deployment 上游协议、Provider 引用、Scope、Capabilities 和状态不变量。
- ProviderCredential 的 Provider 引用、Scope、加密信封和状态不变量。
- 统一 Request、Response、Event 及流事件顺序。

Domain 不依赖 HTTP、OpenAI DTO、SSE、加密实现、GORM、PostgreSQL、配置框架或厂商 SDK。

### 5.2 Application

Application 负责：

- 控制面事务、关联读取、作用域校验和 ConfigRevision。
- 通过 AccessContext、RoutePlan 和 Provider 配置选择可用凭据层级。
- 调用 ConnectorRegistry，校验统一响应和统一流事件。
- 把 Connector 内部错误包装为稳定、安全的 GatewayError。

Application 不构造厂商 HTTP 请求，不解析厂商 JSON/SSE，也不持有解密后的上游 Key。

### 5.3 Infrastructure

Infrastructure 负责：

- PostgreSQL Entity、Repository 和版本迁移。
- 快照一致性读取、编译和原子发布。
- ProviderCredential 解密。
- OpenAI 与 OpenAI-Compatible HTTP Connector。
- Responses 与 Chat Completions 的上游 DTO、编解码和 SSE 解析。
- 安全 HTTP Transport、连接复用和 OTel Client Instrumentation。

### 5.4 Interfaces

Interfaces 负责：

- 控制面 Provider、Credential、Deployment DTO 的新字段。
- 新增 `POST /v1/responses` 请求解码、普通响应编码和 SSE 编码。
- 保持现有 Chat Completions 与 Anthropic Messages 入口只依赖统一 Gateway Use Case。

依赖方向继续由架构测试保证：

```text
Interfaces → Application → Domain
Infrastructure ───────────→ Application / Domain
DI 组装具体实现
```

## 6. Provider BaseURL

### 6.1 语义

`base_url` 是上游服务根地址。协议适配器在它后面追加固定标准路径：

- Responses：`/v1/responses`
- Chat Completions：`/v1/chat/completions`

例如：

```text
base_url = https://api.openai.com
Responses = https://api.openai.com/v1/responses
Chat      = https://api.openai.com/v1/chat/completions
```

BaseURL 可以包含平台预先配置的路径前缀。例如 `https://gateway.example.com/openai` 对应 `https://gateway.example.com/openai/v1/responses`。路径必须使用 URL 语义安全拼接，不能使用字符串直接连接。

### 6.2 校验与规范化

- 必须是绝对 `http` 或 `https` URL。
- 必须包含合法 Host。
- 禁止 userinfo、query 和 fragment。
- 规范化尾部斜杠，持久化稳定形式。
- OpenAI 与 OpenAI-Compatible Provider 必须提供 BaseURL。
- Fake Provider 不提供 BaseURL。
- BaseURL 只能来自已发布快照，客户端 Header、Body、Query 和 Path 都不能覆盖。

Provider BaseURL 更新是数据面配置变更，必须在事务中递增 ConfigRevision。新快照发布后，新请求使用新地址；已经开始的请求继续使用其调用开始时解析出的配置。

## 7. 控制面与关联约束

### 7.1 Provider API

创建 Provider 请求增加：

```json
{
  "name": "OpenAI 官方",
  "connector_type": "openai",
  "base_url": "https://api.openai.com"
}
```

允许的 Connector 类型：

- `fake`
- `openai`
- `openai_compatible`

Provider 更新支持名称、BaseURL 和状态。Connector 类型创建后不可修改；需要更换类型时创建新 Provider，避免现有 Deployment 和凭据被重新解释。

停用仍被启用 Deployment 引用的 Provider 时继续返回 Conflict。BaseURL 更新不改变关联，但必须重新编译快照。

### 7.2 ProviderCredential API

ProviderCredential API 路径保持不变。创建和更新凭据时：

- 平台管理员提供 Provider、Scope 和凭据 JSON。
- Application 在同一事务中确认 Provider、Organization 或 Project 存在。
- 对真实 OpenAI 类 Provider 校验固定 `api_key` 结构后立即信封加密。
- 查询和列表只返回 ID、Provider、Scope、状态和审计时间，不返回密文、包装数据密钥或任何可恢复秘密。
- 租户数据面和租户响应不返回凭据 ID。

停用凭据不再因为 Deployment 固定引用而被阻止。凭据撤销必须始终可执行，即使它是池中的最后一个凭据。

### 7.3 Deployment API

创建 Deployment 请求调整为：

```json
{
  "provider_id": "UUIDv7",
  "name": "gpt-5-responses",
  "upstream_model": "gpt-5",
  "upstream_protocol": "responses",
  "scope": {"kind": "platform"},
  "capabilities": {
    "text": true,
    "image_input": true,
    "tools": true,
    "structured_output": true,
    "streaming": true
  }
}
```

请求和响应移除 `credential_id` 与 `connector_type`。严格 JSON 解码会拒绝旧字段，避免调用方误以为它们仍然生效。

Application 在同一事务中校验：

- Provider 存在且启用。
- Provider Connector 类型支持所选 UpstreamProtocol。
- Deployment Scope 指向存在的 Organization 或 Project。
- 作用域、能力和状态满足现有领域规则。

Deployment 或 ModelAlias 激活时不要求当前凭据池非空。凭据是否可用取决于每次请求的 Project 和 Organization，且紧急撤销最后一个凭据不能被引用保护阻塞。激活流程只验证路由结构、Provider、Deployment、Scope 和能力；无凭据由调用阶段安全失败。

Deployment 更新支持名称、上游模型、上游协议、能力和状态。改变上游模型或协议会递增 ConfigRevision，并在新快照发布后原子生效。

## 8. 数据库迁移与仓储

新增版本迁移完成：

- Provider Entity 增加 `base_url` 文本列。
- Deployment Entity 增加 `upstream_protocol` 文本列。
- Deployment Entity 移除 `credential_id` 和 `connector_type` 列。
- 为常用的 Provider、Scope 和状态查询保留或增加普通索引。
- Fake 测试数据迁移为 `upstream_protocol=fake`。

迁移不得包含 `FOREIGN KEY`、`REFERENCES`、GORM Relationship、Association、Preload、级联保存或级联删除。Repository 继续逐字段映射领域对象和持久化 Entity。

正常业务代码显式生成 UUIDv7；数据库 ID 默认值只作为手工 SQL 兜底。时间字段继续使用 `timestamp(0) without time zone`，PostgreSQL DSN 必须指定 `TimeZone=Asia/Shanghai`。

迁移测试必须检查：

- 新列类型和索引符合预期。
- Deployment 不再持久化固定 Credential 或重复 Connector 类型。
- 数据库不存在外键。
- Entity 不包含 GORM 关联声明。
- UUID 和时间约束未回退。

## 9. RuntimeSnapshot 与凭据池

### 9.1 快照结构

RuntimeSnapshot 扩展为三个只读索引：

```text
RoutePlanIndex
ProviderRuntimeConfigIndex
CredentialIndex
```

ProviderRuntimeConfig 的运行时字段固定为：

```text
ProviderID
ConnectorType
BaseURL
```

Deployment Runtime View 的运行时字段固定为：

```text
DeploymentID
ProviderID
UpstreamModel
UpstreamProtocol
Capabilities
```

它不包含固定 Credential。

CredentialIndex 逻辑结构：

```text
ProviderID
  ProjectID      → CredentialEnvelope[]
  OrganizationID → CredentialEnvelope[]
  Platform       → CredentialEnvelope[]
```

快照只保存 Credential ID、Provider ID、Scope 和加密信封，不保存明文 Key。所有 slice、map 和字节字段在发布前深拷贝，发布后不可修改。

### 9.2 选择算法

一次真实上游调用按以下顺序选择凭据：

1. 过滤 Provider 匹配、状态启用且 Scope 与当前 AccessContext 精确匹配的凭据。
2. 若 Project 级池非空，只使用该池。
3. 否则若 Organization 级池非空，只使用该池。
4. 否则使用 Platform 级池。
5. 命中池内按 Credential UUID 稳定排序。
6. 使用进程内原子游标轮询，不访问数据库、不修改快照。

“Project 优先、Organization 次之、Platform 兜底”只表示凭据池为空时的层级回退。以下情况不触发跨池回退：

- 上游认证失败。
- 上游限流。
- 网络、超时或 5xx。
- 响应解析失败。

Fake Connector 不选择或解密凭据。

### 9.3 撤销优先和 LKG

凭据停用后，下一版快照必须移除该凭据。即使移除后某个 Provider 没有任何可用凭据，Compiler 也必须成功发布新快照。

禁止把“当前没有可用凭据”作为快照编译失败条件，否则 Refresher 会保留含旧凭据的 Last Known Good 快照，导致已撤销密钥继续服务。

池为空时：

- Alias 和 Deployment 仍可解析。
- 调用阶段返回内部 `credential_unavailable` Cause。
- Gateway 对租户安全映射为 `connector_failed`。
- 不泄露凭据池、Scope 或 Provider 配置。

手工改库造成的无效 Provider、Deployment 引用、非法 URL、非法协议或重复配置仍然使 Compiler 失败并保留 LKG。安全撤销类状态变化必须能编译成“资源不可用”的新快照，不能保留被撤销资源。

## 10. Connector 架构

### 10.1 ConnectorRegistry

现有 Connector 与 ConnectorRegistry Application Port 保持稳定。Registry 至少注册：

- `fake`
- `openai`
- `openai_compatible`

OpenAI 与 OpenAI-Compatible 可以共享同一个协议核心和 HTTP Client 工厂，但以两个 Registry 键显式注册。不能把未知 Connector 类型静默当成 Compatible。

### 10.2 包职责

推荐基础设施结构：

```text
internal/infrastructure/connector/openai/
  connector.go
  credential.go
  transport.go
  upstream_error.go
  responses/
    request.go
    response.go
    stream.go
  chatcompletions/
    request.go
    response.go
    stream.go
```

文件可以根据现有代码规模进一步拆分，但职责必须保持：

- Connector 只编排协议选择、凭据解析和 HTTP 调用。
- Responses Adapter 只处理 Responses Wire DTO。
- Chat Completions Adapter 只处理 Chat Completions Wire DTO。
- Transport 只处理安全请求、Header、连接、取消和 OTel。
- UpstreamError 只负责安全分类，不依赖外部 HTTP Encoder。

### 10.3 不使用厂商 SDK

直接使用 `net/http` 和明确 DTO，原因是：

- 精确控制 SSE 增量事件、首事件 Flush 和取消。
- OpenAI-Compatible BaseURL 可配置。
- 同时支持两套协议且需要映射到现有统一模型。
- 避免 SDK 的模型、默认值和版本更新成为领域契约。
- 可以注入 `RoundTripper`，用 `httptest.Server` 完成确定性测试。

## 11. 协议转换

### 11.1 客户端协议与上游协议解耦

入口协议只负责把客户端请求转成统一 Request。Deployment 决定上游 Adapter：

```text
OpenAI Chat Client ─┐
OpenAI Responses ───┼→ Unified Request → Responses Upstream
Anthropic Messages ─┘                  ↘ Chat Upstream
```

任何入口都不能根据客户端协议强制选择同名上游协议。

### 11.2 上游请求映射

两套上游 Adapter 都必须映射统一模型支持的：

- system、user、assistant 消息。
- 文本内容。
- 图片输入。
- Tool 定义、ToolChoice、ToolCall 和 ToolResult。
- 结构化输出 JSON Schema。
- 协议允许的显式零值参数，包括 `temperature=0` 和空停止序列。
- 流式开关。

字段映射固定为：

- Responses 使用 `input`、`instructions`、Function Tool/Input Item、`text.format` 和 `max_output_tokens`。
- OpenAI 官方 Chat 使用 `messages`、`tools`、`tool_choice`、`response_format` 和 `max_completion_tokens`。
- OpenAI-Compatible Chat 使用相同 Chat 结构，但输出 Token 上限使用兼容面更广的 `max_tokens`。
- Chat Completions 的统一 Stop 映射到 `stop`。
- Responses Create 当前支持子集不发送 Stop；统一 Request 显式设置 Stop 时，在调用前返回 `capability_unsupported`，参数为 `stop`。
- OpenAI 与标准 Compatible 的真实上游调用都不发送零值输出 Token 上限；统一 MaxTokens 显式为零时，在调用前返回 `capability_unsupported`，参数为 `max_tokens`。
- OpenAI Responses 请求固定发送 `store=false`；本平台不提供依赖服务端存储的 retrieve 或 continuation 语义。

OpenAI 与 OpenAI-Compatible 的 Chat 字段差异必须集中在小型 Wire Policy 中，不能复制完整 Adapter。

无法由目标协议无损表达的统一能力必须在调用前返回 `capability_unsupported` 或明确的安全 Connector 错误，禁止静默丢字段。

### 11.3 上游响应映射

普通响应必须转为统一 Response，并通过现有领域校验。规则包括：

- 统一 Response ID 由平台生成 UUIDv7，不依赖厂商 ID 格式。
- Response Model 使用客户端请求中的 ModelAlias，不暴露 UpstreamModel。
- 文本块、ToolCall ID、Tool 名称和参数保持语义。
- Responses `refusal` 和 Chat `message.refusal` 映射为新增的统一 Refusal 内容块，停止原因为 ContentFilter；不能把拒绝误判成 Connector 错误。
- ToolCall ID 必须保留，以便后续 ToolResult 正确关联。
- Usage 只映射当前统一模型已有的 InputTokens、OutputTokens、CacheReadInputTokens 和 CacheWriteInputTokens。上游 reasoning token 已包含在 OutputTokens 时不得再次相加；本阶段不新增独立 reasoning token 字段。上游缺失的可选缓存分项为零，输入与输出总和必须保持一致。
- 上游完成原因映射为统一 StopReason；未知原因失败关闭，不能随意当作正常结束。

### 11.4 上游流式映射

Responses 与 Chat Completions SSE 都必须增量解析为统一 Event：

- ResponseStart
- ContentBlockStart
- TextDelta
- RefusalDelta
- ToolCallStart
- ToolArgumentsDelta
- ContentBlockStop
- UsageUpdate
- ResponseFinish
- StreamError

每个事件必须通过 Event.Validate 和 SequenceValidator。Tool Arguments 必须按 ToolCall Index 分别累计，结束时是合法 JSON Object。Usage 必须累计不回退。EOF 前必须出现正常 Finish 或 StreamError。

统一推理模型相应增加：

- Response 可包含 Refusal 内容块。
- Event 增加 RefusalDelta，并要求它只能出现在 Refusal 类型的活动内容块中。
- OpenAI Responses Encoder 输出标准 refusal 事件和内容块。
- OpenAI Chat Encoder 输出 `message.refusal` 或对应流式 refusal delta。
- Anthropic 没有独立 Refusal Wire 类型，编码为文本内容并使用现有安全停止语义。

流实现不得预读取完整响应。上游产生首个可映射事件后，数据面 Encoder 必须能够在上游响应结束前 Flush 给客户端。

## 12. 新增 OpenAI Responses 入口

新增：

```text
POST /v1/responses
```

该入口与现有 `POST /v1/chat/completions`、`POST /v1/messages` 共用 Virtual Key 认证、Gateway Use Case、快照、路由和 ConnectorRegistry。

首版支持能映射到统一 Request 的 Responses Create 子集：

- `model`
- `input` 文本或受支持的 Input Item
- `instructions`
- `max_output_tokens`
- `temperature`
- `tools`
- `tool_choice`
- `text.format` 结构化输出
- `stream`

只支持 Function Tool；不支持 Web Search、File Search、Computer Use、Code Interpreter、Remote MCP 等内建工具。Input Item 只支持消息、图片 URL、Function Call 和 Function Call Output，并必须遵守统一工具调用图约束。不在首版范围内的 Responses 字段或 Item 类型应返回明确的 `invalid_request`，不能静默忽略。

普通响应编码为标准 Responses 对象；流式响应编码为带命名 `event:` 和 `data:` 的 Responses SSE 事件。错误继续使用 OpenAI 兼容错误对象，且只包含稳定、安全信息。

不实现 retrieve、cancel、delete、input items、后台任务或 WebSocket 路由。现有透明代理 `/openai/v1/responses` 不受影响。

## 13. 凭据解密与敏感信息边界

真实调用流程：

```text
CredentialSelector
  → CredentialEnvelope
  → Connector 调用 CredentialCipher/SecretResolver
  → 校验明文 JSON
  → 取出 api_key
  → 构造独立上游 Request
  → 注入 Authorization: Bearer
  → 调用完成后释放短生命周期对象
```

禁止把上游明文 Key 放入：

- Domain Request、Response 或 Event。
- Application Invocation。
- RuntimeSnapshot。
- Context Value。
- 日志字段或错误字符串。
- Trace 或 Metric Attribute。
- 控制面查询响应。
- 测试 Golden、配置文件和文档样例。

Virtual Key 在进入 Connector 前已从内部请求 Header 清理。Connector 必须新建上游请求，不能转发客户端原始 Header 集合，从结构上防止把 Virtual Key 当成上游凭据。

## 14. HTTP Transport 与可观测性

### 14.1 全局 Transport 配置

新增 `gateway.upstream` 全局配置，不把网络策略重复存入每个 Provider：

```yaml
gateway:
  upstream:
    connect_timeout: 10s
    tls_handshake_timeout: 10s
    response_header_timeout: 60s
    complete_timeout: 5m
    stream_idle_timeout: 5m
    idle_connection_timeout: 90s
    max_idle_connections: 100
    max_idle_connections_per_host: 20
```

所有时长必须大于零，连接数必须为正且每 Host 上限不大于总上限。配置支持环境变量覆盖，并同步更新配置结构、默认值、环境变量绑定、`config.yaml`、`.env.example` 和配置测试。

普通 JSON 响应最多读取 16 MiB，单个 SSE Event 最多读取 2 MiB，上游错误体最多读取 64 KiB。这些是协议安全上限，不按 Provider 或租户开放配置。超限归类为 `invalid_response`。

### 14.2 Transport 行为

- 使用共享、可复用的 `http.Transport`，禁止每个请求创建 Transport。
- 设置连接、TLS 握手、响应头和空闲连接边界。
- 普通和流式请求都继承入站 Context。
- 普通请求使用 Parent Context 与 `complete_timeout` 中更早的 Deadline。
- 流式请求在连续 `stream_idle_timeout` 未读取到任何上游字节时取消；每次成功读取后重新计时。
- 流式请求不使用会截断长响应的全局 `http.Client.Timeout`。
- 所有 Response Body 在成功、错误、取消和解析失败路径都必须关闭。
- Redirect 默认失败关闭，防止鉴权 Header 被带到未配置主机；首版不自动跟随重定向。
- 上游请求只携带协议所需固定 Header、Content-Type、Accept、Authorization 和 OTel 传播 Header。

### 14.3 OTel 与日志

OTel Client Instrumentation 必须延续数据面 Server Span，并向上游注入 `traceparent`。为避免 `otelhttp` 自动记录真实 BaseURL，插桩时使用静态规范化 URL，清空 query 和 fragment，再在最底层实际 Transport 前恢复真实 URL、Host 和 RequestURI。

日志和自定义指标只允许低基数、安全字段：

- `provider`：规范化 Connector 类型。
- `endpoint`：`responses` 或 `chat_completions`。
- `outcome`：成功或规范化错误分类。
- 协议、状态、耗时和字节数等现有安全元数据。

禁止记录 BaseURL、UpstreamModel、Prompt、Completion、动态 Path、Query、Organization ID、Project ID、Deployment ID、Credential ID、明文或密文凭据。

## 15. 错误分类与外部映射

Connector 内部定义可判定的上游错误类别：

- `authentication`
- `rate_limited`
- `timeout`
- `unavailable`
- `request_rejected`
- `invalid_response`
- `credential_unavailable`

这些类别用于 Cause、日志 Outcome、指标和后续重试设计。第一阶段不基于它们执行重试、换 Key、协议回退或故障转移。

对租户统一映射为现有：

```text
connector_failed → HTTP 502
```

上游 401/403 不能映射为租户 AuthenticationFailed，因为租户 Virtual Key 可能完全有效；它表示平台上游凭据或授权配置失败。上游 429 也不冒充平台自身限流响应。

错误体处理：

- 只读取固定上限。
- 原始错误体只作为内部受控诊断输入，不直接透传。
- GatewayError 只返回安全消息，Cause 不进入 HTTP JSON。
- URL、Header、请求正文和密钥不得拼入错误字符串。

流式失败分两类：

- 首个事件发送前失败：正常返回 ConnectorFailed HTTP 错误。
- SSE 已开始后失败：发出安全的统一 StreamError，由当前入口协议编码错误事件并终止流，不能再修改 HTTP 状态。

客户端取消和 Deadline 必须传播到上游。取消后不得继续读取、生成事件或尝试其他凭据。

## 16. 请求数据流

一次完整调用：

1. HTTP 入口按 OpenAI Chat、OpenAI Responses 或 Anthropic Messages 解码。
2. Virtual Key 认证得到 Organization、Project 和 VirtualKeyID。
3. Gateway 从不可变快照解析 ModelAlias 和 RoutePlan。
4. Gateway 校验统一 Request 所需能力不超出 Deployment Capabilities。
5. 读取 Provider Runtime Config 和 Deployment UpstreamProtocol。
6. CredentialSelector 按 Project、Organization、Platform 选择加密凭据。
7. Connector 按 Provider ConnectorType 和 Deployment UpstreamProtocol 选择 Adapter。
8. Connector 短暂解密 API Key，构造全新上游 HTTP 请求。
9. Transport 以安全 OTel URL 插桩后调用真实 BaseURL。
10. Adapter 将普通响应或 SSE 增量转换为统一 Response/Event。
11. Gateway 进行统一领域校验。
12. 当前入口协议编码为客户端期望的普通响应或流事件。

热路径不查询 PostgreSQL，不修改 RuntimeSnapshot，不把客户端 Virtual Key 传给上游。

## 17. 测试设计

### 17.1 Domain 与 Application

- Provider BaseURL 合法、非法和规范化用例。
- ConnectorType 与 UpstreamProtocol 支持矩阵。
- Deployment 不再包含固定 Credential 或重复 ConnectorType。
- Credential Scope 的 Project、Organization、Platform 匹配。
- 平台、组织、项目资源存在性和停用保护均在事务内校验。
- Credential 停用不再被 Deployment 引用保护阻塞。
- ConfigRevision 只在事务提交成功后通知 Refresher。

### 17.2 Snapshot

- Provider 配置、Deployment、CredentialIndex 使用同一 Repeatable Read 视图。
- Project 凭据优先于 Organization，Organization 优先于 Platform。
- 同级多个凭据按稳定顺序轮询。
- 不匹配 Provider、Scope 或停用的凭据不会进入候选池。
- 停用最后一个凭据仍发布更高 Revision，旧密钥从 Store 消失。
- 无凭据时路由仍可解析，调用安全失败。
- 快照复制后无法通过外部 slice、map 或 byte slice 修改。
- 数据库断开时保留 LKG；数据库恢复后发布最新安全 Revision。

### 17.3 Connector Contract

OpenAI 与 OpenAI-Compatible、Responses 与 Chat Completions 四个组合都覆盖：

- 文本请求和普通响应。
- 图片输入。
- Tool 定义、ToolChoice、ToolCall、Tool Arguments 和 ToolResult。
- Refusal 普通响应和 RefusalDelta 流式响应。
- 结构化输出。
- 显式零值。
- Usage 与 StopReason。
- 流式文本与多 ToolCall Index。
- 任意网络分块、CRLF、空行、多个 SSE 事件和 EOF。
- 首事件在上游结束前可读取。
- 上游 400、401、403、429、5xx、超时、取消、断流和非法 JSON/SSE。
- Response Body 在所有路径关闭。
- Redirect 不携带认证信息离开配置地址。

所有测试使用 `httptest.Server` 或注入 RoundTripper，不调用真实厂商网络。

### 17.4 跨协议纵向矩阵

至少覆盖：

| 客户端入口 | 上游协议 | 普通 | 流式 |
| --- | --- | --- | --- |
| OpenAI Chat Completions | Chat Completions | 是 | 是 |
| OpenAI Chat Completions | Responses | 是 | 是 |
| OpenAI Responses | Chat Completions | 是 | 是 |
| OpenAI Responses | Responses | 是 | 是 |
| Anthropic Messages | Chat Completions | 是 | 是 |
| Anthropic Messages | Responses | 是 | 是 |

矩阵断言 ModelAlias 不泄露 UpstreamModel、ToolCall ID 可往返、Usage 一致、错误协议正确、首帧及时 Flush。

### 17.5 安全与可观测性

- Invocation JSON、日志、Trace、Metric、错误响应和序列化快照中不存在明文上游 Key。
- 客户端 Virtual Key 不出现在上游 Header。
- 客户端不能覆盖 BaseURL、Host 或 Authorization。
- OTel Span 使用静态 URL，不包含私有 BaseURL、模型或资源 ID。
- 上游原始错误体和 Cause 不进入租户响应。
- Metrics 标签集合保持低基数。

### 17.6 数据库、架构与回归

- 真实 PostgreSQL 迁移纵向测试在配置测试 DSN 时运行。
- 自动检查迁移和 Entity 不含数据库外键或 GORM Relationship。
- 架构测试继续保证 DDD 依赖方向。
- 现有透明代理路径、Path/RawPath、Host、SSE、限流、统计和 OTel 回归测试必须通过。
- 完成后运行目标包测试、`go test ./...`、`go test -race ./...`、`go vet ./...`、Wire 检查、`git diff --check` 和二进制构建。

## 18. 验收标准

本阶段完成必须同时满足：

1. 平台管理员可以创建 OpenAI 或 OpenAI-Compatible Provider，并配置统一 BaseURL。
2. 平台管理员可以为 Provider 创建 Platform、Organization 或 Project 作用域的加密凭据。
3. Deployment 显式选择 Responses 或 Chat Completions，不保存地址、Connector 类型或固定凭据。
4. 同一个 ModelAlias 可以通过 OpenAI Chat、OpenAI Responses 或 Anthropic Messages 入口调用。
5. 上游协议与客户端入口协议可以不同，普通和流式调用均保持统一语义。
6. OpenAI 与 OpenAI-Compatible 的两套标准上游协议都通过 Contract Test。
7. Project、Organization、Platform 凭据优先级正确且租户无感知。
8. 停用最后一个凭据后仍发布新快照，旧凭据不再被调用。
9. SSE 首事件在上游结束前到达客户端，取消能传播到上游。
10. 明文上游 Key、私有 BaseURL、UpstreamModel 和内部资源 ID 不进入租户响应或不允许的遥测字段。
11. 无自动重试、协议回退、跨凭据切换或多 BaseURL 选择。
12. 数据库无外键、代码生成 UUIDv7、时间与 PostgreSQL 时区约束保持不变。
13. 现有透明代理行为无回归。
14. 全量、Race、Vet、架构检查和构建通过。

## 19. 后续演进

下一阶段可以在不修改 Deployment 语义的前提下引入：

```text
Provider
  └── ProviderEndpoint[]
        BaseURL
        Priority
        Weight
        HealthStatus
```

EndpointSelector 在凭据选择之前或与凭据授权策略共同产生一次 AttemptPlan。重试策略必须基于内部 UpstreamError 分类、请求幂等性和已经输出的流状态设计，不能简单对所有失败重试。

厂商兼容差异通过显式、版本化 Profile 扩展；禁止把任意 Header、路径或脚本能力直接开放给租户。Anthropic、Gemini 等原生 Connector 各自实现协议 Adapter，但继续复用统一 Request、Response、Event、Gateway、快照和安全 Transport 契约。
