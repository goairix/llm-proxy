# OpenTelemetry and Dashboard Token Observability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为代理增加 OTLP/HTTP traces 和 metrics、健康检查、日志 trace 关联，并在 Dashboard 中按总计/OpenAI/Anthropic 展示尽力解析的 Token 用量。

**Architecture:** `internal/observability` 管理 OTel SDK 生命周期、HTTP 插桩和低基数业务指标；`internal/tokenusage` 独立解析 JSON/SSE usage，服务器的 stats ResponseWriter 在保持透明写入与 Flush 的同时提交原子计数。OpenAI/Anthropic ReverseProxy 仅接受注入的 RoundTripper，不依赖遥测或协议 schema。

**Tech Stack:** Go 1.25、`net/http`、OpenTelemetry Go v1.45.0、`otelhttp` v0.70.0、Zap、标准库 `sync/atomic`、`httptest`

## Global Constraints

- 仅支持 OTLP/HTTP protobuf，不增加 gRPC、Prometheus、OTel Logs 或 Profiles。
- `observability.enabled` 默认 `false`；禁用时不得启动 exporter 或连接 Collector。
- Trace 默认使用 `ParentBased(TraceIDRatioBased(0.1))`；metrics 不采样。
- Token 只进入 Dashboard，不进入 OTel metrics；不按模型、API Key、租户或时间序列拆分。
- 不修改客户端请求，不强制 OpenAI 流返回 usage。
- 不记录 API Key、Prompt、模型输出、请求/响应正文或动态资源 ID。
- 交给 `otelhttp` 的 URL 必须先归一化且清空 query/fragment；真实 URL 只在进入业务 handler 或底层 transport 前恢复。
- 必须保留 SSE Flush、响应字节、状态码、限流和现有统计语义。
- 当前仓库直接在用户已授权的 `main` 上工作，不创建 worktree。

---

## File Map

- `internal/config/config.go`：项目级 Observability 配置结构和默认值。
- `internal/config/env.go`：`LLM_PROXY_OBSERVABILITY_*` 绑定与标量校验。
- `internal/tokenusage/observer.go`：端点识别、2 MiB 捕获边界、Observer 生命周期。
- `internal/tokenusage/json.go`：OpenAI/Anthropic 普通 JSON usage 映射。
- `internal/tokenusage/sse.go`：任意分块 SSE 事件解析与累计 usage 合并。
- `internal/dashboard/handler.go`：原子 Token 计数与注入 JSON。
- `internal/dashboard/web/index.html`：中文 Token 卡片与三行明细表。
- `internal/observability/config.go`：标准 OTel 环境变量优先级与校验。
- `internal/observability/runtime.go`：exporter、Resource、Provider、Propagator 和 Shutdown。
- `internal/observability/http.go`：入站 handler、业务 metrics、endpoint/outcome 归一化和上游 transport。
- `internal/middleware/logging.go`：Zap trace_id/span_id 关联。
- `internal/proxy/*.go`：注入可观测 RoundTripper。
- `internal/server/server.go`：请求链、Token Observer、健康状态和 Runtime 集成。
- `cmd/proxy/main.go`：OTel 初始化与退出 Flush。

---

### Task 1: Observability 项目配置

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/env.go`
- Modify: `internal/config/config_test.go`
- Modify: `config.yaml`
- Modify: `.env.example`

**Interfaces:**
- Produces: `config.ObservabilityConfig`
- Produces: `Config.Observability ObservabilityConfig`
- Produces env names `LLM_PROXY_OBSERVABILITY_ENABLED`, `SERVICE_NAME`, `OTLP_ENDPOINT`, `TRACE_SAMPLE_RATIO`, `METRICS_EXPORT_INTERVAL_SECONDS`

- [ ] **Step 1: 写配置默认值、YAML 和环境变量失败测试**

在 `configEnvNames` 加入五个新变量，并在 `TestLoad` 的默认分支断言：

```go
want := ObservabilityConfig{
    Enabled:                      false,
    ServiceName:                  "llm-proxy",
    OTLPEndpoint:                 "http://localhost:4318",
    TraceSampleRatio:             0.1,
    MetricsExportIntervalSeconds: 15,
}
if cfg.Observability != want {
    t.Fatalf("Observability = %+v, want %+v", cfg.Observability, want)
}
```

在有效 YAML case 加入：

```yaml
observability:
  enabled: true
  service_name: proxy-test
  otlp_endpoint: http://collector.example:4318
  trace_sample_ratio: 0.5
  metrics_export_interval_seconds: 30
```

在 `TestLoadScalarEnvironmentOverrides` 设置五个变量并断言环境变量覆盖 YAML。在 `TestLoadInvalidScalarEnvironment` 加入非法 bool、float、interval：

```go
{name: "LLM_PROXY_OBSERVABILITY_ENABLED", value: "invalid"},
{name: "LLM_PROXY_OBSERVABILITY_TRACE_SAMPLE_RATIO", value: "invalid"},
{name: "LLM_PROXY_OBSERVABILITY_METRICS_EXPORT_INTERVAL_SECONDS", value: "invalid"},
```

- [ ] **Step 2: 运行配置测试并确认 RED**

Run: `go test ./internal/config -run 'TestLoad|TestLoadScalarEnvironmentOverrides|TestLoadInvalidScalarEnvironment' -count=1`

Expected: 编译失败，提示 `undefined: ObservabilityConfig` 或 `Config` 没有 `Observability` 字段。

- [ ] **Step 3: 实现配置结构、默认值和 env binding**

在 `config.go` 增加：

```go
type ObservabilityConfig struct {
    Enabled                      bool    `mapstructure:"enabled"`
    ServiceName                  string  `mapstructure:"service_name"`
    OTLPEndpoint                 string  `mapstructure:"otlp_endpoint"`
    TraceSampleRatio             float64 `mapstructure:"trace_sample_ratio"`
    MetricsExportIntervalSeconds int     `mapstructure:"metrics_export_interval_seconds"`
}
```

把字段加入根 Config，并设置五个默认值。在 `env.go` 增加五个常量与 bindings，复用 `validateBool`、`validateFloat`、`validateInt`；范围校验留给 `internal/observability`，使配置加载与运行语义分层。

- [ ] **Step 4: 更新配置示例**

在 `config.yaml` 增加带中文注释的 `observability` 区块；在 `.env.example` 增加：

```dotenv
LLM_PROXY_OBSERVABILITY_ENABLED=false
LLM_PROXY_OBSERVABILITY_SERVICE_NAME=llm-proxy
LLM_PROXY_OBSERVABILITY_OTLP_ENDPOINT=http://localhost:4318
LLM_PROXY_OBSERVABILITY_TRACE_SAMPLE_RATIO=0.1
LLM_PROXY_OBSERVABILITY_METRICS_EXPORT_INTERVAL_SECONDS=15
```

标准 `OTEL_EXPORTER_OTLP_*` 变量不写入 `.env.example` 的值区，避免误导用户把认证 Header 提交到仓库；README 在 Task 9 说明它们。

- [ ] **Step 5: 验证并提交**

Run: `gofmt -w internal/config/config.go internal/config/env.go internal/config/config_test.go && go test ./internal/config -count=1`

Expected: `ok github.com/goairix/llm-proxy/internal/config`。

```bash
git add internal/config/config.go internal/config/env.go internal/config/config_test.go config.yaml .env.example
git commit -m "feat: add observability configuration"
```

### Task 2: Token 普通 JSON Observer

**Files:**
- Create: `internal/tokenusage/observer.go`
- Create: `internal/tokenusage/json.go`
- Create: `internal/tokenusage/observer_test.go`

**Interfaces:**
- Produces: `tokenusage.Usage`
- Produces: `tokenusage.Result`
- Produces: `tokenusage.Observer`
- Produces: `tokenusage.NewObserver(provider, method, path string) Observer`, ineligible request returns `nil`

- [ ] **Step 1: 写端点识别和 JSON 字段映射失败测试**

使用表驱动测试，至少包含以下 cases：

```go
tests := []struct {
    name, provider, path, body string
    want Usage
}{
    {
        name: "OpenAI Responses",
        provider: "openai",
        path: "/openai/v1/responses",
        body: `{"usage":{"input_tokens":120,"output_tokens":30,"input_tokens_details":{"cached_tokens":40,"cache_write_tokens":10},"output_tokens_details":{"reasoning_tokens":12}}}`,
        want: Usage{Input: 120, Output: 30, CacheRead: 40, CacheWrite: 10, Reasoning: 12},
    },
    {
        name: "OpenAI Chat Completions",
        provider: "openai",
        path: "/openai/v1/chat/completions",
        body: `{"usage":{"prompt_tokens":80,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":25,"cache_write_tokens":5},"completion_tokens_details":{"reasoning_tokens":7}}}`,
        want: Usage{Input: 80, Output: 20, CacheRead: 25, CacheWrite: 5, Reasoning: 7},
    },
    {
        name: "Anthropic Messages",
        provider: "anthropic",
        path: "/anthropic/v1/messages",
        body: `{"usage":{"input_tokens":50,"cache_creation_input_tokens":20,"cache_read_input_tokens":30,"output_tokens":15,"output_tokens_details":{"thinking_tokens":6}}}`,
        want: Usage{Input: 100, Output: 15, CacheRead: 30, CacheWrite: 20, Reasoning: 6},
    },
}
```

每个 case 创建 Observer、调用 `Observe("application/json", []byte(body))`、再调用 `Finish(200, nil)`，断言 `Present=true` 和 Usage。另写端点表断言只有规格中的五个 POST 端点返回非 nil；GET、Responses retrieve、input_tokens 和未知路径返回 nil。

- [ ] **Step 2: 运行测试并确认 RED**

Run: `go test ./internal/tokenusage -run 'TestObserverJSON|TestNewObserverEndpoints' -count=1`

Expected: 编译失败，因为 `Usage`、`Observer`、`NewObserver` 尚不存在。

- [ ] **Step 3: 实现 Observer 生命周期和 2 MiB 边界**

`observer.go` 定义：

```go
const maxCaptureBytes = 2 << 20

type Usage struct {
    Input, Output, CacheRead, CacheWrite, Reasoning int64
}

type Result struct {
    Usage   Usage
    Present bool
}

type Observer interface {
    Observe(contentType string, chunk []byte)
    Finish(status int, writeErr error) Result
}

type observer struct {
    provider, path string
    mode           responseMode
    jsonBody       []byte
    oversized      bool
    finished       bool
    result         Result
    sse            sseDecoder
}
```

`NewObserver` 使用 method + 精确 path 表，只接受五个生成端点。`Observe` 第一次根据 `strings.HasPrefix(strings.ToLower(contentType), "text/event-stream")` 确定模式；JSON 模式追加前先检查 2 MiB，超限后释放 `jsonBody` 并只设置 `oversized=true`。`Finish` 幂等返回同一 Result；非 2xx、writeErr 非 nil、oversized 时 Result 不存在。

- [ ] **Step 4: 实现 JSON schema 与校验**

在 `json.go` 使用指针区分主字段缺失与 0：

```go
type openAIResponsesUsage struct {
    InputTokens  *int64 `json:"input_tokens"`
    OutputTokens *int64 `json:"output_tokens"`
    InputDetails struct {
        Cached    int64 `json:"cached_tokens"`
        CacheWrite int64 `json:"cache_write_tokens"`
    } `json:"input_tokens_details"`
    OutputDetails struct {
        Reasoning int64 `json:"reasoning_tokens"`
    } `json:"output_tokens_details"`
}
```

为 Chat/Completions 和 Anthropic 定义对应结构。`parseJSON(provider, path, data)` 只读取 `usage`。Input/Output 指针必须非 nil；所有值必须非负。Anthropic Input 使用三项相加，并用溢出安全检查：任何加法导致结果小于加数时返回 invalid。

- [ ] **Step 5: 增加无效与边界测试**

增加 cases：主字段为 0、可选明细缺失、主字段缺失、负数、usage null、非法 JSON、恰好 2 MiB、超过 2 MiB、Finish 两次。断言无效数据 `Present=false`，第二次 Finish 与第一次完全相等。

- [ ] **Step 6: 验证并提交**

Run: `gofmt -w internal/tokenusage/*.go && go test ./internal/tokenusage -count=1`

Expected: tokenusage 包全部通过。

```bash
git add internal/tokenusage
git commit -m "feat: parse token usage from JSON responses"
```

### Task 3: Token SSE 增量解析

**Files:**
- Create: `internal/tokenusage/sse.go`
- Modify: `internal/tokenusage/observer.go`
- Modify: `internal/tokenusage/observer_test.go`

**Interfaces:**
- Consumes: Task 2 `Observer`, `Usage`, provider/path mapping
- Produces: internal `sseDecoder.Observe(chunk []byte)` and `sseDecoder.Finish() Result`

- [ ] **Step 1: 写 SSE 任意分块失败测试**

为三种协议构造完整流：

```go
responsesSSE := "event: response.completed\n" +
    "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":4,\"input_tokens_details\":{\"cached_tokens\":3},\"output_tokens_details\":{\"reasoning_tokens\":2}}}}\n\n"

chatSSE := "data: {\"object\":\"chat.completion.chunk\",\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":5}}\n\n" +
    "data: [DONE]\n\n"

anthropicSSE := "event: message_start\r\n" +
    "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"cache_creation_input_tokens\":2,\"cache_read_input_tokens\":3,\"output_tokens\":1}}}\r\n\r\n" +
    "event: message_delta\r\n" +
    "data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":8,\"output_tokens_details\":{\"thinking_tokens\":4}}}\r\n\r\n" +
    "event: message_stop\r\ndata: {\"type\":\"message_stop\"}\r\n\r\n"
```

对每个流分别一次写入和逐字节写入，断言 Result 一致。Anthropic want 为 `{Input:15, Output:8, CacheRead:3, CacheWrite:2, Reasoning:4}`。

- [ ] **Step 2: 运行测试并确认 RED**

Run: `go test ./internal/tokenusage -run TestObserverSSE -count=1`

Expected: 测试失败，SSE 当前被当作普通 JSON，`Present=false`。

- [ ] **Step 3: 实现无扫描器上限的 SSE 行状态机**

`sse.go` 不使用 `bufio.Scanner` 默认 64 KiB 限制，定义：

```go
type sseDecoder struct {
    provider, path string
    line           []byte
    data           []byte
    discardLine    bool
    discardEvent   bool
    latest         Result
    anthropic      anthropicPartialUsage
}
```

`Observe` 逐字节消费：遇到 `\n` 时移除行尾 `\r`；空行完成事件；`data:` 行移除一个可选空格后追加到 event data，多 data 行用 `\n` 连接。line 或 data 将超过 2 MiB 时立即释放 buffer，并丢弃到当前行/事件结束。comment、event、id、retry 行不进入 JSON。

- [ ] **Step 4: 实现三种 SSE usage 合并**

完成事件时：

- Responses 只接受 `type=response.completed`，解析 `response.usage`；
- Chat/Completions 接受顶层非空 `usage`，后出现值覆盖前值；
- Anthropic `message_start.message.usage` 初始化指针字段，`message_delta.usage` 只覆盖 JSON 中实际出现的字段；`message_stop` 不清空候选；
- `[DONE]`、ping、未知事件、无效 JSON 直接忽略。

`Finish` 要处理没有尾随空行的最后事件，然后返回 `latest`。不得把多个累计 delta 相加。

- [ ] **Step 5: 增加重复、超限和缺失测试**

覆盖：两个 Chat usage 取最后一个；Anthropic 多个累计 message_delta 取最后字段；超限事件后仍能解析下一个小事件；CRLF；无尾随空行；无 usage；负数；调用 Finish 两次。

- [ ] **Step 6: 验证并提交**

Run: `gofmt -w internal/tokenusage/*.go && go test ./internal/tokenusage -count=1`

```bash
git add internal/tokenusage
git commit -m "feat: parse token usage from SSE responses"
```

### Task 4: Dashboard Token 原子统计与页面

**Files:**
- Modify: `internal/dashboard/handler.go`
- Modify: `internal/dashboard/handler_test.go`
- Modify: `internal/dashboard/web/index.html`

**Interfaces:**
- Consumes: `tokenusage.Usage`
- Produces: `(*Stats).AddTokenUsage(provider string, usage tokenusage.Usage)`
- Produces: `(*Stats).AddMissingUsage(provider string)`
- Produces injected JSON `tokens.total`, `tokens.openai`, `tokens.anthropic`

- [ ] **Step 1: 写原子累计与注入 JSON 失败测试**

在 `handler_test.go`：

```go
stats.AddTokenUsage("openai", tokenusage.Usage{Input: 100, Output: 20, CacheRead: 30, CacheWrite: 5, Reasoning: 8})
stats.AddTokenUsage("anthropic", tokenusage.Usage{Input: 50, Output: 10, CacheRead: 15, CacheWrite: 4, Reasoning: 3})
stats.AddMissingUsage("openai")
```

解析注入 JSON，断言 total 为 Input 150、Output 30、CacheRead 45、CacheWrite 9、Reasoning 11、Missing 1、TotalTokens 180；同时断言 provider 拆分。断言 HTML 包含 `Token 用量`、`token-total`、`token-row-openai`、`token-row-anthropic`。

- [ ] **Step 2: 运行测试并确认 RED**

Run: `go test ./internal/dashboard -run 'TestHandler_TokenStatsInjected|TestHandler_TokenSection' -count=1`

Expected: 编译失败，因为 Stats 没有 Token 方法和 proxyData 没有 Tokens。

- [ ] **Step 3: 实现嵌套原子计数**

新增：

```go
type TokenStats struct {
    Input, Output, CacheRead, CacheWrite, Reasoning, Missing atomic.Int64
}

type TokenCounters struct {
    Total, OpenAI, Anthropic TokenStats
}
```

`Stats` 增加 `Tokens TokenCounters`。`AddTokenUsage` 先更新 Total，再按 provider 更新对应组；未知 provider 不更新任何计数。`AddMissingUsage` 同样更新 Total 和 provider。增加 snapshot helper，`TotalTokens = Input + Output`，只使用已 Load 的快照值计算。

- [ ] **Step 4: 扩展注入 JSON**

`proxyData` 增加：

```go
Tokens tokenData `json:"tokens"`
```

`tokenData` 固定包含 `Total`、`OpenAI`、`Anthropic`，每项字段为 `total_tokens`、`input_tokens`、`output_tokens`、`cache_read_tokens`、`cache_write_tokens`、`reasoning_tokens`、`missing_usage_requests`。

- [ ] **Step 5: 增加中文 Dashboard Token 区域**

在请求统计卡片后插入独立 card。顶部四个卡片 ID：`token-total`、`token-input`、`token-output`、`token-missing`。复用 `.rl-table` 样式，三行 ID：`token-row-total`、`token-row-openai`、`token-row-anthropic`。增加：

```javascript
function formatCount(value) {
  return Number(value || 0).toLocaleString('zh-CN');
}
```

初始化时填充四个卡片和三行七个数值列。不得新增 fetch、timer 或后台请求。

- [ ] **Step 6: 验证并提交**

Run: `gofmt -w internal/dashboard/handler.go internal/dashboard/handler_test.go && go test ./internal/dashboard -count=1`

```bash
git add internal/dashboard/handler.go internal/dashboard/handler_test.go internal/dashboard/web/index.html
git commit -m "feat: show token usage on dashboard"
```

### Task 5: Token Observer 接入请求链

**Files:**
- Modify: `internal/server/server.go`
- Create: `internal/server/server_test.go`

**Interfaces:**
- Consumes: `tokenusage.NewObserver`, `dashboard.Stats.AddTokenUsage`, `AddMissingUsage`
- Preserves: `statsMiddleware(provider string, stats *dashboard.Stats, next http.Handler) http.Handler`

- [ ] **Step 1: 写 JSON、missing 和 SSE 透明集成失败测试**

直接测试 `statsMiddleware`：

1. OpenAI Responses handler 返回带 usage 的 JSON，断言响应字节不变且 OpenAI/Total Token 更新；
2. 成功 JSON 无 usage，断言 OpenAI Missing=1；
3. 429 与 500 无 usage，断言 Missing=0；
4. Anthropic SSE handler Flush message_start、message_delta、message_stop，客户端必须在 handler 结束前读到首事件，最终断言累计 Usage；
5. 非生成 GET `/openai/v1/responses/resp_1` 不统计也不计 missing。

- [ ] **Step 2: 运行测试并确认 RED**

Run: `go test ./internal/server -run 'TestStatsMiddleware_Token|TestStatsMiddleware_TokenStreaming' -count=1`

Expected: Token 断言失败，因为 statsMiddleware 尚未创建 Observer。

- [ ] **Step 3: 扩展 statsResponseWriter**

增加：

```go
type statsResponseWriter struct {
    http.ResponseWriter
    status   int
    bytes    int
    observer tokenusage.Observer
    writeErr error
}
```

`Write` 先调用底层；如果 `n>0 && observer!=nil`，调用 `Observe(w.Header().Get("Content-Type"), b[:n])`；只保存第一个 write error。`Flush` 保持现有转发。不得把 Observer 错误返回给客户端。

- [ ] **Step 4: 在请求完成后提交一次 Result**

`statsMiddleware` 在调用 next 前创建：

```go
observer := tokenusage.NewObserver(provider, r.Method, r.URL.Path)
```

next 返回后调用 `result := observer.Finish(srw.status, srw.writeErr)`。只有 observer 非 nil 且 status 为 2xx：Present 时 `AddTokenUsage`，否则 `AddMissingUsage`。之后执行现有 latency、bytes、errors/rate-limited 更新，保持 429 语义。

- [ ] **Step 5: 验证响应接口和竞态**

Run: `gofmt -w internal/server/*.go && go test ./internal/server -count=1 && go test -race ./internal/server ./internal/dashboard ./internal/tokenusage -count=1`

Expected: 全部通过，无 race；SSE 测试确认首事件在上游结束前可读。

- [ ] **Step 6: 提交**

```bash
git add internal/server/server.go internal/server/server_test.go
git commit -m "feat: collect token usage from proxy responses"
```

### Task 6: OpenTelemetry Runtime 与 OTLP/HTTP Exporter

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/observability/config.go`
- Create: `internal/observability/config_test.go`
- Create: `internal/observability/runtime.go`
- Create: `internal/observability/runtime_test.go`

**Interfaces:**
- Consumes: `config.ObservabilityConfig`, `server.Version` must not be imported to avoid cycle; caller passes version to New
- Produces: `observability.New(ctx, cfg, serviceVersion, logger) (*Runtime, error)`
- Produces: `(*Runtime).Shutdown(ctx context.Context) error`

- [ ] **Step 1: 添加固定版本依赖**

Run:

```bash
go get go.opentelemetry.io/otel@v1.45.0 \
  go.opentelemetry.io/otel/sdk@v1.45.0 \
  go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp@v1.45.0 \
  go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp@v1.45.0 \
  go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp@v0.70.0
go mod tidy
```

Expected: OTel API/SDK/exporter/otelhttp 加入 `go.mod`，间接 protobuf 依赖写入 `go.sum`。

- [ ] **Step 2: 写配置解析失败测试**

`config_test.go` 表驱动覆盖：disabled 无需 endpoint；空 service name；ratio -0.1/1.1；interval 0；相对/ftp/缺 hostname/带 query/fragment endpoint；protocol env 为 grpc；标准 trace/metric endpoint 覆盖通用 endpoint；`OTEL_SERVICE_NAME` 覆盖项目值。

测试期望内部解析结果：

```go
type effectiveConfig struct {
    enabled        bool
    serviceName    string
    traceEndpoint  string
    metricEndpoint string
    sampleRatio    float64
    metricInterval time.Duration
}
```

- [ ] **Step 3: 运行配置测试并确认 RED**

Run: `go test ./internal/observability -run TestResolveConfig -count=1`

Expected: 编译失败，`resolveConfig` 尚不存在。

- [ ] **Step 4: 实现标准环境变量优先级和校验**

`resolveConfig`：disabled 直接返回 no-op 有效配置；enabled 时 trim service name；检查三个 protocol env 只能为空或 `http/protobuf`；分别按 signal endpoint > general endpoint > project endpoint 选 trace/metric endpoint。所有 endpoint 使用 `url.ParseRequestURI` 后再检查 scheme、Hostname、RawQuery、Fragment。interval 转为 `time.Duration(seconds)*time.Second` 前检查正数和溢出。

保留布尔 `traceFromEnv`、`metricFromEnv`，Runtime 创建 exporter 时：标准 env 生效则不传 `WithEndpointURL`；否则传 effective endpoint，让 exporter 继续原生读取 Header、证书、压缩、timeout 等标准变量。

- [ ] **Step 5: 写 disabled、OTLP path、resource 和 Shutdown 失败测试**

使用 `httptest.Server` 收集请求路径。Enabled Runtime 创建后通过内部 tracer 结束一个 span，通过 meter counter 写一个点，再调用 Shutdown。断言收到 `/v1/traces` 和 `/v1/metrics`，Content-Type 含 protobuf，Resource 有 `service.name`/`service.version`。Disabled Runtime 不向 server 发请求。调用 Shutdown 两次返回相同结果且不 panic。

- [ ] **Step 6: 实现 Runtime**

`runtime.go` 定义：

```go
type Runtime struct {
    enabled        bool
    tracerProvider *sdktrace.TracerProvider
    meterProvider  *sdkmetric.MeterProvider
    propagator     propagation.TextMapPropagator
    shutdownOnce   sync.Once
    shutdownErr    error
}
```

Enabled New 创建：

- `otlptracehttp.New` + `sdktrace.WithBatcher`；
- `otlpmetrichttp.New` + `sdkmetric.NewPeriodicReader(WithInterval)`；
- `resource.Default()` 与 `service.name`、`service.version` merge；
- ParentBased TraceIDRatioBased sampler；
- composite TraceContext + Baggage propagator；
- OTel ErrorHandler 写 `logger.Error("opentelemetry error", zap.Error(err))`；
- 设置全局 TracerProvider、MeterProvider、TextMapPropagator。

Shutdown 在 `sync.Once` 内依次调用 `ForceFlush` 和 `Shutdown`，用 `errors.Join` 汇总 trace/metric 错误。Disabled Shutdown 返回 nil。

- [ ] **Step 7: 验证并提交**

Run: `gofmt -w internal/observability/*.go && go test ./internal/observability -count=1`

```bash
git add go.mod go.sum internal/observability
git commit -m "feat: add OTLP HTTP observability runtime"
```

### Task 7: HTTP Spans、业务 Metrics、上游 Transport 与日志关联

**Files:**
- Create: `internal/observability/http.go`
- Create: `internal/observability/http_test.go`
- Modify: `internal/observability/runtime.go`
- Modify: `internal/middleware/logging.go`
- Modify: `internal/middleware/logging_test.go`
- Modify: `internal/proxy/proxy.go`
- Modify: `internal/proxy/openai.go`
- Modify: `internal/proxy/anthropic.go`
- Modify: `internal/proxy/proxy_test.go`
- Modify: `internal/server/server.go`

**Interfaces:**
- Produces: `(*Runtime).WrapHandler(provider string, next http.Handler) http.Handler`
- Produces: `(*Runtime).Transport(base http.RoundTripper) http.RoundTripper`
- Changes: `proxy.NewOpenAIProxy(baseURL string, transport http.RoundTripper)` and Anthropic equivalent
- Changes: `server.New(cfg, logger, telemetry *observability.Runtime)`

- [ ] **Step 1: 写 endpoint/outcome 与业务 metrics 失败测试**

表驱动断言 endpoint：exact generation paths 分别映射为 `responses`、`responses.compact`、`chat.completions`、`completions`、`messages`，其余为 `other`。用 SpanRecorder + ManualReader 构造测试 Runtime，WrapHandler 一个返回 201、429、500、context canceled 的 handler，断言：

- server span 名不含动态 ID；
- `llm_proxy.requests` 带 provider/endpoint/outcome；
- in-flight 请求期间值为 1，完成后为 0；
- 429 增加 `llm_proxy.rate_limit.rejections`；
- attribute 集合不含 path、API key、model。

- [ ] **Step 2: 运行 HTTP 测试并确认 RED**

Run: `go test ./internal/observability -run 'TestEndpoint|TestWrapHandler' -count=1`

Expected: `Runtime` 没有 `WrapHandler`，测试编译失败。

- [ ] **Step 3: 实现业务 instruments 和 URL 脱敏 otelhttp handler**

Runtime New 时从 Meter 创建三个 int64 instruments，unit 为 `{request}`。`WrapHandler` 在 disabled 时原样返回 next；enabled 时：

1. 计算 provider/endpoint attributes；
2. in-flight +1，defer -1；
3. 用保留 `http.Flusher` 的 status writer 捕获状态；
4. 根据 context error、429、4xx、5xx 生成 outcome；
5. 记录 request counter 和 rate-limit counter；
6. 最外层使用 `otelhttp.NewHandler`，显式传 Runtime 的 TracerProvider、MeterProvider、Propagator 和规范 span name formatter。

`otelhttp` 会自动记录 `url.path`，所以不得直接把真实 request 交给它。`WrapHandler` 使用浅克隆 request 和深克隆 URL，把 instrumentation 视图设为静态路径 `/{provider}/{endpoint}`（例如 `/openai/responses`、`/anthropic/other`），并清空 `RawPath`、`RawQuery`、`ForceQuery` 和 `Fragment`，同时把 `RequestURI` 设为归一化路径。内层 handler 再用 traced request 的 Context 与原始 URL/RequestURI 构造下游 request。这样 trace propagation 和 status/metrics 保留，而动态 ID 和 query 不进入 span 或标准 HTTP metrics attributes。

在 request Context 中写入私有 `routeInfo{provider, endpoint}`，仅供同一请求的上游 transport 生成同样的脱敏 instrumentation URL；不对业务层暴露 context key。

- [ ] **Step 4: 写并实现上游 client span 与 transport 注入**

测试创建 upstream `httptest.Server`，server handler 用 `Runtime.Transport(http.DefaultTransport)` 发请求。断言 SpanRecorder 有一个 server span、一个 client span，client Parent SpanID 等于 server SpanID，upstream 收到 `traceparent`。

`Runtime.Transport` disabled 时返回传入 base；base nil 时使用 `http.DefaultTransport`。Enabled transport 由三层组成：

1. 外层 `sanitizingTransport` 从 Context 读取 `routeInfo`，克隆 request/URL，保存原始 URL、Host 和 RequestURI 到私有 context value，将 instrumentation URL 路径改为 `/{provider}/{endpoint}`，清空 query/fragment 并保持 client `RequestURI` 为空；
2. 中层为单个复用的 `otelhttp.NewTransport`，显式设置 Runtime TracerProvider、MeterProvider 和 Propagator；
3. 内层 `restoringTransport` 在调用真实 base 前克隆 request，恢复原始 URL 和 Host。

这保证 `otelhttp` 只看到脱敏 URL，但底层 transport 仍向真实 upstream 发送请求。增加带 `/responses/resp_secret?include=usage` 的测试，断言 upstream 收到完整原始 URL，而 server/client span attributes 不含 `resp_secret`、`include=usage` 或 Authorization 值。

`proxy.options` 增加 `Transport http.RoundTripper`；非 nil 时赋给 ReverseProxy。两个 provider constructor 增加 transport 参数，所有 proxy tests 显式传 nil，并增加自定义 RoundTripper 被调用的测试。

- [ ] **Step 5: 写并实现 Zap trace 关联**

在 Logging test 构造固定 SpanContext：

```go
sc := trace.NewSpanContext(trace.SpanContextConfig{
    TraceID: trace.TraceID{1, 2, 3},
    SpanID:  trace.SpanID{4, 5, 6},
    TraceFlags: trace.FlagsSampled,
})
req = req.WithContext(trace.ContextWithSpanContext(req.Context(), sc))
```

断言 observer log fields 包含 sc 的完整十六进制 `trace_id`、`span_id`。无效 Context 的现有日志不新增空字段。实现时只在 `sc.IsValid()` 时 append 两个 zap.String。

- [ ] **Step 6: 接入 server 请求链**

`server.New` 接收 Runtime；创建 `transport := telemetry.Transport(http.DefaultTransport)` 并传给两个代理。每个 mux provider handler 最外层使用：

```go
telemetry.WrapHandler("openai", loggingMiddleware(
    statsMiddleware("openai", stats,
        rateLimiter.Handler("openai", openaiProxy),
    ),
))
```

Anthropic 同样处理。Dashboard 暂不包装。更新所有 server tests 传入 disabled Runtime test helper。

- [ ] **Step 7: 验证并提交**

Run: `gofmt -w internal/observability/*.go internal/middleware/*.go internal/proxy/*.go internal/server/*.go && go test ./internal/observability ./internal/middleware ./internal/proxy ./internal/server -count=1`

Run: `go test -race ./internal/observability ./internal/middleware ./internal/proxy ./internal/server -count=1`

```bash
git add internal/observability internal/middleware/logging.go internal/middleware/logging_test.go internal/proxy internal/server
git commit -m "feat: instrument proxy HTTP traffic"
```

### Task 8: 健康端点与进程生命周期

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/server_test.go`
- Modify: `cmd/proxy/main.go`

**Interfaces:**
- Produces: `GET /healthz`
- Produces: `GET /readyz`
- Preserves: `Server.Start`, `Server.Shutdown`

- [ ] **Step 1: 写健康状态失败测试**

创建 disabled Runtime 与 Server，从 `srv.httpServer.Handler` 发请求：

- GET `/healthz` 始终 200 JSON `{"status":"ok"}`；
- ready=false 时 GET `/readyz` 为 503 `{"status":"not_ready"}`；
- test 内 `srv.ready.Store(true)` 后为 200 `{"status":"ready"}`；
- POST 两个路径为 405；
- Dashboard `/` 仍返回 HTML；
- health/ready 不增加 Dashboard Total，也不创建 proxy spans。

- [ ] **Step 2: 运行测试并确认 RED**

Run: `go test ./internal/server -run TestServerHealth -count=1`

Expected: 当前根 Dashboard 接住 health path 并返回 HTML，状态/正文断言失败。

- [ ] **Step 3: 实现 method-specific health handlers 和 ready 原子状态**

Server 增加 `ready atomic.Bool`。在根 Dashboard 前注册：

```go
mux.HandleFunc("GET /healthz", healthHandler)
mux.HandleFunc("GET /readyz", s.readyHandler)
```

由于 readyHandler 需要 Server，在 New 中先构造 `Server` 再绑定 handler，最后设置 `httpServer.Handler=mux`。JSON helper 固定 Content-Type `application/json`，使用 `json.NewEncoder` 并处理 Encode error：响应已开始后仅写 logger error。

`Start` 在 ListenAndServe 前 ready=true，defer ready=false；`Shutdown` 首行 ready=false，再沿用 10 秒内部 timeout。

- [ ] **Step 4: 接入 main 初始化与关闭**

Logger 后执行：

```go
telemetry, err := observability.New(context.Background(), cfg.Observability, server.Version, log_)
if err != nil {
    log.Fatalf("failed to init observability: %v", err)
}
```

传给 `server.New`。Start goroutine 只在错误不是 `http.ErrServerClosed` 时记录 error。收到信号后使用现有 30 秒 Context：先 `srv.Shutdown(ctx)`，再 `telemetry.Shutdown(ctx)`；两个错误分别记录，不能因前者失败跳过后者。Zap Sync 保持最后执行。

- [ ] **Step 5: 验证并提交**

Run: `gofmt -w cmd/proxy/main.go internal/server/*.go && go test ./internal/server ./cmd/proxy -count=1 && go build -o /tmp/llm-proxy ./cmd/proxy`

```bash
git add cmd/proxy/main.go internal/server/server.go internal/server/server_test.go
git commit -m "feat: add health checks and telemetry lifecycle"
```

### Task 9: 文档、全量审查与完成门禁

**Files:**
- Modify: `README.md`
- Modify: `AGENTS.md`

**Interfaces:**
- Documents: OTel enablement, OTLP/HTTP env vars, health endpoints, Token semantics and missing usage limitation

- [ ] **Step 1: 更新 README**

增加：

- 功能列表中的 OpenTelemetry traces/metrics、健康检查、Token Dashboard；
- 路由表 `/healthz`、`/readyz`；
- 完整 observability YAML；
- `OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4318`、Headers、证书、timeout 示例；
- 标准 OTel env > `LLM_PROXY_*` > YAML 的优先级；
- Token 五个端点、Input/Output/Cache/Reasoning 映射；
- 流式 usage 缺失和后台 Responses 不补记的限制；
- Collector 不参与 readiness。

- [ ] **Step 2: 更新 AGENTS.md**

补充：

- `internal/observability`、`internal/tokenusage` 目录职责；
- 新请求链 `OTel → Logging → Stats/Token → RateLimiter → ReverseProxy → OTel Transport`；
- 禁止高基数和敏感 OTel attributes；
- Token Observer 的 2 MiB/SSE/每请求一次约束；
- OTel 变更运行 `go test -race` 和 build；
- 新配置字段同步 YAML、env example、config tests。

- [ ] **Step 3: 运行格式化和包级测试**

Run: `gofmt -w cmd/proxy/*.go internal/config/*.go internal/dashboard/*.go internal/middleware/*.go internal/observability/*.go internal/proxy/*.go internal/ratelimit/*.go internal/server/*.go internal/tokenusage/*.go`

Run: `go test ./internal/tokenusage ./internal/dashboard ./internal/observability ./internal/middleware ./internal/proxy ./internal/server -count=1`

Expected: 所有受影响包通过。

- [ ] **Step 4: 运行完整门禁**

Run: `go test ./...`

Run: `go test -race ./...`

Run: `go vet ./...`

Run: `go build -o /tmp/llm-proxy ./cmd/proxy`

Run: `git diff --check`

Expected: 全部退出码 0；无 race、vet warning、构建错误或 whitespace error。

- [ ] **Step 5: 审查变更范围和安全性**

Run: `git status --short && git diff --stat && git diff --check`

检查：

- 无真实 API Key、OTLP Header、证书或私有 endpoint；
- 自定义业务 metrics attributes 只有 provider、endpoint、outcome，标准 HTTP attributes 经过 URL 脱敏；
- OTel server/client span 不含动态 ID、query 或 Authorization，但 upstream 收到的 URL/Host 与客户端请求一致；
- Token 不进入 OTel；
- health/Dashboard 不进入 proxy telemetry；
- 未新增生成二进制、日志或临时文件；
- `observability.enabled=false` 的测试证明无 exporter 网络请求。

- [ ] **Step 6: 提交文档与最终调整**

```bash
git add README.md AGENTS.md
git commit -m "docs: document proxy observability"
```
