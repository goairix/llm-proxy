# AGENTS.md

本文件适用于整个仓库，供在本项目中工作的编码代理使用。以当前源码、`go.mod` 和 `Dockerfile` 为事实来源；README 和历史设计文档可能滞后。

## 项目概览

`llm-proxy` 是一个 Go HTTP 反向代理，为 OpenAI 和 Anthropic API 提供路径转发、按 API Key 限流、OpenTelemetry traces/metrics、结构化日志、Token 统计和内嵌 Dashboard。项目不做两种协议之间的格式转换。

- Module：`github.com/goairix/llm-proxy`
- Go：1.25（Docker 构建镜像当前为 Go 1.25.4）
- HTTP：标准库 `net/http`、`httputil.ReverseProxy`
- 配置：Viper + `config.yaml` + godotenv
- 日志：Zap + `file-rotatelogs`
- 限流：`golang.org/x/time/rate`
- 可观测：OpenTelemetry Go SDK + OTLP/HTTP exporter + `otelhttp`
- 前端：单个内嵌 HTML 文件，无独立构建步骤

## 常用命令

在仓库根目录执行：

```bash
# 全量测试
go test ./...

# 指定包或用例
go test ./internal/interfaces/http/middleware/
go test ./internal/interfaces/http/middleware/ -run TestLoggingMiddlewareErrorLevels

# 涉及 sync.Map、atomic 或并发逻辑时
go test -race ./...

# 构建与运行；运行时从当前目录读取 config.yaml 和可选的 .env
go build -o /tmp/llm-proxy ./cmd/proxy
go run ./cmd/proxy

# 容器构建
docker build -t llm-proxy .
```

修改 Go 文件后运行 `gofmt`。只有依赖确实发生变化时才运行 `go mod tidy`，并检查 `go.mod`、`go.sum` 的差异。

## Git 工作流

- 禁止使用 `git worktree` 或任何基于 worktree 的隔离流程；所有开发都在普通 Git 分支中完成。
- 分支名使用 `<类型>/<说明>` 格式，说明部分使用简短、清晰的小写英文单词并以连字符分隔，例如 `feat/unified-gateway`、`bugfix/sse-flush`、`hotfix/credential-leak`。
- 类型按任务性质选择，常用值包括 `feat`、`bugfix`、`hotfix`、`refactor`、`docs`、`test`、`chore`。
- 新功能开发必须先从正确的基线分支创建独立的 `feat/*` 分支，禁止直接在 `main`、`master` 或其他长期分支上开发。
- Bug 修复使用 `bugfix/*`；需要紧急上线的生产修复使用 `hotfix/*`。除非用户明确指定，不混用分支类型。
- 开始修改前检查当前分支和工作区状态；若存在用户未提交的改动，必须保留并避免覆盖，不得为了切分支而清理或丢弃这些改动。

## 请求链路与路由

代理请求的实际处理顺序是：

```text
客户端 → OTel Server → Logging → Stats/Token Observer → RateLimiter → ReverseProxy → OTel Transport → 上游 API
```

- `/openai/*`：去掉 `/openai` 前缀后转发到 OpenAI `base_url`。
- `/anthropic/*`：去掉 `/anthropic` 前缀后转发到 Anthropic `base_url`。
- `/healthz`：存活检查；不经过代理日志、统计、限流和 OTel proxy handler。
- `/readyz`：就绪检查；开始监听前/关闭开始后返回 503，同样不经过代理链。
- `/`：Dashboard handler。由于它注册为 ServeMux 的根模式，当前还会接住所有未匹配路径；不要默认未知路径一定返回 404。
- Dashboard 不经过代理请求的日志、统计和限流中间件。

中间件的包裹顺序有意让 OTel span 覆盖完整代理请求、Logging 读取 span context、Stats 统计被限流的请求，并让 RateLimiter 在访问上游前拒绝请求。调整顺序前应更新相应测试并检查 trace parent、SSE、Token 和统计语义。

## 目录职责

- `cmd/proxy/main.go`：加载 `config.yaml`、调用 DI 组装根、监听信号并依次关闭 HTTP Server 与 telemetry。
- `internal/domain`：统一网关的供应商无关业务规则；当前透明代理不属于领域模型。
- `internal/application/runtime`：Readiness、Version 和透明代理 Usage Observer 等稳定应用契约。
- `internal/interfaces/http/handler/dashboard`：原子统计、Dashboard 数据注入、页面响应和内嵌 HTML。
- `internal/interfaces/http/handler/health`：存活与就绪 HTTP 适配器。
- `internal/interfaces/http/middleware`：访问日志、API Key 提取、令牌桶限流、请求与 Token 统计。
- `internal/interfaces/http/router`：ServeMux 路由和中间件顺序组装，不管理监听生命周期。
- `internal/infrastructure/config`：配置结构、默认值、YAML、`.env` 和进程环境变量加载；配置文件缺失时使用默认值，文件存在但无效时启动失败。
- `internal/infrastructure/logger`：控制台日志和按天轮转的 JSON 文件日志。
- `internal/infrastructure/observability`：解析 OTel 标准环境变量、管理 OTLP/HTTP exporter/provider 生命周期、HTTP server/client 插桩和低基数业务 metrics。
- `internal/infrastructure/proxy`：创建两个透明反向代理，修改目标地址、Host、Path 和 RawPath。
- `internal/infrastructure/proxy/tokenusage`：识别五个生成端点，增量解析 JSON/SSE usage 并归一化 OpenAI/Anthropic Token 字段。
- `internal/infrastructure/server/http`：标准库 HTTP Server 的监听、Readiness 与 Shutdown 生命周期。
- `internal/di`：Google Wire Provider、模块和唯一应用组装根；生成的 `wire_gen.go` 纳入版本管理。
- `config.yaml`：带注释的 YAML 运行配置示例，也是本地直接运行时读取的默认路径。
- `.env.example`：全部 `LLM_PROXY_` 环境变量的非敏感示例；实际 `.env` 不入库。

生产代码依赖方向固定为 `interfaces → application → domain`；`infrastructure` 实现内层端口，可以依赖 `application` 或 `domain`，但不能依赖 `interfaces`。只有 `internal/di` 可以同时引用接口层和基础设施层。`internal/architecture/dependencies_test.go` 自动检查这些边界。

## 关键实现约束

### 数据库与关联完整性

- 所有业务代码必须在构造领域实体时生成 UUIDv7 并显式写入数据库；`BaseEntity.ID` 保留 `uuid_generate_v7()` 数据库默认值，仅作为手工 SQL 插入时的兜底，正常代码不得依赖该默认值。
- 审计时间和业务时间统一保存为 `timestamp(0) without time zone`；PostgreSQL 连接信息必须指定 `TimeZone=Asia/Shanghai`，避免不同会话以不同时区解释无时区时间。
- 数据库禁止创建或依赖外键约束；迁移、GORM Entity、索引定义和手写 SQL 均不得包含 `FOREIGN KEY`、`REFERENCES` 或 GORM `constraint`/关联声明。
- 资源之间仍使用 UUID 字段保存逻辑关联，并为常用关联查询建立普通索引；数据库只负责字段类型、非空、唯一性等单表约束。
- 父资源存在性、租户归属、作用域匹配、引用状态和停用保护必须由 Application Service 在同一事务内显式读取并校验，不能依赖数据库级联或外键错误兜底。
- Repository 只做逐字段映射和显式查询，不使用 GORM Association、Preload、自动级联保存或级联删除。
- 新增持久化 Entity 或迁移时必须保留“无 GORM Relationship、无数据库外键”的自动化测试。

### 代理与流式响应

- 必须保留 SSE 流式传输能力。任何包装 `http.ResponseWriter` 的类型都要在底层支持时继续实现并转发 `http.Flusher`。
- 修改路径重写时同时处理 `URL.Path` 和非空的 `URL.RawPath`，并保持请求 Host 指向上游。
- 使用 `httptest.Server` 测试代理行为，不要让单元测试依赖真实 OpenAI 或 Anthropic 网络。

### 限流

- 优先级固定为：白名单绕过 → API Key override → default。
- OpenAI 从 `Authorization: Bearer` 提取 Key；Anthropic 优先使用 `x-api-key`，然后回退到 Bearer token。
- 每个 API Key 的 limiter 缓存在 `sync.Map` 中且当前不会淘汰。空 Key 也会作为同一个键参与限流；改变这一行为必须增加覆盖缺失 Key 的测试。
- 超限响应为 HTTP 429 和 JSON `{"error":"rate limit exceeded"}`。

### 日志与敏感信息

- 禁止记录完整 API Key；日志只保留末四位并加 `****` 前缀。
- 4xx/5xx 响应体最多捕获 4096 字节作为 `upstream_error`。修改捕获逻辑时不得破坏响应透传或流式刷新。
- 客户端 IP 提取优先级为 `X-Forwarded-For` 首项、`X-Real-IP`、`RemoteAddr`。
- 不要把真实密钥、私有上游地址或其他凭据写入 `config.yaml`、测试夹具、日志样例或文档。
- 有效 span context 的 Zap 请求日志必须带 `trace_id` 和 `span_id`；无效 context 不写空字段。

### OpenTelemetry

- `observability.enabled=false` 时不得创建 exporter、连接 Collector 或产生后台导出 goroutine。
- 只支持 OTLP/HTTP protobuf。标准 endpoint 优先级是 signal-specific > general `OTEL_EXPORTER_OTLP_ENDPOINT` > 项目配置；`OTEL_SERVICE_NAME` 覆盖项目 service name。
- Token 统计只进入 Dashboard，禁止加入 OTel metrics。自定义业务 metrics 的维度仅限规范化的 `provider`、`endpoint`、`outcome`。
- 禁止在 span/metrics attributes 中加入 API Key、Prompt、模型输出、正文、model、原始动态 path、资源 ID 或 query。
- `otelhttp` 会自动记录 URL。任何 server/client 插桩都必须先使用静态 `/{provider}/{endpoint}` URL，清空 query/fragment，再在业务 handler 或底层 RoundTripper 前恢复原始 URL/Host/RequestURI。
- 代理 transport 必须延续入站 context 并向上游注入 `traceparent`；client span 的 parent 应为对应 server span。
- 修改 OTel 配置、Runtime、插桩或 transport 后至少运行 `go test -race ./internal/infrastructure/observability ./internal/interfaces/http/middleware ./internal/infrastructure/proxy/... ./internal/interfaces/http/router ./internal/infrastructure/server/http` 和完整 build。

### Dashboard 与统计

- 所有统计字段使用 `sync/atomic`，进程重启后清零；不要为这些计数额外引入互斥锁。
- Stats 位于 RateLimiter 外层，因此 429 计入总请求数和 `RateLimited`，但不计入一般 `Errors`。
- Dashboard 在每次请求时将 JSON 注入 `</head>` 前。注入字段变化时同步更新 HTML 使用方和 handler 测试。
- 修改 `internal/interfaces/http/handler/dashboard/web/index.html` 后必须重新构建 Go 二进制才能生效；没有单独的前端打包命令。
- Dashboard 的用户界面文字保持中文；Go 标识符和注释遵循周边文件现有风格。
- Token 统计固定为 Total/OpenAI/Anthropic 三组原子累计值，进程重启清零；不要增加模型、Key、租户或时间序列维度。
- Token Observer 只处理五个精确 POST 端点和 2xx 响应。4xx/429/5xx、Responses retrieve 和未知路径不得增加 Token 或 missing usage。
- JSON 响应和单个 SSE line/event 的捕获上限为 2 MiB。超限、非法 JSON 或客户端写错误不得影响响应透传；成功的 eligible 请求应计入 missing usage。
- SSE parser 必须支持任意网络分块、CRLF、多 `data:` 行和无尾随空行，并把累计 usage 覆盖而非逐 delta 相加。任何改动都必须验证首事件可在上游结束前 Flush 给客户端。

### 配置与生命周期

- 配置优先级为进程环境变量 > `.env` > `config.yaml` > 默认值；环境变量使用 `LLM_PROXY_` 前缀。
- 列表和映射环境变量使用 JSON，非法的数字、布尔值或 JSON 必须导致启动失败。
- 新增配置字段时同步修改配置结构、Viper 默认值、`internal/infrastructure/config/env.go`、`config.yaml`、`.env.example` 和配置测试；若 Dashboard 展示该字段，也要更新其注入结构和页面。
- 配置不会热更新，修改后需要重启进程。
- OTel 项目配置包括 enabled、service name、OTLP endpoint、trace sample ratio 和 metrics interval；还要检查标准 `OTEL_*` 环境变量的覆盖与协议校验。
- `main` 提供 30 秒外层退出期限，`Server.Shutdown` 再限制为 10 秒。收到 SIGINT/SIGTERM 后必须先将 ready 置 false，再关闭 HTTP server，最后 ForceFlush/Shutdown telemetry；某一步失败不得跳过后续关闭。

## 常见改动落点

| 改动类型 | 通常需要检查的文件 |
| --- | --- |
| 新增或修改配置 | `internal/infrastructure/config/config.go`、`env.go`、`config_test.go`、`config.yaml`、`.env.example`，必要时 README |
| 修改代理路径或上游行为 | `internal/infrastructure/proxy/*.go`、`proxy_test.go`、`internal/di/provider/transparent.go` |
| 修改限流 | `internal/interfaces/http/middleware/ratelimit.go`、`ratelimit_test.go`、DI 配置映射、Dashboard 展示 |
| 修改请求日志 | `internal/interfaces/http/middleware/logging.go`、`logging_test.go`、`internal/infrastructure/logger` |
| 修改统计或 Dashboard | `internal/interfaces/http/middleware/stats.go`、`internal/interfaces/http/handler/dashboard`、对应测试和 HTML |
| 修改 Token usage 解析 | `internal/infrastructure/proxy/tokenusage/*.go`、Stats 中间件、Dashboard 统计与流式测试 |
| 修改 OTel 或 HTTP 插桩 | `internal/infrastructure/observability/*.go`、Logging、Proxy transport、Router 和 race 测试 |
| 修改健康检查或退出 | `internal/interfaces/http/handler/health`、`internal/infrastructure/server/http`、`cmd/proxy/main.go` |
| 修改依赖组装 | `internal/di/provider`、`modules`、`wire.go`，然后重新生成 `wire_gen.go` |
| 修改版本号 | `internal/application/runtime/version.go`，并检查 Router 与页面展示 |

## 测试与提交前检查

- 测试文件与实现放在同一包目录，沿用现有 `httptest` 和标准库断言风格。
- Bug 修复应先增加能复现问题的回归测试；涉及 ResponseWriter 时至少检查状态码、响应字节和 `http.Flusher`。
- 完成改动后先运行受影响包测试，再运行 `go test ./...`。
- 并发或统计逻辑运行 `go test -race ./...`。
- 构建或 Docker 相关改动至少运行 `go build -o /tmp/llm-proxy ./cmd/proxy`；Dockerfile 改动再运行镜像构建。
- 提交前运行 `git diff --check`，检查没有生成的二进制、日志、真实凭据或无关文件进入变更。
- 保持改动聚焦，不顺手修复与当前任务无关的问题；如果文档与源码冲突，优先核实源码并在任务范围内同步相关文档。
