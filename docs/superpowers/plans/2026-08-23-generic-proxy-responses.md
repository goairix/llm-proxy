# Generic Proxy and OpenAI Responses API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 抽取通用 HTTP 反向代理核心，并保证 OpenAI SDK 以 `/openai/v1` 为基地址时完整支持 Responses API 及 SSE。

**Architecture:** 包内私有的 `newReverseProxy(options)` 统一负责上游 URL 校验、前缀移除、目标基础路径拼接和 Host 重写；现有提供商构造函数变为公开薄封装。Responses API 不引入协议模型，通过 `httputil.ReverseProxy` 透明转发方法、查询、头、正文、状态和流。

**Tech Stack:** Go 1.25、`net/http`、`net/http/httputil`、`httptest`

## Global Constraints

- 公开路由保持 `/openai/*` 和 `/anthropic/*`，不新增 `/v1/*`。
- 客户端 SDK `base_url` 使用 `http://localhost:8080/openai/v1`；项目的 `providers.openai.base_url` 仍表示真实上游。
- 不解析或转换 OpenAI/Anthropic 请求响应协议。
- 保留 SSE Flush、限流、日志、统计和中间件顺序。
- 不创建 worktree，直接在用户已授权的当前分支执行。

---

### Task 1: 通用反向代理核心

**Files:**
- Create: `internal/proxy/proxy.go`
- Modify: `internal/proxy/proxy_test.go`

**Interfaces:**
- Produces: `type options struct { BaseURL string; StripPrefix string }`
- Produces: `func newReverseProxy(opts options) (http.Handler, error)`

- [x] **Step 1: 写入通用代理的失败测试**

在 `internal/proxy/proxy_test.go` 增加表驱动 URL 校验测试，以及一个真实 `httptest.Server` 测试：上游地址含 `/gateway`、请求为 `/openai/v1/responses?include=usage` 时，上游收到 `/gateway/v1/responses?include=usage` 和上游 Host。

```go
func TestReverseProxy_RewritesPathBeforeJoiningBasePath(t *testing.T) {
    var gotPath, gotQuery, gotHost string
    upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotPath, gotQuery, gotHost = r.URL.Path, r.URL.RawQuery, r.Host
        w.WriteHeader(http.StatusNoContent)
    }))
    defer upstream.Close()

    handler, err := newReverseProxy(options{
        BaseURL: upstream.URL + "/gateway",
        StripPrefix: "/openai",
    })
    if err != nil {
        t.Fatalf("newReverseProxy returned unexpected error: %v", err)
    }
    proxyServer := httptest.NewServer(handler)
    defer proxyServer.Close()

    resp, err := http.Get(proxyServer.URL + "/openai/v1/responses?include=usage")
    if err != nil {
        t.Fatalf("GET request failed: %v", err)
    }
    resp.Body.Close()

    if gotPath != "/gateway/v1/responses" { t.Errorf("path = %q", gotPath) }
    if gotQuery != "include=usage" { t.Errorf("query = %q", gotQuery) }
    target, _ := url.Parse(upstream.URL)
    if gotHost != target.Host { t.Errorf("host = %q", gotHost) }
}
```

- [x] **Step 2: 运行测试并确认 RED**

Run: `go test ./internal/proxy -run 'TestReverseProxy' -count=1`

Expected: 编译失败，提示 `undefined: newReverseProxy` 或 `undefined: options`。

- [x] **Step 3: 实现最小通用代理**

在 `internal/proxy/proxy.go` 实现 URL 校验与 Director：先移除 Path/RawPath 前缀，再调用默认 Director，最后设置 `req.Host`。

```go
type options struct {
    BaseURL     string
    StripPrefix string
}

func newReverseProxy(opts options) (http.Handler, error) {
	 target, err := url.Parse(opts.BaseURL)
    if err != nil {
        return nil, fmt.Errorf("parse base URL: %w", err)
    }
    if target.Scheme != "http" && target.Scheme != "https" {
        return nil, fmt.Errorf("base URL scheme must be http or https")
    }
    if target.Hostname() == "" {
        return nil, fmt.Errorf("base URL must include a host")
    }

    reverseProxy := httputil.NewSingleHostReverseProxy(target)
    defaultDirector := reverseProxy.Director
    reverseProxy.Director = func(req *http.Request) {
        req.URL.Path = strings.TrimPrefix(req.URL.Path, opts.StripPrefix)
        if req.URL.RawPath != "" {
            req.URL.RawPath = strings.TrimPrefix(req.URL.RawPath, opts.StripPrefix)
        }
        defaultDirector(req)
        req.Host = target.Host
    }
    return reverseProxy, nil
}
```

- [x] **Step 4: 运行通用代理测试并确认 GREEN**

Run: `go test ./internal/proxy -run 'TestReverseProxy' -count=1`

Expected: `ok github.com/goairix/llm-proxy/internal/proxy`。

### Task 2: 提供商薄封装与兼容性

**Files:**
- Modify: `internal/proxy/openai.go`
- Modify: `internal/proxy/anthropic.go`
- Modify: `internal/proxy/proxy_test.go`

**Interfaces:**
- Consumes: `newReverseProxy(options)`
- Preserves: `NewOpenAIProxy(baseURL string) (http.Handler, error)`
- Preserves: `NewAnthropicProxy(baseURL string) (http.Handler, error)`

- [x] **Step 1: 增加提供商基础路径失败测试**

分别用带 `/gateway` 基础路径的上游创建 OpenAI 和 Anthropic 代理。OpenAI 用真实 Responses 路径并断言 `/openai/v1/responses` 被转发为 `/gateway/v1/responses`；Anthropic 断言 `/anthropic/v1/messages` 被转发为 `/gateway/v1/messages`。现有实现会保留公开前缀，因此测试必须先失败。

- [x] **Step 2: 运行测试并确认 RED**

Run: `go test ./internal/proxy -run TestProviderProxies_JoinUpstreamBasePathAfterStrippingPrefix -count=1`

Expected: 路径断言失败，实际值包含 `/gateway/openai/` 或 `/gateway/anthropic/`。

- [x] **Step 3: 将两个构造函数改为薄封装**

```go
func NewOpenAIProxy(baseURL string) (http.Handler, error) {
    return newReverseProxy(options{BaseURL: baseURL, StripPrefix: "/openai"})
}

func NewAnthropicProxy(baseURL string) (http.Handler, error) {
    return newReverseProxy(options{BaseURL: baseURL, StripPrefix: "/anthropic"})
}
```

- [x] **Step 4: 运行代理包全量测试并确认 GREEN**

Run: `go test ./internal/proxy -count=1`

Expected: 包内全部测试通过。

### Task 3: Responses API 普通请求透明转发特征测试

**Files:**
- Modify: `internal/proxy/proxy_test.go`

**Interfaces:**
- Consumes: `NewOpenAIProxy(baseURL string)`
- Verifies: `POST /openai/v1/responses` -> upstream `POST /v1/responses`

- [x] **Step 1: 写 Responses 请求响应特征测试**

用 `httptest.Server` 捕获方法、路径、查询参数、Authorization、Content-Type 和 JSON 请求体，并返回非 200 状态、自定义响应头和 JSON 正文。通过 OpenAI 代理请求 `/openai/v1/responses?include=reasoning.encrypted_content`，逐项断言请求及响应完全透传。

这是对标准库 ReverseProxy 已有透明行为的重构保护测试，不新增生产代码；路径重写缺陷已经由 Task 2 的 Responses 基础路径测试完成 RED-GREEN 验证。

- [x] **Step 2: 运行特征测试并确认 GREEN**

Run: `go test ./internal/proxy -run TestNewOpenAIProxy_ResponsesAPI -count=1`

Expected: 测试通过，证明 Responses 方法、URL、请求和响应均透明转发。

### Task 4: Responses API SSE 实时 Flush

**Files:**
- Modify: `internal/proxy/proxy_test.go`

**Interfaces:**
- Consumes: `NewOpenAIProxy(baseURL string)`
- Verifies: 上游 `http.Flusher` 的首个 SSE 事件可在响应结束前被客户端读取

- [x] **Step 1: 写 SSE 实时到达特征测试**

上游写入并 Flush 第一个 `response.output_text.delta` 事件，然后阻塞等待测试释放；客户端必须在释放上游前读到首行。用 channel 同步事件顺序，避免以固定 sleep 判断流式行为。

- [x] **Step 2: 运行特征测试并确认 GREEN**

Run: `go test ./internal/proxy -run TestNewOpenAIProxy_ResponsesAPIStreaming -count=1`

Expected: 首个 SSE 事件在上游结束前到达，测试通过。

### Task 5: 使用文档与最终验证

**Files:**
- Modify: `README.md`

**Interfaces:**
- Documents: OpenAI Responses curl、Python SDK、Node SDK 和两种 `base_url` 的区别

- [x] **Step 1: 更新 README**

增加 `/openai/v1/responses` 示例和 SDK 配置：

```python
client = OpenAI(
    api_key=os.environ["OPENAI_API_KEY"],
    base_url="http://localhost:8080/openai/v1",
)
response = client.responses.create(model="gpt-5", input="Hello")
```

明确 `providers.openai.base_url` 是上游地址，配置为本机代理会导致循环代理；同步项目结构中的通用代理文件。

- [x] **Step 2: 格式化并运行受影响测试**

Run: `gofmt -w internal/proxy/*.go`

Run: `go test ./internal/proxy -count=1`

Expected: 代理包测试通过。

- [x] **Step 3: 完整验证**

Run: `go test ./...`

Run: `go test -race ./...`

Run: `go build -o /tmp/llm-proxy ./cmd/proxy`

Run: `git diff --check`

Expected: 全部命令退出码为 0；竞态测试无 race；差异检查无输出。

- [x] **Step 4: 检查变更范围**

Run: `git status --short && git diff --stat && git diff -- internal/proxy README.md docs/superpowers`

Expected: 仅包含本设计、计划、代理实现/测试和 README，无二进制、日志、密钥或无关文件。
