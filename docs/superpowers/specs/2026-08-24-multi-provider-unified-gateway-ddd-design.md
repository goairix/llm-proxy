# 多供应商统一大模型网关——DDD 架构设计

**日期：** 2026-08-24

**状态：** 设计讨论已批准，待书面规格复核

**仓库：** `github.com/goairix/llm-proxy`

## 1. 文档约定

本项目面向团队的设计文档、实施计划、Dashboard 文案和运维说明默认使用中文。代码标识符、协议字段、标准名称以及没有准确中文译法的技术术语保留英文。

本文是总体架构规格。系统按阶段交付，每个阶段实施前还要有独立的细化设计和实施计划。

## 2. 背景

当前项目是一套透明反向代理：

- `/openai/*` 转发到配置的 OpenAI 或 OpenAI-Compatible 上游。
- `/anthropic/*` 转发到配置的 Anthropic 上游。

这条链路不做协议转换。现有中间件顺序、SSE 透传、上游 API Key 传递、限流、Token 观察、日志和 OpenTelemetry 语义都是必须保持的兼容契约。

新产品在同一进程中增加一条独立的统一模型网关链路。它接收兼容 OpenAI 和 Anthropic 的客户端请求，在多个供应商部署之间路由逻辑模型，完成请求与响应的双向协议转换，并逐步演进为可自托管的多租户平台。现有透明代理继续存在，行为不变。

## 3. 目标

1. 保持现有透明代理的公开路径和行为。
2. 新增 `POST /v1/chat/completions` 和 `POST /v1/messages`。
3. 第一阶段支持 OpenAI、Anthropic、Gemini 和通用 OpenAI-Compatible 四类上游连接器。
4. 统一表达文本、图片输入、工具调用、结构化输出、流式事件、停止原因和 Usage；不静默丢弃不支持的语义。
5. 同时支持逻辑模型别名和显式 `provider/model` 选择。
6. 支持加权路由、优先级故障转移、有限重试、被动健康状态和熔断。
7. 客户端使用网关虚拟 Key；上游凭据由服务端管理。
8. 数据面可在单区域内以无状态多副本方式水平扩展。
9. 最终提供基于 PostgreSQL 的多租户控制面，包括组织、项目、RBAC、审计、预算、Usage 和成本。
10. 整个代码库采用 DDD 与 Clean Architecture，整体组织参考 `/Users/dysodeng/project/go/app-service`，同时执行更严格的依赖边界。

## 4. 非目标

- 改变现有透明代理的路径或语义。
- 第一阶段支持 OpenAI Responses API。
- 第一阶段支持音频、文件、Batch、支付、充值、发票或订阅套餐。
- 把所有供应商私有能力都提升为统一公共字段。
- 第一阶段动态加载 Go Plugin 或 WASM 连接器。
- 初始架构实现多区域主动—主动部署。
- 当上游只接受内联图片时，由网关主动下载远程图片 URL。
- 记录 Prompt、模型输出、工具参数、API Key 或供应商 Secret。

## 5. 总体架构

采用“模块化单体 + 类型化统一语义内核 + DDD 分层”：

```text
客户端
  ├─ /openai/*、/anthropic/*
  │    └─ 现有透明代理链路
  │
  └─ /v1/chat/completions、/v1/messages
       └─ 入口协议适配器
          └─ 推理应用服务
             ├─ 虚拟 Key 认证与访问策略
             ├─ 能力校验
             ├─ 模型解析与路由计划
             ├─ 配额预留
             ├─ 供应商连接器调用
             └─ Usage 与成本计量
                └─ 统一响应或类型化事件流
                   └─ 出口协议适配器
```

统一语义模型不是无类型的“万能 JSON”，而是稳定、类型化的公共语义加显式命名空间的供应商扩展。

## 6. DDD 与 Clean Architecture

### 6.1 顶层分层与依赖

```text
internal/
├── interfaces/       # HTTP/CLI、协议 DTO、Handler、Router、Middleware
├── application/      # 用例、Command/Result、流程编排、应用端口
├── domain/           # 聚合、值对象、领域服务、端口、仓储接口
├── infrastructure/   # Server、供应商、持久化、Redis、Secret、配置、OTel
└── di/               # Wire 模块与唯一组装根
```

依赖方向固定为：

```text
interfaces → application → domain
infrastructure → 实现 domain/application 定义的端口
di → 仅在最终装配时同时认识各层
```

领域层只允许依赖 Go 标准库和经过明确选择的基础值类型库，禁止导入供应商 SDK、`net/http`、GORM、Redis、Viper、OpenTelemetry、Zap 或任何 `infrastructure` 包。数据库 Entity 不能直接充当领域 Aggregate。

依赖注入沿用参考项目的 Google Wire 方式。生成代码纳入版本管理，仅在依赖图变化时重新生成。

### 6.2 限界上下文

#### 推理域 `inference`

负责统一消息、内容块、工具定义与调用、结构化输出约束、生成参数、停止原因、Usage、供应商尝试和类型化流式事件。核心类型包括 `InferenceRequest`、`Message`、`ContentBlock`、`ToolDefinition`、`InferenceResponse`、`StreamEvent`、`Usage` 和 `AttemptResult`。

供应商连接器端口属于该领域，因为其契约完全由推理领域类型表达。

#### 模型目录域 `modelcatalog`

负责供应商类型、供应商部署、上游模型、模型别名、能力集合、路由目标、路由策略和不可变 `RoutePlan`。

#### 访问控制域 `access`

负责 Organization、Project、虚拟 Key、角色、项目模型策略和授权判断。初始自托管版本使用平台级引导组织和项目，但应用与领域契约从一开始携带组织、项目上下文。

#### 凭据域 `credential`

负责凭据元数据、平台或租户作用域、供应商绑定、Secret 引用、启停状态和轮换版本。

#### 计量域 `metering`

负责不可变 Usage 事实、尝试级成本、版本化价格、配额预留、项目预算、对账和成本 Ledger。

#### 共享内核 `shared`

只保存稳定的跨域 ID、领域错误基础类型、时钟和领域事件基础类型。通用工具函数不能因为方便而进入共享内核。

### 6.3 目标目录

```text
internal/
├── domain/
│   ├── inference/{model,valueobject,service,port,errors,event}/
│   ├── modelcatalog/{model,valueobject,service,repository,errors,event}/
│   ├── access/{model,valueobject,service,repository,errors,event}/
│   ├── credential/{model,valueobject,service,repository,port,errors,event}/
│   ├── metering/{model,valueobject,service,repository,port,errors,event}/
│   └── shared/{valueobject,errors,event,port}/
├── application/
│   ├── inference/{service,dto/command,dto/result}/
│   ├── modelcatalog/{service,dto}/
│   ├── access/{service,dto}/
│   ├── credential/{service,dto}/
│   └── metering/{service,dto,event/handler}/
├── interfaces/
│   ├── http/
│   │   ├── handler/{transparent,inference,admin,dashboard}/
│   │   ├── dto/request/{openai,anthropic,admin}/
│   │   ├── dto/response/{openai,anthropic,admin}/
│   │   ├── middleware/
│   │   ├── router/
│   │   └── validator/
│   └── cli/command/
├── infrastructure/
│   ├── provider/{openai,anthropic,gemini,openaicompatible}/
│   ├── proxy/
│   ├── persistence/{entity,repository,migration,transaction}/
│   ├── cache/redis/
│   ├── metering/redisstream/
│   ├── secrets/{environment,file,dbencrypted}/
│   ├── snapshot/{file,postgres}/
│   ├── config/
│   ├── server/http/
│   ├── observability/
│   └── logger/
└── di/{modules,provider}/
```

目录随对应阶段创建，不提前生成空目录和无用途脚手架。

### 6.4 现有包迁移

| 当前包 | 目标职责 |
| --- | --- |
| `internal/proxy` | `internal/infrastructure/proxy` |
| `internal/middleware` | HTTP 部分进入 `interfaces/http/middleware`；技术后端进入 infrastructure |
| `internal/config` | `internal/infrastructure/config` |
| `internal/observability` | `internal/infrastructure/observability` |
| `internal/logger` | `internal/infrastructure/logger` |
| `internal/dashboard` | Handler/UI 进入 `interfaces/http/handler/dashboard`；读模型不进入领域层 |
| `internal/tokenusage` | 透明代理观察器进入 infrastructure；新网关 Usage 进入计量域与连接器适配器 |
| `internal/server` | Router 进入 interfaces；Server 生命周期进入 `infrastructure/server/http` |

移动现有代码属于重构，不得改变行为。移动前先用特征测试锁定中间件顺序、路由、SSE Flush、Token 语义、健康检查、OTel 传播、关闭顺序、日志和限流。

透明代理是技术适配器，不虚构为业务领域，也不经过推理统一语义模型。

## 7. HTTP 公开契约

### 7.1 现有透明代理

- `/openai/*` 保持上游 Key 透传和 OpenAI 路径重写。
- `/anthropic/*` 保持上游 Key 透传和 Anthropic 路径重写。
- `/healthz`、`/readyz` 和现有 Dashboard 保持当前语义。

### 7.2 新统一网关

- `POST /v1/chat/completions` 提供兼容 OpenAI Chat Completions 的 JSON 与 SSE。
- `POST /v1/messages` 提供兼容 Anthropic Messages 的 JSON 与 SSE。
- Chat Completions 从 `Authorization: Bearer <key>` 读取虚拟 Key。
- Messages 优先从 `x-api-key` 读取虚拟 Key，同时把 Bearer 认证作为明确的网关扩展；`anthropic-version` 必须属于入口适配器支持的版本。
- 虚拟 Key 绝不当作供应商凭据，也不转发到上游。
- 在能力矩阵允许的范围内，官方 SDK 只需修改 Base URL 和 API Key。
- 未知公共字段默认拒绝；入口协议明确允许的字段和已登记的供应商扩展命名空间除外。

OpenAI 与 Anthropic DTO 只存在于接口层，不能充当领域模型，也不能直接传给供应商连接器。

## 8. 统一推理语义

### 8.1 请求

统一请求支持 system/developer 指令及顺序语义、user/assistant 消息、文本、内联图片、目标上游可直接接受的图片 URL、工具定义与调用、工具结果、并行工具、JSON/JSON Schema 输出、常用采样参数、流式标记、Metadata、逻辑模型标识和供应商扩展。

当“字段缺失”和“显式提供默认值”会影响上游行为时，领域类型必须保留两者差异。

### 8.2 供应商扩展

```json
{
  "provider_options": {
    "gemini": {},
    "anthropic": {}
  }
}
```

每个连接器维护自己带版本的扩展 Schema。只有路由目标属于对应供应商且调用方有权限时才接受扩展。禁止任意 Header 注入、任意 URL 字段以及跨供应商转发扩展。

### 8.3 类型化事件流

供应商响应解码为 `ResponseStart`、`ContentBlockStart`、`TextDelta`、`ToolCallStart`、`ToolArgumentsDelta`、`ContentBlockStop`、`UsageUpdate`、`ResponseFinish` 和 `StreamError`。

事件保留内容块索引、工具调用 ID、统一停止原因、统一 Usage，以及审计或诊断需要的供应商原始枚举值。供应商 SDK 类型不能越过连接器边界。

事件流采用拉取或迭代器接口，让取消与背压自然向上游传播。连接器不能接收 `http.ResponseWriter`。

## 9. 请求生命周期

1. HTTP 入口适配器校验协议请求并生成应用 Command。
2. 推理应用服务认证虚拟 Key，得到组织、项目和策略上下文。
3. 协议映射器生成 `InferenceRequest`。
4. 模型解析器处理逻辑别名或 `provider/model`。
5. 过滤能力、供应商扩展、权限和预算不兼容的目标。
6. 配额服务预留请求数、Token 和预计成本。
7. 路由器选择目标并调用连接器。
8. 连接器编码原生请求，将上游 JSON/SSE 解码为统一事件。
9. 出口适配器把统一事件编码回客户端协议。
10. 尝试级 Usage 和成本事件通过可靠计量链路异步对账与持久化。

客户端取消、Context Deadline、背压和 Trace Context 必须贯穿整个链路。

## 10. 供应商连接器与能力矩阵

连接器必须声明或实现供应商类型、连接器版本、能力集合、扩展 Schema 版本、请求编码、普通/流式响应解码、错误分类、Usage 归一化、认证 Header 注入和可选健康观察钩子。

首批内置连接器为 OpenAI、Anthropic、Gemini 和通用 OpenAI-Compatible，通过 Wire Provider Set 注册。增加内置连接器不得修改入口协议或推理应用用例。未来可增加带版本的外部 Adapter RPC 协议；第一阶段不支持运行时 Plugin。

能力不能仅凭供应商名称推断。模型目标显式声明文本、URL/内联图片、工具、并行工具、文本流、工具参数增量流、JSON、JSON Schema、Usage 和供应商扩展能力。

入口映射器从请求推导所需能力。路由排除不能满足全部公共语义的目标；没有合格目标时返回入口协议原生错误。消息内容块、工具、输出约束和扩展都不得静默删除。

如果目标需要网关下载图片 URL 再转成内联数据，该目标在第一阶段视为不合格。

## 11. 模型解析与路由

### 11.1 模型选择

- `fast-chat` 等逻辑名称通过不可变、带版本的模型目录快照解析。
- `provider/model` 在第一个 `/` 处分割，后半部分是完整上游模型名，因此模型名本身仍可包含 `/`。
- 显式选择仍必须经过权限、能力、预算、凭据和熔断检查。
- 同一供应商类型可有多个合格部署。
- 响应 `model` 保持客户端请求的逻辑名称；真实目标只进入授权诊断、日志、审计和 Trace。

每个路由目标包含供应商部署 ID、上游模型 ID、凭据引用与作用域、优先级、权重、单次超时、能力、可重试错误策略、价格版本和可选租户限制。

### 11.2 选择顺序

1. 组织与项目授权。
2. 请求能力。
3. 供应商扩展兼容性。
4. 配额与预算。
5. 停用与熔断状态。
6. 在最高可用优先级组内按权重选择。

随机源与时钟必须可注入，以便确定性测试。

### 11.3 重试与故障转移

- 总 Deadline 和最大尝试次数共同限制重试。
- 连接失败、指定超时、429 以及选定的 5xx/过载错误可以重试。
- 认证、授权、参数校验等不可重试 4xx 不得重试。
- 只有 `Retry-After` 落在剩余 Deadline 内时才遵守。
- 主要通过真实尝试维护被动健康与熔断；主动探测不得产生付费模型调用。
- 写出第一个客户端事件后立即锁定上游，禁止重试和故障转移。
- 不得为迁就备用目标而篡改供应商扩展。

一次客户端调用对应一个逻辑请求，但可有多个尝试记录；前序失败尝试产生的可识别成本也要记账。

## 12. 访问控制、凭据与 Secret

目标资源层级为 `Organization → Project → Virtual Keys / Route Policies / Quotas / Budgets / Usage Views`。

初始数据面使用平台级引导 Organization 和 Project。虚拟 Key 使用高熵随机值和非敏感前缀，明文只在创建时返回一次；持久化检索使用带服务端 Pepper 的 HMAC-SHA-256 指纹。

透明代理继续基于上游凭据执行现有限流。统一网关基于虚拟 Key 和 Project 执行限流、配额和预算，两套命名空间和存储不共享。

凭据支持平台凭据池和后续租户 BYOK。路由目标绑定凭据 ID 与作用域，Usage、成本和审计保留凭据归属。

`SecretResolver` 计划实现：

- `env://NAME`
- `file:///absolute/path`
- `db-encrypted://credential/<id>`

数据库实现采用 AES-256-GCM 信封加密。记录包含密文版本、Nonce、加密数据和 KEK ID。根密钥或 KEK 位于 PostgreSQL 之外；第一版由环境变量或文件 Key Provider 提供，未来可接 KMS/Vault。轮换时先写新版本，再停用旧版本。

Secret 明文只允许短期缓存，并按凭据版本失效。快照、日志、Trace、Metrics、错误、审计和 API 响应都不能包含明文。

推理应用服务通过 `SecretResolver` 获取仅限本次调用的短生命周期凭据租约，再交给目标连接器。连接器不能自行查询凭据 Repository，也不能持久保存明文。

删除凭据先停用依赖路由并发布新快照，再撤销上游 Secret，最后按保留策略清理可删除元数据；不可变审计记录继续保留。

## 13. 控制面与运行快照

PostgreSQL 是控制面资源的唯一事实来源，Redis 不是配置事实来源。

一次管理写操作在同一事务中校验 RBAC 与领域约束、写资源变更、写不可变审计事件、写 Outbox 事件。

快照编译器读取一致配置版本，解析引用并校验约束，生成不可变 `RuntimeSnapshot`。快照包含虚拟 Key 指纹、访问策略、模型目录、路由计划、部署元数据、价格版本引用和凭据引用，不含 Secret 明文。

数据面先校验新快照，再原子替换。进行中的请求继续持有旧快照，新请求使用新快照。错误快照不能覆盖 Last-Known-Good，并触发就绪详情、Metrics、日志和告警。

Redis 用于通知新版本；数据面仍定期对比权威版本，补偿丢失通知。

第一阶段由 YAML Snapshot Source 编译出同一种 `RuntimeSnapshot`。后续 PostgreSQL 控制面只替换配置来源和编译适配器，推理、路由与连接器不变。端口、日志、OTel 等部署设置始终由文件或环境变量管理。

## 14. 部署、Usage 与成本

生产目标为单区域多副本：数据面在内存持有快照和短期 Secret 缓存，PostgreSQL 提供控制面与 Ledger，Redis 提供分布式配额、熔断协同、配置通知和 Usage Stream。

路由配置不得逐请求查询 PostgreSQL。只有分布式限流、配额预留与对账、严格预算和共享熔断进入 Redis 热路径。本地可使用明确标注为非分布式、非严格的内存适配器；生产配置不满足后端要求时启动失败，不能静默退化。

调用上游前，网关估算输入 Token，结合最大输出，在 Redis 中原子预留请求数、Token 和预计成本，并创建 Pending 计量事实。严格预算模式下 Redis 不可用则拒绝请求。

每个尝试记录组织、项目、虚拟 Key、逻辑请求、Attempt、客户端模型、真实目标、凭据归属、原始/统一 Usage、Usage 是否存在、价格版本、结果和时间。客户端只看到最终结果的 Usage；内部 Ledger 记录所有已知计费尝试。Usage 缺失不能当作零。

生产环境先把 Usage 写入 Redis Stream。Metering Worker 至少一次消费，使用调用时固定的价格版本计算成本，幂等写入 PostgreSQL Ledger，并完成预留对账。事件 ID 唯一约束避免重复计费。

Ledger 不可更新；修正通过补偿记录。汇总报表是可重建 Projection。严格计量的 Redis 必须具备合适的持久化与复制；发布失败或 Pending 长期未完成必须触发严重告警和对账任务。

## 15. 错误、安全与可观测性

统一错误包含参数、认证、授权、模型不可用、能力不支持、限流、预算超额、上游超时、上游过载和内部错误。

Header 未提交时，出口适配器映射为入口协议原生状态码与错误结构。每个响应带稳定网关 Request ID，供应商 Request ID 只进入内部与授权诊断。SSE 开始后不能改变 HTTP 状态；协议支持时发送流内错误并结束流，绝不切换供应商。

安全约束：

- Base URL 只能由管理员配置，并校验协议、Host、DNS/IP、重定向和私网策略。
- `provider_options` 带版本、按 Schema 校验且限定供应商。
- 统一网关禁止任意 Header 透传。
- 限制正文、图片、JSON 深度、消息/工具数量、Schema、并发、流空闲和总 Deadline。
- 第一阶段不主动下载远程图片。
- Prompt 与输出默认不进入日志、Trace、Metrics 或审计。
- Metrics 禁止使用 API Key、租户、项目、模型、原始路径、Query 或资源 ID。
- Repository、缓存 Key、快照检索、管理 Command 和 Usage 查询都要覆盖租户隔离。
- 只有直接对端属于受信代理时才接受代理来源 Header。

Trace 在 Server Span 下建立认证/策略、模型解析、路由、配额和每次供应商尝试的子 Span。低基数 Metrics 可包含入口协议、规范化端点、连接器类型、结果、重试类型、熔断、配额拒绝、Usage 缺失、快照健康、Secret 失败和计量积压。

迁移必须保持现有透明代理的 OTel URL 归一化、Span 父子关系和低基数规则。

## 16. 测试与发布门禁

- 移动包前锁定透明代理路由、中间件顺序、限流、SSE Flush、Token、健康检查、OTel、日志和关闭行为。
- 增加 Import Boundary 测试或 Lint，阻止反向依赖。
- 为两种入口维护 JSON/SSE Golden Fixture，覆盖文本、图片、工具、结构化输出、停止原因、Usage 和错误。
- 使用官方 OpenAI 与 Anthropic SDK 执行本地黑盒兼容测试。
- 覆盖任意分块、CRLF、多行 SSE、未知事件、局部工具 JSON、无尾分隔符、提前断开和最终 Usage 缺失。
- 所有连接器运行同一套 `httptest.Server` 契约测试，默认不访问真实供应商。
- 注入时钟、随机源和 Transport，测试加权选择、错误分类、Deadline、`Retry-After`、故障转移和熔断。
- 对统一映射和流解析执行 Fuzz/Property Test；验证取消、背压、Goroutine 清理和首事件 Flush。
- 集成测试验证 PostgreSQL、Redis、Usage 幂等、Ledger 补偿、预留对账、Outbox、快照原子性、租户隔离、SSRF、虚拟 Key 和 Secret 轮换。

发布前执行受影响包测试、`go test ./...`、必要的 `go test -race ./...`、构建、`git diff --check`、协议兼容和流式契约测试。性能必须对比受控基线，流式实现不得缓存完整响应。透明代理测试不能通过修改预期掩盖行为变化。

## 17. 分阶段交付

### 阶段一：DDD 基础与统一推理内核

- 补齐透明代理特征测试。
- 建立 DDD 分层与 Wire 组装。
- 小步移动现有技术包，每一步保持构建和回归通过。
- 新增两个 `/v1` 入口、统一请求/响应/事件、虚拟 Key、YAML Snapshot、能力矩阵和供应商扩展。
- 实现四类连接器、加权路由、优先级故障转移、有限重试和本地熔断。
- 通过 `env://` 与 `file://` 提供平台凭据。

阶段一禁止大爆炸式交付，依次拆成三份实施计划：

1. **阶段一 A：DDD 基础迁移**——补齐特征测试、建立分层与 Wire、小步移动现有包，功能行为保持不变。
2. **阶段一 B：统一协议内核**——实现领域类型、应用用例、两个入口协议、虚拟 Key、YAML Snapshot 和 Fake Connector，先验证完整纵向链路。
3. **阶段一 C：供应商与路由**——逐个加入四类真实连接器、能力矩阵、供应商扩展、加权路由、重试和熔断。

### 阶段二：生产级数据面与可靠计量

- 增加 PostgreSQL、Redis、分布式限流、配额、共享熔断和配置通知。
- 增加 Redis Stream、Metering Worker、价格版本、Ledger、对账和预算。
- 增加 `db-encrypted://`、外部根密钥和轮换。
- 在管理 API 前，通过复用凭据应用服务的 CLI 安全地新增、轮换、停用和查看加密凭据元数据。
- 验证多副本与后端故障。

### 阶段三：多租户控制面

- 实现 Organization、Project、成员、Service Account、虚拟 Key、RBAC 和审计。
- 实现管理 API、Repository、Migration、Transaction 和 Outbox。
- 实现 PostgreSQL 快照编译、版本发布、平台凭据池管理和 Usage/成本查询 Projection。

### 阶段四：生态扩展

- 启用租户 BYOK。
- 增加 OpenAI Responses API。
- 定义外部供应商 Adapter RPC。
- 增加供应商、Secret 后端，以及确有需求时的动态路由和多区域部署。

每个阶段和子阶段编码前都要有自己的细化设计与实施计划。本文批准后，下一份计划只聚焦“阶段一 A：DDD 基础迁移”。

## 18. 总体验收标准

1. 现有透明代理客户端无行为回归。
2. 官方 OpenAI 与 Anthropic SDK 在能力矩阵内通过新入口兼容测试。
3. 四类连接器通过统一契约测试。
4. 不支持的组合返回明确的协议原生错误。
5. 流式响应在上游结束前转发首事件，正确传播取消与背压，无 Goroutine 泄漏。
6. 路由、重试和熔断遵守规则，流提交后绝不故障转移。
7. 多副本执行分布式配额并幂等记录尝试级 Usage。
8. 控制面发布原子快照，错误快照不能替换 Last-Known-Good。
9. 平台与租户凭据作用域正确、可加密或引用、可轮换、可脱敏、可审计。
10. domain 包不存在基础设施或传输依赖。

## 19. 协议参考

- [OpenAI Chat API](https://developers.openai.com/api/reference/resources/chat)
- [Anthropic Streaming Messages](https://platform.claude.com/docs/en/build-with-claude/streaming)
- [Anthropic API Errors](https://platform.claude.com/docs/en/api/errors)
- [Gemini Function Calling](https://ai.google.dev/gemini-api/docs/function-calling)
- [Gemini Structured Output](https://ai.google.dev/gemini-api/docs/structured-output)

这些外部文档是实现输入，不是稳定内部契约。供应商协议变化必须隔离在带版本的 interfaces 与 infrastructure 适配器中。
