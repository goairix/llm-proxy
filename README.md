# LLM Proxy

A lightweight reverse proxy for OpenAI and Anthropic APIs, with per-key rate limiting, OpenTelemetry observability, structured logging, and a built-in dashboard.

## 功能特性

- **透明转发** — 请求头（含 `Authorization`、`x-api-key`）原样传递，无需修改客户端
- **Responses API** — 支持 OpenAI Responses API 的普通响应、后台响应和 SSE 流式事件
- **SSE 流式响应** — 原生支持 streaming，延迟零增加
- **按 Key 限流** — Token Bucket 算法，每个 API Key 独立计数，支持白名单和自定义配额
- **可观测性** — 可选 OTLP/HTTP traces 与 metrics，上游 client span 自动关联，Zap 日志包含 `trace_id` / `span_id`
- **健康检查** — 提供轻量的存活与就绪端点，不依赖上游 API 或 Collector 状态
- **结构化日志** — zap 输出至 stdout + 按天轮转文件，记录延迟、状态码、流量和 Trace 关联字段
- **Web 控制台** — 内嵌静态页面，展示运行状态、请求统计、Token 用量和限流配置
- **单二进制部署** — 静态编译，无外部依赖

## 路由规则

| 请求路径 | 转发至 |
|----------|--------|
| `GET /` | Web 控制台 |
| `GET /healthz` | 存活检查，进程可响应时返回 200 |
| `GET /readyz` | 就绪检查，HTTP 服务已进入服务状态时返回 200 |
| `/openai/*` | `https://api.openai.com/*` |
| `/anthropic/*` | `https://api.anthropic.com/*` |

**示例：**

```bash
# OpenAI
curl http://localhost:8080/openai/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4o", "messages": [{"role": "user", "content": "Hello"}]}'

# OpenAI Responses API
curl http://localhost:8080/openai/v1/responses \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-5", "input": "Hello"}'

# Anthropic
curl http://localhost:8080/anthropic/v1/messages \
  -H "x-api-key: $ANTHROPIC_API_KEY" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{"model": "claude-opus-4-6", "max_tokens": 1024, "messages": [{"role": "user", "content": "Hello"}]}'
```

### OpenAI SDK

OpenAI 官方 SDK 的 API 基地址应指向代理的 `/openai/v1`。SDK 会在其后追加 `/responses` 等资源路径：

```python
import os
from openai import OpenAI

client = OpenAI(
    api_key=os.environ["OPENAI_API_KEY"],
    base_url="http://localhost:8080/openai/v1",
)

response = client.responses.create(
    model="gpt-5",
    input="Hello",
)
print(response.output_text)
```

```javascript
import OpenAI from "openai";

const client = new OpenAI({
  apiKey: process.env.OPENAI_API_KEY,
  baseURL: "http://localhost:8080/openai/v1",
});

const response = await client.responses.create({
  model: "gpt-5",
  input: "Hello",
});
console.log(response.output_text);
```

这里有两种用途不同的基地址：

- SDK 的 `base_url` / `baseURL`：代理入口，例如 `http://localhost:8080/openai/v1`。
- 本项目的 `providers.openai.base_url`：真实上游，例如 `https://api.openai.com`。

不要将 `providers.openai.base_url` 指向当前代理自身，否则请求会形成循环代理。若上游网关包含基础路径，例如 `https://gateway.example.com/api`，代理会将 `/openai/v1/responses` 正确转发为 `/api/v1/responses`。

## 快速开始

### 直接运行

**环境要求：** Go 1.25+

```bash
git clone https://github.com/goairix/llm-proxy.git
cd llm-proxy

# 可直接编辑 config.yaml，也可使用 .env
cp .env.example .env

go run ./cmd/proxy
```

### Docker

```bash
# 构建镜像
docker build -t llm-proxy:latest .

# 运行（挂载配置文件）
docker run -d \
  -p 8080:8080 \
  -v /path/to/config.yaml:/app/config.yaml \
  -v /path/to/logs:/app/logs \
  llm-proxy:latest
```

## 配置

启动时从当前目录读取 `config.yaml` 和可选的 `.env`，缺失字段自动使用默认值。

```yaml
server:
  port: 8080
  # 控制台页面显示的外部访问地址，留空则自动使用 http://localhost:<port>
  show_base_url: ""

log:
  level: info          # debug | info | warn | error
  file: ./logs/proxy.log  # 留空则只输出到 stdout
  max_age: 30          # 日志文件保留天数

rate_limit:
  enabled: true
  default:
    requests_per_second: 10
    burst: 20
  whitelist:           # 白名单 Key 完全跳过限流
    - "sk-internal-key"
  overrides:           # 为特定 Key 设置独立配额
    "sk-high-volume":
      requests_per_second: 100
      burst: 200

providers:
  openai:
    base_url: "https://api.openai.com"   # 真实上游，可替换为兼容 OpenAI 协议的第三方地址
  anthropic:
    base_url: "https://api.anthropic.com"

observability:
  enabled: false
  service_name: "llm-proxy"
  otlp_endpoint: "http://localhost:4318"
  trace_sample_ratio: 0.1
  metrics_export_interval_seconds: 15
```

### 环境变量

配置优先级为：进程环境变量 > `.env` > `config.yaml` > 默认值。

```bash
cp .env.example .env
```

变量名使用 `LLM_PROXY_` 前缀，并将配置层级转为大写下划线，例如 `server.port` 对应 `LLM_PROXY_SERVER_PORT`。完整列表见 `.env.example`。

`LLM_PROXY_RATE_LIMIT_WHITELIST` 必须是 JSON 字符串数组，`LLM_PROXY_RATE_LIMIT_OVERRIDES` 必须是 JSON 对象：

```dotenv
LLM_PROXY_RATE_LIMIT_WHITELIST=["sk-a","sk-b"]
LLM_PROXY_RATE_LIMIT_OVERRIDES={"sk-a":{"requests_per_second":100,"burst":200}}
```

无效的数字、布尔值或 JSON 会导致启动失败，错误信息会包含对应的环境变量名。

### OpenTelemetry

`observability.enabled` 默认关闭。启用后，代理通过 OTLP/HTTP protobuf 导出 traces 和 metrics；不支持 OTLP/gRPC、OTel Logs、Profiles 或 Prometheus exporter。

```yaml
observability:
  enabled: true
  service_name: "llm-proxy"
  otlp_endpoint: "http://otel-collector:4318"
  trace_sample_ratio: 0.1
  metrics_export_interval_seconds: 15
```

也可以使用 OpenTelemetry 标准环境变量配置 Collector 和传输细节：

```dotenv
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_EXPORTER_OTLP_HEADERS=Authorization=Bearer%20<token>
OTEL_EXPORTER_OTLP_CERTIFICATE=/path/to/collector-ca.pem
OTEL_EXPORTER_OTLP_TIMEOUT=10000
```

Endpoint 优先级为：signal-specific `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` / `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` > `OTEL_EXPORTER_OTLP_ENDPOINT` > `LLM_PROXY_OBSERVABILITY_OTLP_ENDPOINT` > YAML/default。`OTEL_SERVICE_NAME` 优先于项目的 `service_name`。标准 OTel Header、证书、压缩和 timeout 变量仍由官方 exporter 处理。

Trace 默认使用 `ParentBased(TraceIDRatioBased(0.1))`，可通过 `trace_sample_ratio` 调整。业务 metrics 包括累计请求、当前处理请求和 429 拒绝；Token 数据不会导出到 OTel。遥测 URL 会归一化，不记录动态资源 ID、query、API Key、模型名或请求/响应正文。

### Dashboard Token 统计

Dashboard 会尽力从以下成功响应中解析累计 Token 用量，并按总计、OpenAI、Anthropic 展示：

- `POST /openai/v1/responses`
- `POST /openai/v1/responses/compact`
- `POST /openai/v1/chat/completions`
- `POST /openai/v1/completions`
- `POST /anthropic/v1/messages`

OpenAI 的 Input/Output 对应 `input_tokens`/`output_tokens` 或 `prompt_tokens`/`completion_tokens`；Anthropic Input 包含普通输入、cache creation 和 cache read。Dashboard 还分别展示 cache read、cache write、reasoning/thinking，以及成功但未携带可解析 usage 的请求数。

统计只保存在进程内，重启后清零。代理不会修改客户端请求来强制流式响应返回 usage；因此部分 SSE 响应会计入“缺失 Usage”。后台 Responses 的初始成功响应也可能缺少 usage，后续 retrieve 请求不会补记，以避免轮询造成重复统计。4xx、429 和 5xx 不计入 Token 或缺失 Usage。

### 健康检查

- `/healthz` 只反映进程 HTTP handler 可响应。
- `/readyz` 在服务开始监听时变为 ready，在关闭开始时立即变为 not ready。

上游 OpenAI/Anthropic API 和 OTel Collector 都不是 readiness 依赖，健康检查不会主动探测它们。

### 限流说明

限流基于 **Token Bucket（令牌桶）** 算法，按 API Key 独立计算：

- `requests_per_second` — 令牌补充速率，即稳定吞吐上限
- `burst` — 桶容量，控制瞬时突发峰值，建议设为 `requests_per_second` 的 2 倍
- 超出限制返回 `429 Too Many Requests`

**生效优先级：** 白名单（豁免）> `overrides`（自定义）> `default`（兜底）

**API Key 提取规则：**

| 服务商 | 提取来源 |
|--------|----------|
| OpenAI | `Authorization: Bearer <key>` |
| Anthropic | `x-api-key: <key>`，其次 `Authorization: Bearer <key>` |

## 项目结构

```
llm-proxy/
├── cmd/proxy/main.go              # 入口：加载配置、初始化、优雅关闭
├── internal/
│   ├── config/                    # 配置加载（viper）
│   ├── logger/                    # 日志初始化（zap + file-rotatelogs）
│   ├── middleware/
│   │   ├── logging.go             # 请求日志中间件
│   │   └── ratelimit.go           # 限流中间件
│   ├── proxy/
│   │   ├── proxy.go               # 通用反向代理核心
│   │   ├── openai.go              # OpenAI 反向代理
│   │   └── anthropic.go           # Anthropic 反向代理
│   ├── observability/              # OTel Runtime、HTTP 插桩和业务 metrics
│   ├── tokenusage/                 # JSON/SSE usage 解析与归一化
│   ├── server/                    # HTTP 服务器组装与路由
│   └── dashboard/                 # 原子统计、Web 控制台 handler 与内嵌 HTML
├── config.yaml                    # 配置文件
└── Dockerfile                     # 多阶段构建
```

## 请求处理流程

```
客户端请求
    │
    ▼
[OTel Server]     ← 入站 span、标准 HTTP metrics、低基数业务 metrics
    │
    ▼
[日志中间件]     ← 记录请求，关联 trace_id / span_id
    │
    ▼
[统计与 Token Observer] ← 更新控制台请求统计，透明解析 JSON/SSE usage
    │
    ▼
[限流中间件]     ← 提取 API Key，检查令牌桶
    │
    ▼
[ReverseProxy + OTel Transport] ← 转发请求，创建上游 client span，流式透传响应
    │
    ▼
上游 API（OpenAI / Anthropic）
```

## 依赖

| 包 | 用途 |
|----|------|
| `go.uber.org/zap` | 结构化日志 |
| `github.com/lestrrat-go/file-rotatelogs` | 日志文件轮转 |
| `golang.org/x/time/rate` | Token Bucket 限流 |
| `github.com/spf13/viper` | YAML 配置加载 |
| `github.com/joho/godotenv` | `.env` 文件加载 |
| `go.opentelemetry.io/otel` | Traces、metrics、上下文传播与 SDK |
| `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` | HTTP server/client 自动插桩 |

## License

MIT
