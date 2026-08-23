# 通用代理层与 OpenAI Responses API 设计

## 目标

在不改变现有公开路由、配置结构、限流和统计语义的前提下，抽取可复用的 HTTP 反向代理核心，并明确支持 OpenAI Responses API。

客户端继续将 OpenAI SDK 的 `base_url` 配置为：

```text
http://localhost:8080/openai/v1
```

SDK 调用 `responses.create` 时，请求链路为：

```text
POST /openai/v1/responses -> 去掉 /openai -> 上游 POST /v1/responses
```

项目自身的 `providers.openai.base_url` 仍表示上游地址，例如 `https://api.openai.com`，不能填写本机代理地址。

## 设计

### 通用代理核心

`internal/proxy` 新增：

```go
type options struct {
    BaseURL     string
    StripPrefix string
}

func newReverseProxy(opts options) (http.Handler, error)
```

它负责：

- 校验 `BaseURL` 是带主机名的绝对 HTTP(S) URL；
- 在标准库 Director 拼接目标基础路径之前去掉公开路由前缀；
- 同时重写 `URL.Path` 和非空的 `URL.RawPath`；
- 让标准库保留查询参数、请求体、请求头、响应状态、响应头和流式响应；
- 将请求 `Host` 设置为上游主机。

`NewOpenAIProxy` 和 `NewAnthropicProxy` 保留为稳定的包外提供商入口；`newReverseProxy` 和 `options` 保持包内私有，只负责接收 `/openai`、`/anthropic` 两组参数，避免服务器层了解路径重写细节。

### 路径语义

去前缀必须发生在 `httputil.NewSingleHostReverseProxy` 的默认 Director 之前。这样当上游地址包含子路径时仍能正确拼接：

```text
公开请求： /openai/v1/responses
上游配置： https://gateway.example.com/api
最终路径： /api/v1/responses
```

而不是错误的 `/api/openai/v1/responses`。

仅移除路径开头的一次精确前缀；ServeMux 继续保证 OpenAI 和 Anthropic 请求分别进入对应 handler。

### Responses API 兼容性

Responses API 采用透明代理，不解析或转换 JSON。至少覆盖：

- `POST /openai/v1/responses` 的方法、查询参数、请求头和请求体透传；
- 上游响应状态、响应头和响应体透传；
- `text/event-stream` 响应在上游 Flush 后即可被客户端读取；
- 后续 `GET /responses/{id}`、`DELETE /responses/{id}`、取消等资源路径由同一通用规则自然支持。

### 错误处理

启动时拒绝以下上游地址：

- 语法错误的 URL；
- 相对 URL 或缺少主机名的 URL；
- 非 `http`/`https` scheme。

错误通过现有 `server.New` 返回，不增加运行时回退。

## 不在本次范围

- OpenAI 与 Anthropic 协议互转；
- 请求或响应 JSON schema 校验；
- 重试、熔断、负载均衡和动态上游；
- 新增 Responses 专用配置项；
- 改动中间件顺序、限流规则、统计字段或公开路由。

## 验收

- 现有 `/openai/*`、`/anthropic/*` 行为保持兼容；
- 通用代理支持带基础路径的上游 URL；
- SDK 基地址 `http://localhost:8080/openai/v1` 对应上游 `/v1/responses`；
- Responses 普通响应与 SSE 流式响应均有自动化测试；
- `go test ./...`、`go test -race ./...` 和 `go build ./cmd/proxy` 通过。
