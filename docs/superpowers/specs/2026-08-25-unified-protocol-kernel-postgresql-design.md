# 统一协议内核与 PostgreSQL 控制面设计

## 1. 文档目的

本文定义多厂商统一网关 Phase 1B 的详细设计。在 Phase 1A 已完成的 DDD 基础结构上，引入多租户控制面、PostgreSQL 持久化、运行时配置快照、OpenAI/Anthropic 兼容入口、统一语义内核和 Fake Connector。

本阶段的目标不是接入真实模型厂商，而是打通一条可验证的完整纵向链路：通过控制面创建组织、项目、虚拟密钥和模型配置，再分别使用 OpenAI 与 Anthropic 兼容协议调用同一个 Fake Deployment，并获得各自协议兼容的普通响应或 SSE 流式响应。

## 2. 已确认约束

- 现有 `/openai/*` 与 `/anthropic/*` 透明代理是独立能力，路由、限流、统计、可观测和请求透传行为均不得改变。
- 新架构采用 DDD 模块化单体，代码组织参考 `app-service`，依赖方向严格受控。
- 系统从第一版开始支持多个 Organization、Project 和 Virtual Key，不设计单租户过渡模型。
- PostgreSQL 是当前事实来源；领域仓储和数据库方言必须允许以后增加 MySQL、SQLite 实现。
- 请求热路径不查询数据库，只读取进程内不可变快照。
- 第一版提供资源化控制面 HTTP API；CLI 只负责数据库迁移和必要的启动引导。
- 第一版启用平台凭据池，数据模型同时保留 Organization、Project 级凭据作用域；租户 BYOK 后续开放。
- 数据库中的 Provider 凭据必须加密，虚拟密钥不得以明文保存。
- Phase 1B 只实现 Fake Connector；真实厂商、加权路由、重试、熔断进入 Phase 1C。
- 禁止使用 Git worktree；本功能在 `feat/unified-protocol-kernel` 分支完成。

## 3. 范围

### 3.1 本阶段包含

- Organization、Project、Virtual Key 的多租户领域模型。
- Provider、ProviderCredential、Deployment、ModelAlias、RouteTarget 配置模型。
- PostgreSQL 连接、GORM 持久化实现、事务适配器和 `gormigrate` 版本迁移。
- 资源化控制面 API 和独立控制面鉴权。
- Provider 凭据信封加密、虚拟密钥单次展示和哈希存储。
- `ConfigRevision`、版本轮询、进程内唤醒和不可变 `RuntimeSnapshot` 原子发布。
- `POST /v1/chat/completions` 的 OpenAI 兼容 JSON/SSE。
- `POST /v1/messages` 的 Anthropic 兼容 JSON/SSE。
- 统一请求、统一响应、统一流事件和能力校验。
- Fake Connector 的完整纵向测试。
- PostgreSQL 故障隔离、旧快照保留和透明代理回归保护。

### 3.2 本阶段不包含

- 真实 OpenAI、Anthropic 或其他第三方 Connector。
- 多目标加权选择、自动故障转移、重试、熔断和跨地域路由。
- 租户自助录入 BYOK 的产品能力。
- 管理员账号体系、RBAC、审计日志和控制面 UI。
- 用量计费、配额、账单、历史请求存储和 Prompt/Completion 留存。
- Redis、消息队列、PostgreSQL `LISTEN/NOTIFY` 或跨进程主动推送。
- 两个现有透明代理协议之间的格式转换。

## 4. 总体架构

系统保持一个部署单元，但明确分成三个区域：

```text
┌──────────────────────────────────────────────────────────────┐
│                         HTTP Interfaces                      │
│                                                              │
│  透明代理入口          控制面资源 API          数据面兼容入口 │
│  /openai/*             /v1/organizations...   /v1/chat/...   │
│  /anthropic/*          /v1/providers...       /v1/messages   │
└──────────┬────────────────────┬──────────────────────┬────────┘
           │                    │                      │
    现有代理 Application   控制面 Use Case       Gateway Use Case
                                │                      │
                                └──────────┬───────────┘
                                           │
                                      Domain 模型
                                           │
                  ┌────────────────────────┴────────────────────┐
                  │              Infrastructure                 │
                  │ PostgreSQL/GORM  加密  Snapshot  Connector  │
                  └─────────────────────────────────────────────┘
```

固定依赖方向为：

```text
Interfaces → Application → Domain
Infrastructure ───────────→ Domain
DI 负责把接口与实现组装起来
```

Domain 不依赖 HTTP DTO、GORM、PostgreSQL、配置框架或厂商 SDK。Interfaces 只负责协议解析、校验和编码；Application 编排用例与事务；Infrastructure 实现仓储、密码学、快照加载和 Connector。

“控制面”和“数据面”是模块、权限及运行时边界，不通过 `/admin` 路径表达。所有新 API 使用版本前缀 `/v1`，控制面采用资源化路径。

## 5. 领域划分与模型

### 5.1 租户与访问上下文

#### Organization

系统的一级租户边界，包含 UUIDv7、名称、状态和审计时间。停用 Organization 后，其下所有 Project 的数据面访问立即失效。

#### Project

归属一个 Organization，是数据面授权、模型别名和后续用量隔离的基本边界。ModelAlias 名称在同一 Project 内唯一。

#### VirtualKey

归属一个 Project，包含名称、密钥哈希、展示前缀、末四位、状态、可选过期时间和审计时间。完整密钥仅在创建成功时返回一次。

数据面只允许通过 Virtual Key 确定 Organization 与 Project。客户端不能通过 Header、Query 或请求正文覆盖租户上下文。

### 5.2 Provider 与路由配置

#### Provider

描述厂商及其 Connector 类型，例如 `fake`、`openai`、`anthropic`。Provider 只保存非敏感定义，不直接持有密钥。

#### ProviderCredential

保存加密后的厂商凭据，作用域为以下三种之一：

- `platform`：Organization 和 Project 均为空。
- `organization`：Organization 非空，Project 为空。
- `project`：Project 非空，所属 Organization 由 Project 推导。

Phase 1B 的管理用例只允许创建和使用 `platform` 凭据，但持久化模型和授权规则从第一版保留全部作用域。

#### Deployment

代表某 Provider 下一个可调用的模型部署，包含名称、上游模型标识、Connector 类型、作用域、凭据引用、状态和能力声明。Fake Deployment 可以不关联凭据。

能力声明至少包含：文本、图片输入、工具调用、结构化输出和流式响应。

#### ModelAlias

Project 可见的逻辑模型名称。数据面请求中的 `model` 始终先在当前 Project 内解析为 ModelAlias，客户端不能直接选择 ProviderCredential 或 Deployment。

#### RouteTarget

连接 ModelAlias 与 Deployment，包含状态、优先级和权重。Phase 1B 要求每个可用 Alias 恰好有一个启用的 Fake Target；结构上允许多个 Target，真正的加权与故障转移在 Phase 1C 实现。

### 5.3 ConfigRevision

`ConfigRevision` 是单调递增的配置版本计数器，不是业务 ID。其数据库记录使用 UUIDv7 主键，版本值使用整数。任何影响数据面有效配置的控制面写操作，都必须在同一数据库事务中递增版本。

## 6. UUIDv7 与持久化基础模型

参考项目采用领域模型与 GORM Entity 分离的方式，本项目沿用这一结构。

- 所有领域实体主键和逻辑关联 ID 均使用 `github.com/google/uuid.UUID`。
- 所有新业务 ID 必须由领域构造器调用 `uuid.NewV7()` 生成。
- PostgreSQL 使用原生 `uuid` 列保存主键和逻辑关联 ID，不保存 UUID 字符串。
- 不使用 `uuid_generate_v7()` 数据库默认值，避免依赖 PostgreSQL 扩展并消除双重生成来源。
- 所有创建入口，包括迁移、测试夹具和内部引导，都必须显式生成 UUIDv7。
- 持久化 Entity 统一嵌入 `BaseEntity`，字段为 `ID`、`CreatedAt`、`UpdatedAt`。
- 领域模型不嵌入 GORM 标签；Repository 负责领域模型与持久化 Entity 的双向转换。
- 当前 PostgreSQL 方言把逻辑 UUID 映射为原生 `uuid`。未来 MySQL、SQLite Adapter 负责映射到各自的等价二进制或 UUID 类型，领域接口不变。

时间统一使用 UTC。API 输出 RFC 3339 时间，数据库字段不承担业务时区转换。

## 7. 仓储、事务与数据库可替换性

Domain 为聚合根定义仓储接口，Application 只依赖这些接口及 `TransactionManager`。Infrastructure 提供 GORM 实现，并通过事务上下文让同一用例内的多个 Repository 共享事务。

数据库层禁止创建外键约束。Entity 只保存 UUID 逻辑关联字段和普通索引，不声明 GORM Relationship、Association、级联保存或级联删除。父资源存在性、租户归属、凭据与 Deployment 作用域、Alias 与 RouteTarget 引用状态等跨表约束，全部由 Application Service 在同一事务内显式查询并校验。数据库继续承担非空、唯一索引和字段类型等单表约束。

核心接口按职责拆分：

- OrganizationRepository
- ProjectRepository
- VirtualKeyRepository
- ProviderRepository
- ProviderCredentialRepository
- DeploymentRepository
- ModelAliasRepository
- ConfigRevisionRepository
- RuntimeConfigReader：以一致性视图加载编译快照所需的读模型
- TransactionManager：在 Application 中包裹原子写操作

Domain Repository 返回领域模型，不泄露 `*gorm.DB`、数据库 Entity、分页 SQL 或 PostgreSQL 错误。

首版仅注册 PostgreSQL Driver。数据库初始化层提供方言选择点，但不提前实现 MySQL、SQLite Driver。迁移和查询禁止依赖 `jsonb`、数组、序列、数据库生成 UUID 或 PostgreSQL 通知。复杂能力配置以应用可校验的序列化文本保存；不同方言的 UUID 和索引差异由 Infrastructure 迁移层处理。

所有控制面写用例必须在提交前校验聚合约束、租户作用域和引用完整性，正常 API 写入不能产生无法编译的配置。快照构建阶段仍会重复校验，用于防御手工改库、旧版本数据或并发异常。

## 8. 控制面 API

### 8.1 路径

首版至少提供以下资源的创建、查询、列表、更新和启停能力：

```text
/v1/organizations
/v1/organizations/{organization_id}/projects
/v1/projects/{project_id}
/v1/projects/{project_id}/virtual-keys
/v1/virtual-keys/{virtual_key_id}
/v1/providers
/v1/providers/{provider_id}
/v1/provider-credentials
/v1/provider-credentials/{credential_id}
/v1/deployments
/v1/deployments/{deployment_id}
/v1/model-aliases
/v1/model-aliases/{model_alias_id}
```

首版不提供物理删除。停用操作使用状态更新；被引用资源不能被修改为破坏引用完整性的状态。ProviderCredential 的查询与列表响应永远不返回密文、加密数据密钥或任何可恢复秘密的字段。

控制面响应使用统一结构。成功创建影响数据面的配置时返回本次提交对应的 `config_revision`。失败响应至少包含：

```json
{
  "error": {
    "code": "stable_error_code",
    "message": "可安全展示的中文或协议约定消息",
    "request_id": "请求标识"
  }
}
```

### 8.2 控制面认证

Phase 1B 使用独立全局管理令牌：

- 令牌只从进程环境变量或 Secret 注入，不进入数据库和配置示例明文。
- 客户端通过 `Authorization: Bearer` 传递。
- 服务启动时保存令牌摘要，请求认证使用固定时间比较。
- 请求日志、Trace、Metric 和错误响应不记录令牌。
- 认证能力通过 `ControlPlaneAuthorizer` 接口提供，后续 RBAC 替换实现时不改变 Application Use Case。

管理员账号、租户管理员和审计日志不在 Phase 1B 内。

## 9. Virtual Key 安全

Virtual Key 使用密码学安全随机源生成，格式带固定产品和版本前缀，密钥主体至少包含 256 bit 随机熵。

- 创建成功后完整密钥只在 HTTP 响应中出现一次。
- 数据库保存完整密钥 SHA-256 哈希、展示前缀和末四位，不保存可恢复明文。
- RuntimeSnapshot 以完整密钥哈希为索引，值为只读租户访问上下文。
- OpenAI 入口从 `Authorization: Bearer` 读取 Virtual Key。
- Anthropic 入口优先从 `x-api-key` 读取，缺失时兼容 Bearer。
- 鉴权完成后从内部上游请求上下文中移除 Virtual Key，任何 Connector 都不得把它作为上游凭据使用。
- 禁用、过期、Project 停用或 Organization 停用都返回对应协议的认证错误。

Virtual Key 与现有透明代理 API Key 的处理链路相互独立，不改变现有限流缓存和密钥提取行为。

## 10. Provider 凭据信封加密

每条 ProviderCredential 使用独立数据密钥，避免直接用长期主密钥加密所有业务密文。

写入流程：

```text
生成 256 bit 数据密钥
  → 使用数据密钥和 AES-256-GCM 加密规范化凭据 JSON
  → 使用当前版本主密钥包装数据密钥
  → 保存主密钥版本、两个 nonce、加密数据密钥和凭据密文
```

凭据 ID、Provider ID 和作用域信息作为附加认证数据参与校验，防止密文被复制到其他记录后继续解密。

运行时规则：

- 主密钥以“版本号 → Base64 编码 256 bit 密钥”的 Keyring 形式通过 Secret 注入。
- 当前写入版本必须存在；旧版本可保留用于读取和轮换。
- RuntimeSnapshot 只保存密文封装，不长期保存解密后的凭据。
- Connector 即将创建上游请求时，通过 `CredentialCipher` 解包并解密；凭据仅存在于该次调用的短生命周期对象中。
- 未知密钥版本、GCM 校验失败或密文格式非法都必须失败关闭，不得尝试明文解释。
- 日志只允许记录 Credential ID、Provider ID 和错误分类，不记录密文、nonce 或解密内容。

Phase 1B 实现本地 AES-256-GCM Keyring。以后接入 KMS 或 Vault 时替换 `CredentialCipher`/主密钥包装实现，领域与数据库读取流程不变。

## 11. RuntimeSnapshot 与刷新机制

### 11.1 快照内容

不可变 RuntimeSnapshot 至少包含：

- `revision`
- Virtual Key 哈希到 Organization/Project 访问上下文的索引
- Project 与 ModelAlias 到编译路由计划的索引
- Deployment、能力声明、Connector 类型和加密凭据封装
- 快照构建时间，仅用于诊断

快照发布后不得修改内部 Map、Slice 或对象。Application 通过只读 SnapshotProvider 获取当前指针。

### 11.2 刷新流程

```text
轮询 ConfigRevision
  → 发现版本大于当前快照
  → 在可重复读的一致性只读事务中加载全部有效配置
  → 校验租户关系、引用、作用域和能力
  → 编译新的不可变 RuntimeSnapshot
  → 使用 atomic.Pointer 一次性发布
```

控制面事务提交后向本进程刷新器发送非阻塞唤醒信号，以缩短本实例生效时间；其他实例仍通过版本轮询发现变化。系统不依赖 PostgreSQL `LISTEN/NOTIFY`。

控制面和数据面采用最终一致性：控制面成功响应表示配置已持久化并获得 Revision，不表示所有实例已经发布该 Revision。正常情况下，本实例由唤醒信号立即刷新，其他实例最迟在“轮询间隔 + 单次加载超时”内尝试刷新。Virtual Key 禁用、资源停用和路由修改同样遵循这一传播窗口；Phase 1B 不提供同步等待所有实例生效的接口。

构建失败时：

- 不替换当前快照。
- 记录版本号、错误分类和安全的资源 ID。
- 按配置退避后继续轮询。
- 如果已有快照，数据面继续使用最后一份可用快照。
- 如果从未成功加载快照，数据面返回协议兼容的 `503`。

快照刷新是内存缓存方案，Phase 1B 不引入 Redis。未来如需跨进程主动发布，可在 SnapshotPublisher/Loader 边界增加实现。

## 12. 统一协议语义内核

OpenAI 与 Anthropic HTTP Adapter 分别解析外部协议，禁止先把一种外部协议转换成另一种外部协议。两者只在统一语义模型处汇合：

```text
OpenAI DTO ──→ OpenAI Decoder ──┐
                                ├─→ UnifiedRequest → Gateway Use Case
Anthropic DTO → Anthropic Decoder┘

GatewayResult → 与入口匹配的普通响应或 SSE Encoder
```

### 12.1 UnifiedRequest

统一请求至少表达：

- 请求模型别名。
- system、developer、user、assistant 等消息角色。
- 文本、图片、工具调用和工具结果内容块。
- Tool 名称、描述和输入 JSON Schema。
- 结构化输出约束。
- temperature、top_p、max_tokens、stop 等公共生成参数。
- stream 标志和安全的请求元数据。

可选生成参数必须使用能区分“未提供”和“显式零值”的 Optional 语义，禁止在协议 Decoder 中擅自写入厂商默认值。

图片内容只表达媒体类型和来源语义，不在 Phase 1B 下载远程文件。Fake Connector 仅验证和确认图片输入。未知协议字段不透传给 Connector；将来确需厂商扩展时通过显式命名、能力校验的扩展结构增加。

### 12.2 UnifiedResponse 与 Usage

普通响应包含统一的响应 ID、模型别名、内容块、停止原因和 Usage。Usage 至少归一化：

- InputTokens
- OutputTokens
- CacheReadInputTokens
- CacheWriteInputTokens

各协议 Encoder 负责映射字段名、总量和停止原因，不让厂商字段进入 Domain。

### 12.3 统一流事件

流式 Connector 通过可取消且具有背压的 `Recv(ctx)` 抽象产生以下强类型事件：

```text
ResponseStart
ContentBlockStart
TextDelta
ToolCallStart
ToolArgumentsDelta
ContentBlockStop
UsageUpdate
ResponseFinish
StreamError
```

事件必须满足合法顺序。客户端断开或请求 Context 取消时，Encoder 停止读取并取消 Connector。HTTP ResponseWriter 包装在底层支持时必须继续实现并转发 `http.Flusher`，首个 SSE 事件不得等待上游流结束才发送。

OpenAI SSE 与 Anthropic SSE Encoder 分别生成自身协议的事件、终止标志和错误事件。

## 13. Gateway 数据面执行流程

```text
解析入口协议
  → 提取并哈希 Virtual Key
  → 从当前 Snapshot 获得 Organization/Project
  → 在 Project 内解析 ModelAlias
  → 获取唯一启用的 RouteTarget 和 Deployment
  → 校验请求所需能力
  → 调用 Connector
  → 使用入口协议 Encoder 返回 JSON 或 SSE
```

请求热路径不得调用 GORM、SQL 或控制面 Repository。Phase 1B 不执行自动重试；一次请求只调用选定的 Fake Deployment 一次。

## 14. Fake Connector

Fake Connector 是确定性测试实现，不访问网络，也不读取真实厂商凭据。它必须支持：

- 文本输入和多轮消息。
- 图片输入确认。
- 工具定义、工具调用和工具结果。
- 结构化输出约束。
- 普通响应与可控分块的流式响应。
- 确定性的停止原因和 Usage。
- Context 取消和客户端断开。
- 通过测试参数触发合法错误和流中错误。

同一 UnifiedRequest 在两个入口下应进入同一个 Gateway Use Case 和 Fake Connector；最终由不同 Encoder 产生各自兼容格式。

## 15. 错误模型

Application 使用稳定的统一错误分类，至少包括：

- InvalidRequest
- AuthenticationFailed
- PermissionDenied
- ResourceNotFound
- Conflict
- CapabilityUnsupported
- GatewayNotReady
- ConnectorFailed
- InternalError

Interfaces 负责映射：

- OpenAI 入口返回 OpenAI 风格 `error` 结构和对应 HTTP 状态。
- Anthropic 入口返回 Anthropic 风格 `type: error` 结构和对应 HTTP 状态。
- 控制面返回统一 `code/message/request_id` 错误结构。
- SSE 开始前的错误使用普通 HTTP 错误响应。
- SSE 开始后的错误使用入口协议允许的错误事件并关闭流。

任何响应不得泄露 SQL、GORM 错误、堆栈、完整密钥、凭据密文或内部地址。日志可以关联安全的 UUID 和 request ID。

## 16. 故障隔离与生命周期

PostgreSQL 是统一网关控制面的事实来源，但不是现有透明代理的启动硬依赖。

- PostgreSQL 启动时不可用，不阻止 HTTP Server、Dashboard 和透明代理启动。
- 控制面在数据库不可用时返回 `503`。
- 数据面在没有首份有效快照时返回入口协议兼容的 `503`。
- 已有快照后 PostgreSQL 中断，数据面继续使用最后一份可用快照。
- PostgreSQL 恢复后刷新器自动重连并加载新版本。
- 现有 `/healthz` 与 `/readyz` 保持生命周期语义，不因新控制面数据库状态而改变。
- 关闭时先停止刷新器和新后台任务，再关闭 HTTP Server、数据库连接与可观测组件；各步骤失败不跳过后续清理。

统一网关的数据库与快照状态通过结构化日志和低基数指标暴露，不在 Phase 1B 增加新的公共健康端点。

## 17. 配置

新增配置分组至少包含：

- Database：driver、DSN、连接池上限、连接生命周期和连接超时。
- ControlPlane：enabled、管理令牌来源。
- CredentialEncryption：当前主密钥版本和版本化 Keyring。
- Gateway：enabled、快照轮询间隔、加载超时、失败退避。

首版只接受 `postgres` Driver。配置仍遵循“进程环境变量 > `.env` > `config.yaml` > 默认值”。敏感值只允许通过进程环境变量或 `.env` 注入；`config.yaml` 与 `.env.example` 仅提供无敏感占位说明。

新增配置时同步更新配置结构、Viper 默认值、环境变量绑定、`config.yaml`、`.env.example` 和配置测试。非法 DSN、时间、数字、Keyring JSON、Base64 或非 256 bit 主密钥必须导致对应组件明确失败，不能静默回退。

这里的配置文件和环境变量仍不支持热更新，修改后需要重启。只有通过控制面 API 写入数据库的租户、凭据、Deployment、Alias 和路由资源由 RuntimeSnapshot 动态刷新。

Gateway 显式关闭时不创建数据库连接、刷新 goroutine 或统一网关路由依赖，现有代理仍正常工作。

## 18. 数据库迁移 CLI

数据库结构通过固定版本号的 `gormigrate` 迁移维护。独立 CLI 至少提供：

- `up`：迁移到最新版本。
- `down`：回滚上一版本或指定安全版本。
- `status`：显示当前与可用版本。

服务启动不自动修改数据库结构。迁移必须提供可测试的回滚逻辑，不 Seed 默认管理令牌、虚拟密钥、Provider 凭据或弱口令。用于本地纵向验证的 Organization、Project 和 Fake 配置通过控制面 API 创建。

## 19. 测试策略

### 19.1 Domain 与 Application

- 聚合构造器生成 UUIDv7。
- Organization、Project、作用域、状态和引用约束。
- 控制面 Use Case 在同一事务中保存配置并递增 Revision。
- Virtual Key 只返回一次，Repository 只接收哈希表示。
- 能力矩阵和 Alias/Target 解析。
- Application 不依赖 HTTP、GORM 或具体数据库。

### 19.2 安全

- ProviderCredential 加密后数据库 Entity 不含明文片段。
- 不同记录交换密文后因附加认证数据不匹配而失败。
- nonce、密文或加密数据密钥被篡改时失败关闭。
- 未知主密钥版本、错误主密钥和主密钥轮换。
- Virtual Key 哈希、禁用、过期、日志脱敏和 Header 移除。

### 19.3 PostgreSQL 集成

- 使用真实 PostgreSQL 执行迁移、回滚和仓储集成测试，不使用 SQLite 代替 PostgreSQL 语义。
- 验证所有业务主键和逻辑关联 ID 为原生 UUID，且由应用生成的主键为 UUIDv7。
- 验证业务表不存在数据库外键约束，GORM Entity 不声明 Relationship。
- 验证事务回滚时业务写入与 ConfigRevision 均不生效。
- 验证一致性读取不会发布引用不完整的快照。

集成测试通过测试专用 PostgreSQL DSN 或受控容器运行，测试数据库不得复用开发或生产库。

### 19.4 协议与 HTTP

- OpenAI/Anthropic 请求 Decoder Golden Test。
- OpenAI/Anthropic 普通响应和 SSE Encoder Golden Test。
- 文本、图片、工具、结构化输出和 Usage 映射。
- 字段缺失与显式零值的差异。
- 完整 Fake Connector 纵向调用。
- 首个 SSE 事件在流结束前 Flush。
- 任意网络分块、客户端断开、Context 取消和流中错误。
- 控制面认证、资源边界、单次密钥展示和错误结构。

### 19.5 并发、故障与回归

- Snapshot 原子替换和高并发读取执行 race 测试。
- 新快照构建失败时保留上一版本。
- 初次无快照、数据库中断和恢复。
- Gateway 关闭时不创建后台资源。
- 现有 `/openai/*`、`/anthropic/*`、Dashboard、健康检查、限流、Token Usage、SSE 和 OTel 契约测试全部保持通过。

## 20. 验收标准

Phase 1B 完成必须同时满足：

1. 迁移 CLI 能在空 PostgreSQL 数据库执行 `up`，创建 UUID 类型表，并能执行已定义的安全回滚。
2. 通过控制面 API 可以创建 Organization、Project、Virtual Key、Fake Provider、Fake Deployment、ModelAlias 和唯一 RouteTarget。
3. 完整 Virtual Key 只在创建响应中出现一次，数据库、日志、Trace 和 Metric 中不存在明文。
4. ProviderCredential 数据库记录不含明文，篡改密文或密钥版本后不能解密。
5. 控制面提交返回 ConfigRevision；刷新器能编译并原子发布对应快照。
6. OpenAI 兼容接口能使用 Virtual Key 通过同一个 Fake Deployment 返回兼容 JSON 和 SSE。
7. Anthropic 兼容接口能使用同一 Project 的 Virtual Key 通过同一个 Fake Deployment 返回兼容 JSON 和 SSE。
8. 文本、图片、工具调用、工具结果、结构化输出和 Usage 在统一语义内核中有自动化测试。
9. PostgreSQL 中断后，透明代理始终可用；已有快照的数据面继续可用；无快照时返回安全的协议兼容 `503`。
10. `go test ./...`、相关 `go test -race`、构建、`go vet` 和架构依赖测试全部通过。

## 21. 后续阶段接口预留

Phase 1B 只预留稳定边界，不提前实现后续策略：

- 新增真实 Connector 只实现统一 Connector 接口和能力声明。
- 加权路由、重试、熔断在 RouteTarget/RoutePlan 之上扩展。
- MySQL、SQLite 通过方言、迁移和 Repository Adapter 增加。
- KMS/Vault 通过 CredentialCipher/KeyWrapper 增加。
- RBAC 替换 ControlPlaneAuthorizer，不改变控制面 Use Case。
- 跨进程快照发布替换 SnapshotPublisher/Loader，不改变请求热路径。

这些预留不构成 Phase 1B 的实现要求。
