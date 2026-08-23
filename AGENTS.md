# AGENTS.md

本文件适用于整个仓库，供在本项目中工作的编码代理使用。以当前源码、`go.mod` 和 `Dockerfile` 为事实来源；README 和历史设计文档可能滞后。

## 项目概览

`llm-proxy` 是一个 Go HTTP 反向代理，为 OpenAI 和 Anthropic API 提供路径转发、按 API Key 限流、结构化日志和内嵌 Dashboard。项目不做两种协议之间的格式转换。

- Module：`github.com/goairix/llm-proxy`
- Go：1.25（Docker 构建镜像当前为 Go 1.25.4）
- HTTP：标准库 `net/http`、`httputil.ReverseProxy`
- 配置：Viper + `config.yaml` + godotenv
- 日志：Zap + `file-rotatelogs`
- 限流：`golang.org/x/time/rate`
- 前端：单个内嵌 HTML 文件，无独立构建步骤

## 常用命令

在仓库根目录执行：

```bash
# 全量测试
go test ./...

# 指定包或用例
go test ./internal/middleware/
go test ./internal/middleware/ -run TestLoggingMiddlewareErrorLevels

# 涉及 sync.Map、atomic 或并发逻辑时
go test -race ./...

# 构建与运行；运行时从当前目录读取 config.yaml 和可选的 .env
go build -o /tmp/llm-proxy ./cmd/proxy
go run ./cmd/proxy

# 容器构建
docker build -t llm-proxy .
```

修改 Go 文件后运行 `gofmt`。只有依赖确实发生变化时才运行 `go mod tidy`，并检查 `go.mod`、`go.sum` 的差异。

## 请求链路与路由

代理请求的实际处理顺序是：

```text
客户端 → Logging → Stats → RateLimiter → ReverseProxy → 上游 API
```

- `/openai/*`：去掉 `/openai` 前缀后转发到 OpenAI `base_url`。
- `/anthropic/*`：去掉 `/anthropic` 前缀后转发到 Anthropic `base_url`。
- `/`：Dashboard handler。由于它注册为 ServeMux 的根模式，当前还会接住所有未匹配路径；不要默认未知路径一定返回 404。
- Dashboard 不经过代理请求的日志、统计和限流中间件。

中间件的包裹顺序有意让 Logging 记录完整请求，让 Stats 统计被限流的请求，并让 RateLimiter 在访问上游前拒绝请求。调整顺序前应更新相应测试并检查统计语义。

## 目录职责

- `cmd/proxy/main.go`：加载 `config.yaml`、初始化日志与服务器、监听信号并优雅退出。
- `internal/config`：配置结构、默认值、YAML、`.env` 和进程环境变量加载；配置文件缺失时使用默认值，文件存在但无效时启动失败。
- `internal/server`：路由和中间件组装、Dashboard 统计、HTTP 服务器生命周期；版本号定义在这里。
- `internal/proxy`：创建两个反向代理，修改目标地址、Host、Path 和 RawPath。
- `internal/middleware`：访问日志、API Key 提取、令牌桶限流。
- `internal/dashboard`：原子统计、Dashboard 数据注入和页面响应。
- `internal/dashboard/web/index.html`：Dashboard 源文件，通过 `go:embed` 编入二进制。
- `internal/logger`：控制台日志和按天轮转的 JSON 文件日志。
- `config.yaml`：带注释的 YAML 运行配置示例，也是本地直接运行时读取的默认路径。
- `.env.example`：全部 `LLM_PROXY_` 环境变量的非敏感示例；实际 `.env` 不入库。

## 关键实现约束

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

### Dashboard 与统计

- 所有统计字段使用 `sync/atomic`，进程重启后清零；不要为这些计数额外引入互斥锁。
- Stats 位于 RateLimiter 外层，因此 429 计入总请求数和 `RateLimited`，但不计入一般 `Errors`。
- Dashboard 在每次请求时将 JSON 注入 `</head>` 前。注入字段变化时同步更新 HTML 使用方和 handler 测试。
- 修改 `internal/dashboard/web/index.html` 后必须重新构建 Go 二进制才能生效；没有单独的前端打包命令。
- Dashboard 的用户界面文字保持中文；Go 标识符和注释遵循周边文件现有风格。

### 配置与生命周期

- 配置优先级为进程环境变量 > `.env` > `config.yaml` > 默认值；环境变量使用 `LLM_PROXY_` 前缀。
- 列表和映射环境变量使用 JSON，非法的数字、布尔值或 JSON 必须导致启动失败。
- 新增配置字段时同步修改配置结构、Viper 默认值、`internal/config/env.go`、`config.yaml`、`.env.example` 和配置测试；若 Dashboard 展示该字段，也要更新其注入结构和页面。
- 配置不会热更新，修改后需要重启进程。
- `main` 提供 30 秒外层退出期限，`Server.Shutdown` 再限制为 10 秒。更改退出流程时要保留 SIGINT/SIGTERM 的优雅关闭。

## 常见改动落点

| 改动类型 | 通常需要检查的文件 |
| --- | --- |
| 新增或修改配置 | `internal/config/config.go`、`env.go`、`config_test.go`、`config.yaml`、`.env.example`，必要时 README |
| 修改代理路径或上游行为 | `internal/proxy/*.go`、`proxy_test.go`、`internal/server/server.go` |
| 修改限流 | `internal/middleware/ratelimit.go`、`ratelimit_test.go`、Dashboard 展示 |
| 修改请求日志 | `internal/middleware/logging.go`、`logging_test.go`、`internal/logger` |
| 修改统计或 Dashboard | `internal/server/server.go`、`internal/dashboard/handler.go`、对应测试和 HTML |
| 修改版本号 | `internal/server/server.go` 中的 `Version`，并检查页面展示 |

## 测试与提交前检查

- 测试文件与实现放在同一包目录，沿用现有 `httptest` 和标准库断言风格。
- Bug 修复应先增加能复现问题的回归测试；涉及 ResponseWriter 时至少检查状态码、响应字节和 `http.Flusher`。
- 完成改动后先运行受影响包测试，再运行 `go test ./...`。
- 并发或统计逻辑运行 `go test -race ./...`。
- 构建或 Docker 相关改动至少运行 `go build -o /tmp/llm-proxy ./cmd/proxy`；Dockerfile 改动再运行镜像构建。
- 提交前运行 `git diff --check`，检查没有生成的二进制、日志、真实凭据或无关文件进入变更。
- 保持改动聚焦，不顺手修复与当前任务无关的问题；如果文档与源码冲突，优先核实源码并在任务范围内同步相关文档。
