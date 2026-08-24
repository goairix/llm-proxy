# OpenTelemetry 与 Dashboard Token 可观测性设计

## 背景与目标

项目当前具备 Zap 结构化访问日志、Dashboard 进程内统计和按 API Key 限流，但缺少标准化的调用链、指标导出、健康检查以及 Token 用量统计。

本次增加两条相互独立的数据链路：

1. 使用 OpenTelemetry Go SDK，通过 OTLP/HTTP protobuf 导出 traces 和 metrics。
2. 从上游响应的 `usage` 中尽力提取 Token 用量，仅写入本地原子计数并展示在 Dashboard。

代理继续保持协议透明：不修改请求，不转换响应，不把 API Key、Prompt、模型输出或响应正文写入遥测系统。

## 范围

### 包含

- OTLP/HTTP traces 和 metrics；
- 入站 HTTP server span 与上游 HTTP client span；
- 标准 HTTP 指标与低基数代理业务指标；
- Zap 日志中的 `trace_id`、`span_id`；
- `/healthz` 和 `/readyz`；
- OpenAI Responses、Chat Completions、Completions 和 Anthropic Messages 的 Token 统计；
- 普通 JSON 与 SSE 响应；
- Dashboard Token 总量和提供商拆分。

### 不包含

- OpenTelemetry Logs、Profiles 或 Prometheus scrape endpoint；
- Token 按模型、API Key、租户或时间序列拆分；
- Token 费用估算、持久化或跨实例聚合；
- 修改流式请求以强制上游返回 usage；
- 解析或记录 Prompt、模型输出、工具参数；
- 探测 OpenAI、Anthropic 或 Collector 作为 readiness 条件。

OpenTelemetry Go 的 traces 和 metrics 当前为 Stable，logs 仍为 Beta，因此日志继续使用 Zap，只增加 trace 关联字段。参考：[OpenTelemetry Go 状态](https://opentelemetry.io/docs/languages/go/)。

## 方案选择

### 采用：标准 OTel HTTP 插桩 + 独立 Token 观察中间件

- `otelhttp` 负责入站和上游 HTTP 插桩；
- Token Observer 旁路读取已写给客户端的响应字节；
- JSON 在响应完成后解析，SSE 按事件增量解析；
- Token 解析器输出统一结构，Dashboard 只消费原子快照；
- 代理核心不依赖任何 OpenAI/Anthropic JSON schema。

### 未采用：在 ReverseProxy.ModifyResponse 中解析

该方案会把提供商协议耦合进通用代理。普通 JSON 往往需要提前读取并恢复 Body，会增加首字节延迟和内存风险。

### 未采用：仅在 Collector 侧统计 Token

该方案需要把 usage 放入 OTel attributes/events，既不适合高频累计值，也无法直接为本地 Dashboard 提供可靠统计。

## 包与接口边界

### `internal/observability`

职责：

- 校验可观测配置；
- 创建 OTLP/HTTP trace exporter、metric exporter；
- 创建 Resource、TracerProvider、MeterProvider 和全局 Propagator；
- 提供 HTTP server handler 包装器与上游 RoundTripper；
- 创建代理业务 instruments；
- 安装 OTel 错误处理器，将 exporter 运行期错误写入 Zap；
- 在 Shutdown 时 Flush 并关闭两个 Provider。

建议接口：

```go
type Runtime struct {
    // private SDK providers and instruments
}

func New(ctx context.Context, cfg config.ObservabilityConfig, logger *zap.Logger) (*Runtime, error)
func (r *Runtime) WrapHandler(provider string, next http.Handler) http.Handler
func (r *Runtime) Transport(base http.RoundTripper) http.RoundTripper
func (r *Runtime) Shutdown(ctx context.Context) error
```

当 `enabled=false` 时返回 no-op Runtime。调用方不需要在每条请求链上增加条件分支。

### `internal/tokenusage`

职责：

- 判断请求是否属于可统计的生成端点；
- 观察响应状态、Content-Type 和响应字节；
- 解析普通 JSON 或 SSE usage；
- 将不同提供商字段映射为统一 `Usage`；
- 每个请求至多提交一次解析结果。

建议接口：

```go
type Usage struct {
    Input      int64
    Output     int64
    CacheRead  int64
    CacheWrite int64
    Reasoning  int64
}

type Result struct {
    Usage   Usage
    Present bool
}

type Observer interface {
    Observe(contentType string, chunk []byte)
    Finish(status int, writeErr error) Result
}

func NewObserver(provider, method, path string) Observer
```

Observer 在请求开始时创建；响应 Content-Type 尚未确定，因此由 ResponseWriter 在每次成功写入后连同字节块传给 `Observe`。解析器不依赖 Dashboard。服务器层负责把 Result 写入统计结构。

### Dashboard 统计

继续沿用 `sync/atomic`。为总计、OpenAI、Anthropic 各维护：

```text
input_tokens
output_tokens
cache_read_tokens
cache_write_tokens
reasoning_tokens
missing_usage_requests
```

不引入互斥锁、数据库或后台聚合 goroutine。进程重启后清零。

## 请求链与数据流

代理请求链调整为：

```text
OTel HTTP Server
  → Logging
  → Stats + Token Observer
  → RateLimiter
  → ReverseProxy
  → OTel HTTP Transport
  → 上游 API
```

OTel handler 必须位于 Logging 外层，确保 Logging 能从请求 Context 读取 server span 的 `trace_id` 和 `span_id`。Stats 继续位于 RateLimiter 外层，因此 429 的现有统计语义不变。

Token Observer 包装现有 stats ResponseWriter，必须继续实现并转发 `http.Flusher`。写入顺序固定为：先写底层客户端 ResponseWriter，再把实际成功写入的 `p[:n]` 交给 Observer。客户端写入失败时不阻塞或重试。

通用 ReverseProxy 接受注入的 `http.RoundTripper`。启用时使用 `otelhttp.NewTransport`，禁用时使用 `http.DefaultTransport`。请求 Context 自然传播到上游 client span，并通过 W3C `traceparent` 和 `baggage` 传播。

Dashboard、`/healthz` 和 `/readyz` 不经过 LLM 业务遥测链，避免轮询噪声。

## OpenTelemetry 配置

新增：

```yaml
observability:
  enabled: false
  service_name: "llm-proxy"
  otlp_endpoint: "http://localhost:4318"
  trace_sample_ratio: 0.1
  metrics_export_interval_seconds: 15
```

新增项目环境变量：

```text
LLM_PROXY_OBSERVABILITY_ENABLED
LLM_PROXY_OBSERVABILITY_SERVICE_NAME
LLM_PROXY_OBSERVABILITY_OTLP_ENDPOINT
LLM_PROXY_OBSERVABILITY_TRACE_SAMPLE_RATIO
LLM_PROXY_OBSERVABILITY_METRICS_EXPORT_INTERVAL_SECONDS
```

### 优先级

Endpoint：

```text
OTEL_EXPORTER_OTLP_<SIGNAL>_ENDPOINT
  > OTEL_EXPORTER_OTLP_ENDPOINT
  > LLM_PROXY_OBSERVABILITY_OTLP_ENDPOINT
  > config.yaml
  > http://localhost:4318
```

Service name：

```text
OTEL_SERVICE_NAME
  > LLM_PROXY_OBSERVABILITY_SERVICE_NAME
  > config.yaml
  > llm-proxy
```

其余项目字段继续遵循：进程环境变量 > `.env` > `config.yaml` > 默认值。

认证 Header、TLS 证书、压缩和 exporter timeout 只使用标准环境变量：

```text
OTEL_EXPORTER_OTLP_HEADERS
OTEL_EXPORTER_OTLP_TRACES_HEADERS
OTEL_EXPORTER_OTLP_METRICS_HEADERS
OTEL_EXPORTER_OTLP_CERTIFICATE
OTEL_EXPORTER_OTLP_*_CERTIFICATE
OTEL_EXPORTER_OTLP_COMPRESSION
OTEL_EXPORTER_OTLP_*_COMPRESSION
OTEL_EXPORTER_OTLP_TIMEOUT
OTEL_EXPORTER_OTLP_*_TIMEOUT
```

仅支持 `http/protobuf`。如果设置 `OTEL_EXPORTER_OTLP_PROTOCOL`、`OTEL_EXPORTER_OTLP_TRACES_PROTOCOL` 或 `OTEL_EXPORTER_OTLP_METRICS_PROTOCOL`，其值必须为 `http/protobuf`，否则启动失败。

通用 OTLP/HTTP endpoint 是基础 URL，exporter 分别追加 `/v1/traces`、`/v1/metrics`；signal-specific endpoint 按标准原样使用。参考：[OTLP Exporter 配置](https://opentelemetry.io/docs/languages/sdk-configuration/otlp-exporter/)。

### 校验

- `otlp_endpoint` 必须是带 hostname 的绝对 HTTP(S) URL，不能含 query 或 fragment；
- `trace_sample_ratio` 必须在 `[0,1]`；
- `metrics_export_interval_seconds` 必须大于 0；
- `service_name` 去除空白后不能为空；
- 非法的 YAML、`LLM_PROXY_*` 或相关标准 OTel 环境变量导致启动失败。

## Traces、Metrics 与日志关联

### Traces

- 采样器：`ParentBased(TraceIDRatioBased(trace_sample_ratio))`；
- 默认采样率：0.1；
- 入站 span：每个 OpenAI/Anthropic 代理请求一个 server span；
- 上游 span：ReverseProxy 每次上游 HTTP 调用一个 client span；
- span 名称与 route 使用归一化端点，不包含 response ID 等动态段；
- HTTP 5xx 和网络错误按 OTel HTTP 语义标记；
- 不向 span 添加 API Key、请求体、响应体或模型内容。

### Metrics

使用 `otelhttp` 产生标准 HTTP server/client metrics，并增加以下业务 instruments：

```text
llm_proxy.requests                 Counter       unit: {request}
llm_proxy.requests.in_flight       UpDownCounter unit: {request}
llm_proxy.rate_limit.rejections    Counter       unit: {request}
```

业务属性只允许：

```text
llm.provider = openai | anthropic
llm.endpoint = responses | responses_compact | chat_completions | completions | messages | other
llm.outcome  = success | client_error | server_error | rate_limited | canceled
```

不使用模型、API Key、原始 path、request ID 作为 metric attribute，避免高基数。

Token 用量明确不写入 OpenTelemetry metrics，只进入 Dashboard。

### Zap

Logging Middleware 从 `trace.SpanContextFromContext(r.Context())` 获取上下文。SpanContext 有效时追加：

```text
trace_id
span_id
```

未采样但有效的上下文仍可写入关联 ID。健康端点和 Dashboard 沿用现有非代理日志行为，不额外记录访问日志。

## 健康检查与生命周期

新增：

```text
GET /healthz  → 200 {"status":"ok"}
GET /readyz   → 200 {"status":"ready"}
```

其他方法由 ServeMux 返回 405。

`Server` 内部维护原子 ready 状态：

- HTTP Server 开始监听前设置为 true；
- Shutdown 开始时先设置为 false；
- 非 ready 时 `/readyz` 返回 503 `{"status":"not_ready"}`。

readiness 不请求 OpenAI、Anthropic 或 Collector。Collector 临时不可达不能导致代理退出或 readiness 失败。

启动顺序：

```text
Load Config
→ Init Zap
→ Init Observability Runtime
→ Build Server and instrumented proxies
→ Start serving
```

关闭顺序：

```text
Mark not ready
→ HTTP Server Shutdown
→ Observability ForceFlush/Shutdown
→ Zap Sync
```

沿用 main 的 30 秒外层退出期限和 Server 的 10 秒 HTTP Shutdown 期限。OTel Shutdown 使用剩余的外层 Context，不创建更长的独立期限。

## Token 端点与计数规则

只统计以下实际生成请求：

```text
POST /openai/v1/responses
POST /openai/v1/responses/compact
POST /openai/v1/chat/completions
POST /openai/v1/completions
POST /anthropic/v1/messages
```

GET 历史响应、DELETE、取消、input token count、batch 查询等路径不统计，防止对同一生成结果重复计数。

只有 2xx 响应进入 Token 提交流程：

- 找到有效 usage：提交一次 Usage；
- 完整响应没有 usage、响应超限、JSON/SSE 无效或客户端中途断开：对应提供商的 missing usage 加一；
- 429、4xx、5xx 不提交 Token，也不增加 missing usage。

后台 Responses 初始响应若没有 usage 会记为 missing。后续 GET 即使返回 usage 也不统计，因为重复轮询无法在无持久化去重表的前提下可靠去重。这是透明、尽力统计的明确限制。

## Token 字段映射

### OpenAI Responses

```text
Input      = usage.input_tokens
Output     = usage.output_tokens
CacheRead  = usage.input_tokens_details.cached_tokens
CacheWrite = usage.input_tokens_details.cache_write_tokens
Reasoning  = usage.output_tokens_details.reasoning_tokens
```

普通响应读取顶层 `usage`。SSE 读取 `response.completed.response.usage`，流结束时只提交最后一个完整候选值。字段参考：[OpenAI Responses usage](https://developers.openai.com/api/reference/cli/resources/responses/methods/retrieve)。

### OpenAI Chat Completions / Completions

```text
Input      = usage.prompt_tokens
Output     = usage.completion_tokens
CacheRead  = usage.prompt_tokens_details.cached_tokens
CacheWrite = usage.prompt_tokens_details.cache_write_tokens
Reasoning  = usage.completion_tokens_details.reasoning_tokens
```

SSE 只在上游实际返回非空 usage chunk 时统计。代理不添加 `stream_options.include_usage`。字段参考：[OpenAI Chat Completions](https://developers.openai.com/api/reference/cli/resources/chat/subresources/completions)。

### Anthropic Messages

```text
Input      = usage.input_tokens
           + usage.cache_creation_input_tokens
           + usage.cache_read_input_tokens
Output     = usage.output_tokens
CacheRead  = usage.cache_read_input_tokens
CacheWrite = usage.cache_creation_input_tokens
Reasoning  = usage.output_tokens_details.thinking_tokens
```

Anthropic SSE 的 `message_delta.usage` 是累计值。Observer 合并 `message_start.message.usage` 和后续 `message_delta.usage`，保留每个字段的最新已提供值，并在 `message_stop`/EOF 时提交一次。参考：[Anthropic Streaming](https://platform.claude.com/docs/en/build-with-claude/streaming) 和 [Prompt Caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)。

Reasoning/Thinking 与缓存均为 Input/Output 的明细，不再次加入总 Token。Dashboard 总 Token 固定为 `Input + Output`。

## Token 捕获边界

### 普通 JSON

- 捕获上限为每请求 2 MiB；
- 捕获不延迟底层 Write；
- 完成时使用 `encoding/json` 解析；
- 超过上限后立即停止捕获并标记 unavailable，不截断或阻止客户端响应。

### SSE

- 按空行切分事件，支持任意网络/Write 分块；
- 每个事件的捕获上限为 2 MiB；
- 只解析 `data:` 行，忽略 comment、ping 和未知事件；
- 单事件超限后跳过该事件，继续透传并处理后续事件；
- 保存最新 Usage 候选，不累加每个 delta；
- EOF、Close 和写入错误都必须让 Observer 生命周期结束，且提交回调最多执行一次。

2 MiB 是内存安全边界，不提供配置项。Observer 不保留请求间数据，Finish 后释放捕获字节。

## Dashboard

增加中文 Token 区域。

顶部卡片：

- 总 Token；
- 输入 Token；
- 输出 Token；
- Usage 缺失请求数。

明细表固定三行：总计、OpenAI、Anthropic。列为：

```text
提供商 | 总 Token | 输入 | 输出 | 缓存读取 | 缓存写入 | 推理 | Usage 缺失
```

页面使用现有 JSON 注入机制，不新增 Dashboard API，不增加自动刷新请求。大数使用前端现有格式化风格。

## 错误处理

- OTel 配置或 exporter 创建失败：启动失败并返回带上下文的错误；
- Collector 运行期不可达：交给 exporter 重试并通过 OTel ErrorHandler 写 Zap，代理继续工作；
- Token parser 错误：不写响应、不记录响应正文，只更新 missing usage；
- OTel Shutdown 的 trace/metric 错误使用 `errors.Join` 汇总；
- HTTP Shutdown 与 OTel Shutdown 错误分别记录，不互相短路；
- 所有后台导出 goroutine 由 SDK Provider 拥有，并在 Shutdown 中结束。

## 测试策略

### 配置

- 默认关闭；
- YAML 和全部 `LLM_PROXY_OBSERVABILITY_*`；
- 标准 `OTEL_EXPORTER_OTLP_*`、`OTEL_SERVICE_NAME` 优先级；
- endpoint、协议、采样率、周期、service name 的非法值；
- `.env` 与进程环境变量优先级。

### OpenTelemetry

- 使用 `tracetest.SpanRecorder` 验证 server/client span 父子关系；
- 使用 ManualReader 验证业务 Counter、UpDownCounter 和属性集合；
- 验证采样率 0、1 与父采样决定；
- 验证 Logging 只在 SpanContext 有效时增加 trace 字段；
- 使用 `httptest.Server` 和 ForceFlush 验证 OTLP HTTP 请求路径 `/v1/traces`、`/v1/metrics`；
- 验证 Shutdown 调用幂等且无 goroutine 泄漏。

### Token parser

表驱动测试覆盖：

- Responses、Chat Completions、Completions 普通 JSON；
- Responses completed event、Chat usage chunk；
- Anthropic message_start/message_delta/message_stop；
- SSE 在每个字节边界、多个事件边界和 CRLF 下的分块；
- 重复/累计 usage 只提交一次；
- 无 usage、字段缺失、负数、无效 JSON、未知事件、2 MiB 边界和超限；
- 4xx/5xx/429 不计 missing；
- 客户端中断计 missing；
- 并发原子累计和 `go test -race`。

解析到负数时整份 usage 无效。缺失的可选明细字段按 0 处理；Input 和 Output 主字段必须存在且非负。

### HTTP 与 Dashboard

- Observer 包装后状态码、响应字节和 Header 完全不变；
- SSE 首个事件仍可在上游完成前被读取；
- `/healthz`、`/readyz` 的 ready/not-ready 和方法限制；
- Dashboard JSON 注入和 Token 表格字段；
- 现有限流、日志、统计和代理测试全部保持通过。

### 完成门禁

```bash
gofmt -w <changed-go-files>
go test ./...
go test -race ./...
go vet ./...
go build -o /tmp/llm-proxy ./cmd/proxy
git diff --check
```

## 验收标准

- `observability.enabled=false` 时行为与当前版本一致且不尝试连接 Collector；
- 启用后通过 OTLP/HTTP 导出入站/上游 traces 和 HTTP/代理 metrics；
- Zap 请求日志可通过 trace_id/span_id 关联 trace；
- 健康端点正确反映进程和 ready 生命周期，不依赖外部服务；
- Dashboard 正确累计总计/OpenAI/Anthropic Token；
- JSON 与 SSE 统计不改变响应透传和 Flush；
- usage 缺失与已知限制对用户可见；
- 不产生敏感信息或高基数遥测属性；
- 全量测试、竞态测试、静态检查和构建通过。
