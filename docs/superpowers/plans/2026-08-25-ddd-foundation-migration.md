# 阶段一 A：DDD 基础迁移实施计划

> **供执行代理使用：** 必须使用 `superpowers:subagent-driven-development`（推荐）或 `superpowers:executing-plans`，逐任务执行本计划。所有步骤使用 `- [ ]` 复选框跟踪。

**目标：** 在不改变任何公开行为的前提下，把现有项目迁移为 DDD/Clean Architecture 五层骨架，并用 Google Wire 完成依赖装配。

**架构：** 先用端到端特征测试锁定透明代理，再从叶子包向组装根小步迁移。技术实现进入 `infrastructure`，HTTP 适配进入 `interfaces`，运行期共享契约进入 `application/runtime`，最后拆分 Router 与 Server，并由 `internal/di` 统一装配。

**技术栈：** Go 1.25、标准库 `net/http`、`httputil.ReverseProxy`、Google Wire、Zap、Viper、OpenTelemetry、`httptest`。

**设计依据：** `docs/superpowers/specs/2026-08-24-multi-provider-unified-gateway-ddd-design.md`

---

## 一、范围与约束

本计划只做阶段一 A，不增加 `/v1/chat/completions`、`/v1/messages`、统一协议模型或新供应商连接器。

必须保持：

- `/openai/*`、`/anthropic/*` 路由、Path/RawPath、Host 和 Header 行为；
- 中间件顺序 `OTel → Logging → Stats/Token → RateLimiter → ReverseProxy`；
- SSE 首事件 Flush、Token 统计和错误体透传；
- `/healthz`、`/readyz`、Dashboard 及未知路径被根 Handler 接住的现状；
- OTel URL 归一化、Span 父子关系、关闭顺序和配置优先级；
- 所有现有配置字段、环境变量和 Dashboard JSON。

最终文件职责：

```text
internal/application/runtime/                 # Readiness、Version、透明代理 Usage 端口
internal/interfaces/http/handler/dashboard/   # Dashboard HTTP 适配器和内嵌 HTML
internal/interfaces/http/handler/health/      # 健康检查 HTTP 适配器
internal/interfaces/http/middleware/          # Logging、RateLimiter、Stats
internal/interfaces/http/router/              # ServeMux 与中间件组装
internal/infrastructure/config/                # Viper、YAML、.env
internal/infrastructure/logger/                # Zap
internal/infrastructure/observability/         # OTel Runtime 与 HTTP 插桩
internal/infrastructure/proxy/                 # ReverseProxy
internal/infrastructure/proxy/tokenusage/      # 透明代理 Token 观察器
internal/infrastructure/server/http/           # ListenAndServe、Readiness、Shutdown
internal/di/                                   # Wire 组装根
```

## 二、任务

### 任务 1：建立迁移基线并补充透明代理端到端特征测试

**文件：**

- 修改：`internal/server/server_test.go`
- 不修改生产代码

- [ ] **步骤 1：记录迁移前基线**

运行：

```bash
go test ./...
go test -race ./internal/observability ./internal/middleware ./internal/proxy ./internal/server
go build -o /tmp/llm-proxy ./cmd/proxy
git status --short
```

预期：全部通过；`git status --short` 为空。若失败，先记录并修复基线问题，不进入目录迁移。

- [ ] **步骤 2：添加双供应商端到端特征测试**

在 `internal/server/server_test.go` 增加：

```go
func TestServerTransparentProviderRoutes(t *testing.T) {
	type upstreamRequest struct {
		path          string
		authorization string
		anthropicKey  string
	}
	received := make(chan upstreamRequest, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- upstreamRequest{
			path:          r.URL.EscapedPath(),
			authorization: r.Header.Get("Authorization"),
			anthropicKey:  r.Header.Get("x-api-key"),
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	telemetry, err := observability.New(context.Background(), config.ObservabilityConfig{}, Version, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Server:    config.ServerConfig{Port: 8080},
		RateLimit: config.RateLimitConfig{Enabled: false},
		Providers: config.ProvidersConfig{
			OpenAI:    config.ProviderConfig{BaseURL: upstream.URL},
			Anthropic: config.ProviderConfig{BaseURL: upstream.URL},
		},
	}
	srv, err := New(cfg, zap.NewNop(), telemetry)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, path, authorization, anthropicKey, wantPath string
	}{
		{name: "openai", path: "/openai/v1/chat/completions", authorization: "Bearer sk-openai", wantPath: "/v1/chat/completions"},
		{name: "anthropic", path: "/anthropic/v1/messages", anthropicKey: "sk-ant", wantPath: "/v1/messages"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{}`))
			req.Header.Set("Authorization", tc.authorization)
			req.Header.Set("x-api-key", tc.anthropicKey)
			rec := httptest.NewRecorder()
			srv.httpServer.Handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || rec.Body.String() != `{"ok":true}` {
				t.Fatalf("response = %d %q", rec.Code, rec.Body.String())
			}
			got := <-received
			if got.path != tc.wantPath || got.authorization != tc.authorization || got.anthropicKey != tc.anthropicKey {
				t.Fatalf("upstream request = %+v", got)
			}
		})
	}
}
```

- [ ] **步骤 3：运行新增测试，确认当前实现通过**

运行：

```bash
go test ./internal/server -run TestServerTransparentProviderRoutes -v
```

预期：PASS。该测试是特征测试，不要求先失败；它记录现有组装语义。

- [ ] **步骤 4：提交特征测试**

```bash
git add internal/server/server_test.go
git commit -m "test: lock transparent proxy route composition"
```

### 任务 2：建立 `application/runtime` 稳定契约

**文件：**

- 新建：`internal/application/runtime/usage.go`
- 新建：`internal/application/runtime/readiness.go`
- 新建：`internal/application/runtime/readiness_test.go`
- 新建：`internal/application/runtime/version.go`
- 新建：`internal/domain/doc.go`
- 修改：`internal/tokenusage/observer.go`
- 修改：`internal/tokenusage/json.go`
- 修改：`internal/tokenusage/sse.go`
- 修改：`internal/dashboard/handler.go`
- 修改：`internal/server/server.go`

- [ ] **步骤 1：先写 Readiness 失败测试**

```go
package runtime

import "testing"

func TestReadiness(t *testing.T) {
	state := NewReadiness()
	if state.Ready() {
		t.Fatal("new state is ready")
	}
	state.SetReady(true)
	if !state.Ready() {
		t.Fatal("state did not become ready")
	}
	state.SetReady(false)
	if state.Ready() {
		t.Fatal("state did not become not-ready")
	}
}
```

运行：

```bash
go test ./internal/application/runtime -run TestReadiness -v
```

预期：FAIL，提示 `NewReadiness` 未定义。

- [ ] **步骤 2：实现运行期契约**

`readiness.go`：

```go
package runtime

import "sync/atomic"

type Readiness struct{ ready atomic.Bool }

func NewReadiness() *Readiness { return &Readiness{} }
func (s *Readiness) Ready() bool { return s != nil && s.ready.Load() }
func (s *Readiness) SetReady(ready bool) { s.ready.Store(ready) }
```

`version.go`：

```go
package runtime

const Version = "1.0.0"
```

`usage.go`：

```go
package runtime

type TokenUsage struct {
	Input, Output, CacheRead, CacheWrite, Reasoning int64
}

type UsageResult struct {
	Usage   TokenUsage
	Present bool
}

type UsageObserver interface {
	Observe(contentType string, chunk []byte)
	Finish(status int, writeErr error) UsageResult
}

type UsageObserverFactory func(provider, method, path string) UsageObserver
```

`internal/domain/doc.go` 只声明领域层根包和边界，不放入尚未出现的业务模型：

```go
// Package domain contains provider-neutral business rules for the unified gateway.
// The transparent proxy is an infrastructure adapter and does not belong here.
package domain
```

这样五层目录从本任务起可被版本管理；统一推理领域模型留到阶段一 B，避免为了目录结构虚构领域对象。

- [ ] **步骤 3：让旧 Token Observer 实现新端口**

在 `internal/tokenusage/observer.go` 导入：

```go
appRuntime "github.com/goairix/llm-proxy/internal/application/runtime"
```

把公开类型改成别名，保持现有测试调用不变：

```go
type Usage = appRuntime.TokenUsage
type Result = appRuntime.UsageResult
type Observer = appRuntime.UsageObserver
```

删除旧的 `Usage`、`Result`、`Observer` 重复定义；`json.go`、`sse.go` 继续使用别名类型。

- [ ] **步骤 4：切换 Dashboard 与 Server 到应用契约**

把 `dashboard.Stats.AddTokenUsage` 参数改为：

```go
func (s *Stats) AddTokenUsage(provider string, usage appRuntime.TokenUsage)
```

把 `server.statsResponseWriter.observer` 改为 `appRuntime.UsageObserver`，并把 `Version` 改为：

```go
const Version = appRuntime.Version
```

此处保留兼容别名，后续移动 Server 时删除 `server.Version`。

- [ ] **步骤 5：格式化并运行测试**

```bash
gofmt -w internal/application/runtime internal/tokenusage internal/dashboard/handler.go internal/server/server.go
go test ./internal/application/runtime ./internal/domain ./internal/tokenusage ./internal/dashboard ./internal/server
```

预期：全部 PASS。

- [ ] **步骤 6：提交运行期契约**

```bash
git add internal/application internal/domain internal/tokenusage internal/dashboard/handler.go internal/server/server.go
git commit -m "refactor: add application runtime contracts"
```

### 任务 3：迁移 Config 与 Logger 叶子包

**文件：**

- 移动：`internal/config/*` → `internal/infrastructure/config/*`
- 移动：`internal/logger/*` → `internal/infrastructure/logger/*`
- 修改所有旧 Import Path

- [ ] **步骤 1：执行 Git 感知的目录移动**

```bash
mkdir -p internal/infrastructure
git mv internal/config internal/infrastructure/config
git mv internal/logger internal/infrastructure/logger
```

- [ ] **步骤 2：逐文件替换 Import Path**

使用 `apply_patch` 把：

```go
"github.com/goairix/llm-proxy/internal/config"
"github.com/goairix/llm-proxy/internal/logger"
```

替换为：

```go
"github.com/goairix/llm-proxy/internal/infrastructure/config"
"github.com/goairix/llm-proxy/internal/infrastructure/logger"
```

运行 `rg 'internal/(config|logger)' --glob '*.go'`，预期无旧路径。

- [ ] **步骤 3：运行叶子包和全量测试**

```bash
gofmt -w cmd internal
go test ./internal/infrastructure/config ./internal/infrastructure/logger
go test ./...
```

预期：全部 PASS；配置测试数量和语义不变。

- [ ] **步骤 4：提交**

```bash
git add cmd internal
git commit -m "refactor: move config and logger to infrastructure"
```

### 任务 4：迁移 Observability 包

**文件：**

- 移动：`internal/observability/*` → `internal/infrastructure/observability/*`
- 修改：`cmd/proxy/main.go`
- 修改：当前 Server 及测试的 Import Path

- [ ] **步骤 1：移动并更新 Import Path**

```bash
git mv internal/observability internal/infrastructure/observability
```

把所有 `internal/observability` Go Import 更新为 `internal/infrastructure/observability`。

保留 `runtime.go` 中已有 Meter Instrumentation Scope 字符串：

```go
meter := r.meterProvider.Meter("github.com/goairix/llm-proxy/internal/observability")
```

本阶段不能因为目录移动改变遥测身份。

- [ ] **步骤 2：运行 OTel 强制测试**

```bash
gofmt -w cmd internal
go test -race ./internal/infrastructure/observability ./internal/middleware ./internal/proxy ./internal/server
```

预期：全部 PASS，URL 清洗和 Trace Parent 测试保持通过。

- [ ] **步骤 3：提交**

```bash
git add cmd internal
git commit -m "refactor: move observability to infrastructure"
```

### 任务 5：迁移 ReverseProxy 与 Token Observer

**文件：**

- 移动：`internal/proxy/*` → `internal/infrastructure/proxy/*`
- 移动：`internal/tokenusage/*` → `internal/infrastructure/proxy/tokenusage/*`
- 修改：当前 Server、Dashboard 和测试 Import Path

- [ ] **步骤 1：移动两个包**

```bash
git mv internal/proxy internal/infrastructure/proxy
mkdir -p internal/infrastructure/proxy/tokenusage
git mv internal/tokenusage/*.go internal/infrastructure/proxy/tokenusage/
rmdir internal/tokenusage
```

- [ ] **步骤 2：更新 Import Path，保持包名**

新路径分别使用：

```go
"github.com/goairix/llm-proxy/internal/infrastructure/proxy"
"github.com/goairix/llm-proxy/internal/infrastructure/proxy/tokenusage"
```

`tokenusage` 继续实现 `application/runtime.UsageObserver`，不把解析类型移回接口层。

- [ ] **步骤 3：运行代理、流式和 Race 测试**

```bash
gofmt -w internal
go test ./internal/infrastructure/proxy/... ./internal/dashboard ./internal/server
go test -race ./internal/infrastructure/proxy/... ./internal/server
```

预期：Path/RawPath、Header、SSE 首事件和 Token 测试全部 PASS。

- [ ] **步骤 4：提交**

```bash
git add internal
git commit -m "refactor: move transparent proxy infrastructure"
```

### 任务 6：解耦并迁移 Dashboard 与 HTTP Middleware

**文件：**

- 新建后随目录移动：`internal/middleware/config.go` → `internal/interfaces/http/middleware/config.go`
- 修改后移动：`internal/dashboard` → `internal/interfaces/http/handler/dashboard`
- 修改后移动：`internal/middleware` → `internal/interfaces/http/middleware`
- 修改：当前 Server 的配置映射与 Import Path

- [ ] **步骤 1：在接口层定义专用配置类型**

先在现有包中创建 `internal/middleware/config.go`：

```go
package middleware

type RateLimitRule struct {
	RequestsPerSecond float64
	Burst             int
}

type RateLimitConfig struct {
	Enabled   bool
	Default   RateLimitRule
	Whitelist []string
	Overrides map[string]RateLimitRule
}
```

在 Dashboard Handler 内定义只读视图：

```go
type RateLimitView struct {
	Enabled           bool
	RequestsPerSecond float64
	Burst             int
}
```

把 `NewHandler` 改为接收 `RateLimitView`，删除 Dashboard 对 infrastructure/config 的依赖。

- [ ] **步骤 2：在当前 Server 增加显式映射函数**

```go
func toMiddlewareRateLimit(cfg config.RateLimitConfig) middleware.RateLimitConfig {
	overrides := make(map[string]middleware.RateLimitRule, len(cfg.Overrides))
	for key, rule := range cfg.Overrides {
		overrides[key] = middleware.RateLimitRule{RequestsPerSecond: rule.RequestsPerSecond, Burst: rule.Burst}
	}
	return middleware.RateLimitConfig{
		Enabled: cfg.Enabled,
		Default: middleware.RateLimitRule{RequestsPerSecond: cfg.Default.RequestsPerSecond, Burst: cfg.Default.Burst},
		Whitelist: append([]string(nil), cfg.Whitelist...),
		Overrides: overrides,
	}
}

func toDashboardRateLimit(cfg config.RateLimitConfig) dashboard.RateLimitView {
	return dashboard.RateLimitView{
		Enabled: cfg.Enabled,
		RequestsPerSecond: cfg.Default.RequestsPerSecond,
		Burst: cfg.Default.Burst,
	}
}
```

调整调用方，确保 Dashboard 和 Middleware 不再导入 infrastructure/config。

同步修改 `internal/middleware/ratelimit_test.go` 与 `internal/dashboard/handler_test.go`：测试数据改用各自包内的新配置类型，断言内容保持不变。这里不允许通过删减 Whitelist、Override 或 Dashboard JSON 断言来换取通过。

- [ ] **步骤 3：先运行解耦测试**

```bash
go test ./internal/dashboard ./internal/middleware ./internal/server
```

预期：PASS。

- [ ] **步骤 4：移动目录并更新 Import Path**

```bash
mkdir -p internal/interfaces/http/handler
git mv internal/dashboard internal/interfaces/http/handler/dashboard
git mv internal/middleware internal/interfaces/http/middleware
```

所有 Import 更新为：

```go
"github.com/goairix/llm-proxy/internal/interfaces/http/handler/dashboard"
"github.com/goairix/llm-proxy/internal/interfaces/http/middleware"
```

- [ ] **步骤 5：验证接口层没有基础设施依赖**

运行：

```bash
rg 'internal/infrastructure' internal/interfaces || true
go test ./internal/interfaces/http/...
go test ./...
```

预期：`rg` 无输出；全部测试 PASS。

- [ ] **步骤 6：提交**

```bash
git add internal
git commit -m "refactor: move dashboard and middleware to interfaces"
```

### 任务 7：拆分 Stats Middleware、Router 与 HTTP Server 生命周期

**文件：**

- 新建：`internal/interfaces/http/middleware/stats.go`
- 新建：`internal/interfaces/http/middleware/stats_test.go`
- 新建：`internal/interfaces/http/handler/health/handler.go`
- 新建：`internal/interfaces/http/handler/health/handler_test.go`
- 新建：`internal/interfaces/http/router/router.go`
- 新建：`internal/interfaces/http/router/router_test.go`
- 新建：`internal/infrastructure/server/http/server.go`
- 新建：`internal/infrastructure/server/http/server_test.go`
- 修改：`internal/server/server.go`（迁移期兼容门面，任务 8 删除）
- 修改：`internal/server/server_test.go`（只保留兼容门面回归）

- [ ] **步骤 1：把 Stats Writer 移入接口 Middleware**

将当前 `statsResponseWriter` 与 `statsMiddleware` 完整移动到 `middleware/stats.go`，导出以下入口：

```go
func Stats(
	provider string,
	stats *dashboard.Stats,
	observers appRuntime.UsageObserverFactory,
) func(http.Handler) http.Handler
```

请求内先声明 `var observer appRuntime.UsageObserver`，仅在 `observers != nil` 时调用工厂；其余状态码、429、错误、字节数、Token 和 Flush 逻辑逐行保持当前实现。把现有三个 Stats 测试移动到 `stats_test.go`，调用改为：

```go
handler := Stats("openai", stats, tokenusage.NewObserver)(next)
```

- [ ] **步骤 2：实现健康检查适配器**

`health/handler.go`：

```go
package health

import (
	"encoding/json"
	"net/http"

	appRuntime "github.com/goairix/llm-proxy/internal/application/runtime"
	"go.uber.org/zap"
)

type Handler struct {
	readiness *appRuntime.Readiness
	logger    *zap.Logger
}

func New(readiness *appRuntime.Readiness, logger *zap.Logger) *Handler {
	return &Handler{readiness: readiness, logger: logger}
}
func (h *Handler) Health(w http.ResponseWriter, _ *http.Request) { h.writeStatus(w, http.StatusOK, "ok") }
func (h *Handler) Ready(w http.ResponseWriter, _ *http.Request) {
	if !h.readiness.Ready() { h.writeStatus(w, http.StatusServiceUnavailable, "not_ready"); return }
	h.writeStatus(w, http.StatusOK, "ready")
}
func (h *Handler) writeStatus(w http.ResponseWriter, code int, status string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(struct{ Status string `json:"status"` }{Status: status}); err != nil {
		h.logger.Error("failed to encode health response", zap.Error(err))
	}
}
```

测试 GET 的 200/503 和状态 JSON；方法限制由 Router 测试覆盖。

- [ ] **步骤 3：实现只负责 HTTP 组装的 Router**

`router/router.go` 的公开契约：

```go
type Instrumenter interface {
	WrapHandler(provider string, next http.Handler) http.Handler
}

type Config struct {
	BaseURL   string
	Version   string
	RateLimit middleware.RateLimitConfig
	RateView  dashboard.RateLimitView
}

type Dependencies struct {
	Logger          *zap.Logger
	Instrumenter    Instrumenter
	Readiness       *appRuntime.Readiness
	Stats           *dashboard.Stats
	ObserverFactory appRuntime.UsageObserverFactory
	OpenAIProxy     http.Handler
	AnthropicProxy  http.Handler
}
```

`New` 必须按以下顺序注册和包裹：

```go
func New(cfg Config, deps Dependencies) http.Handler {
	mux := http.NewServeMux()
	healthHandler := health.New(deps.Readiness, deps.Logger)
	mux.HandleFunc("GET /healthz", healthHandler.Health)
	mux.HandleFunc("/healthz", methodNotAllowed(http.MethodGet))
	mux.HandleFunc("GET /readyz", healthHandler.Ready)
	mux.HandleFunc("/readyz", methodNotAllowed(http.MethodGet))
	mux.Handle("/", dashboard.NewHandler(deps.Stats, cfg.RateView, cfg.Version, cfg.BaseURL))

	limiter := middleware.NewRateLimiter(cfg.RateLimit)
	logging := middleware.Logging(deps.Logger)
	openAI := logging(middleware.Stats("openai", deps.Stats, deps.ObserverFactory)(limiter.Handler("openai", deps.OpenAIProxy)))
	anthropic := logging(middleware.Stats("anthropic", deps.Stats, deps.ObserverFactory)(limiter.Handler("anthropic", deps.AnthropicProxy)))
	mux.Handle("/openai/", deps.Instrumenter.WrapHandler("openai", openAI))
	mux.Handle("/anthropic/", deps.Instrumenter.WrapHandler("anthropic", anthropic))
	return mux
}
```

保留 `methodNotAllowed` 的 `Allow` Header 和 405 文本。

- [ ] **步骤 4：实现纯 Server 生命周期**

`infrastructure/server/http/server.go`：

```go
type Server struct {
	httpServer *http.Server
	logger     *zap.Logger
	readiness  *appRuntime.Readiness
}

func New(addr string, handler http.Handler, logger *zap.Logger, readiness *appRuntime.Readiness) *Server {
	return &Server{httpServer: &http.Server{Addr: addr, Handler: handler}, logger: logger, readiness: readiness}
}

func (s *Server) Start() error {
	s.readiness.SetReady(true)
	defer s.readiness.SetReady(false)
	s.logger.Info("server starting", zap.String("addr", s.httpServer.Addr))
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.readiness.SetReady(false)
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	s.logger.Info("server shutting down")
	return s.httpServer.Shutdown(shutdownCtx)
}
```

测试 `Shutdown` 会先把 Readiness 置 false；Router 测试复用任务 1 的双供应商特征用例，并验证 Dashboard 未统计健康检查。

- [ ] **步骤 5：保留可编译的迁移期 Server 兼容门面**

本任务结束时 `cmd/proxy/main.go` 仍调用 `server.New`，因此不能直接删除旧包。把 `internal/server/server.go` 改为兼容门面：保留 `Version`、`New`、`Start`、`Shutdown` 这些旧入口，在 `New` 内完成临时映射并委托给新 Router 与 `infrastructure/server/http.Server`。兼容门面不得复制代理、中间件或生命周期实现；任务 8 切换 Wire 后再删除。

兼容门面可用组合方式持有 `*httpserver.Server` 并转发 `Start`、`Shutdown`，不把新 Server 的内部 `http.Server` 字段导出。将任务 1 的双供应商特征用例迁入 `router/router_test.go`；`internal/server/server_test.go` 只保留一条 `New` 返回非空且无错误的兼容门面冒烟回归，证明旧 `main` 使用的构造路径仍可用。

运行：

```bash
gofmt -w internal
go test ./internal/application/runtime ./internal/interfaces/http/... ./internal/infrastructure/server/http ./internal/infrastructure/proxy/... ./internal/server
go test -race ./internal/interfaces/http/... ./internal/infrastructure/server/http ./internal/infrastructure/proxy/... ./internal/server
go build -o /tmp/llm-proxy ./cmd/proxy
```

预期：全部 PASS；任务 1 的特征用例已迁入 Router 测试且继续通过；旧 Main 仍可构建。

- [ ] **步骤 6：提交**

```bash
git add internal
git commit -m "refactor: split HTTP router and server lifecycle"
```

### 任务 8：引入 Google Wire 并切换 Main 组装根

**文件：**

- 新建：`internal/di/app.go`
- 新建：`internal/di/provider/shared.go`
- 新建：`internal/di/provider/transparent.go`
- 新建：`internal/di/modules/app.go`
- 新建：`internal/di/wire.go`
- 生成：`internal/di/wire_gen.go`
- 修改：`cmd/proxy/main.go`
- 修改：`go.mod`、`go.sum`
- 删除：`internal/server/server.go`
- 删除：`internal/server/server_test.go`

- [ ] **步骤 1：增加 Wire 依赖**

```bash
go get github.com/google/wire@v0.7.0
```

预期：`go.mod` 增加 Wire；没有无关依赖升级。

- [ ] **步骤 2：定义 App 与类型化 Handler Wrapper**

`internal/di/app.go`：

```go
package di

import (
	"github.com/goairix/llm-proxy/internal/infrastructure/observability"
	httpserver "github.com/goairix/llm-proxy/internal/infrastructure/server/http"
	"go.uber.org/zap"
)

type App struct {
	Server    *httpserver.Server
	Telemetry *observability.Runtime
	Logger    *zap.Logger
}
```

在 `provider/transparent.go` 定义三种强类型包装，避免 Wire 对多个 `http.Handler` 产生歧义：

```go
type OpenAIHandler struct{ http.Handler }
type AnthropicHandler struct{ http.Handler }
type RootHandler struct{ http.Handler }
```

- [ ] **步骤 3：实现 Provider 函数**

Provider 必须完成以下显式映射：

```go
func NewLogger(cfg *config.Config) (*zap.Logger, error) { return logger.New(cfg.Log) }
func NewTelemetry(ctx context.Context, cfg *config.Config, log *zap.Logger) (*observability.Runtime, error) {
	return observability.New(ctx, cfg.Observability, appRuntime.Version, log)
}
func NewReadiness() *appRuntime.Readiness { return appRuntime.NewReadiness() }
func NewStats() *dashboard.Stats { return &dashboard.Stats{} }
func NewObserverFactory() appRuntime.UsageObserverFactory { return tokenusage.NewObserver }
```

代理 Provider 使用 `telemetry.Transport(http.DefaultTransport)` 创建两个 ReverseProxy。Router Provider 把 infrastructure/config 映射为任务 6 的两个接口配置类型，计算空 `show_base_url` 的默认值，并调用 `router.New`。Server Provider 使用 `fmt.Sprintf(":%d", cfg.Server.Port)`。

三类 Handler 和 Server Provider 的签名固定为：

```go
func NewOpenAIHandler(cfg *config.Config, telemetry *observability.Runtime) (OpenAIHandler, error)
func NewAnthropicHandler(cfg *config.Config, telemetry *observability.Runtime) (AnthropicHandler, error)
func NewRootHandler(
	cfg *config.Config,
	log *zap.Logger,
	telemetry *observability.Runtime,
	readiness *appRuntime.Readiness,
	stats *dashboard.Stats,
	observers appRuntime.UsageObserverFactory,
	openAI OpenAIHandler,
	anthropic AnthropicHandler,
) RootHandler
func NewHTTPServer(
	cfg *config.Config,
	root RootHandler,
	log *zap.Logger,
	readiness *appRuntime.Readiness,
) *httpserver.Server
```

Provider 内发生错误时用 `%w` 分别补充 `create openai proxy`、`create anthropic proxy` 上下文；不得记录 Base URL 或凭据。

- [ ] **步骤 4：定义 Wire Set 与 Injector**

`modules/app.go`：

```go
package modules

import (
	"github.com/goairix/llm-proxy/internal/di/provider"
	"github.com/google/wire"
)

var AppSet = wire.NewSet(
	provider.NewLogger,
	provider.NewTelemetry,
	provider.NewReadiness,
	provider.NewStats,
	provider.NewObserverFactory,
	provider.NewOpenAIHandler,
	provider.NewAnthropicHandler,
	provider.NewRootHandler,
	provider.NewHTTPServer,
)
```

`wire.go`：

```go
//go:build wireinject

package di

import (
	"context"

	"github.com/goairix/llm-proxy/internal/di/modules"
	"github.com/goairix/llm-proxy/internal/infrastructure/config"
	"github.com/google/wire"
)

func Initialize(ctx context.Context, cfg *config.Config) (*App, error) {
	wire.Build(modules.AppSet, wire.Struct(new(App), "*"))
	return nil, nil
}
```

- [ ] **步骤 5：生成 Wire 代码并验证生成物**

```bash
go run github.com/google/wire/cmd/wire@v0.7.0 ./internal/di
gofmt -w internal/di
go test ./internal/di
```

预期：生成 `wire_gen.go`，无重复 Provider 或 `http.Handler` 歧义。

- [ ] **步骤 6：改写 Main，只保留生命周期编排**

`cmd/proxy/main.go` 保留配置加载、Signal 和 30 秒外层 Deadline；用：

```go
app, err := di.Initialize(context.Background(), cfg)
if err != nil { log.Fatalf("failed to initialize application: %v", err) }
defer app.Logger.Sync() //nolint:errcheck
```

替换手动 Logger、Telemetry、Server 构造。启动使用 `app.Server.Start()`；关闭顺序严格保持：

```go
if err := app.Server.Shutdown(ctx); err != nil { app.Logger.Error("shutdown error", zap.Error(err)) }
if err := app.Telemetry.Shutdown(ctx); err != nil { app.Logger.Error("telemetry shutdown error", zap.Error(err)) }
```

Main 完成切换并通过编译后，删除任务 7 的 `internal/server` 兼容门面及其冒烟测试；此时所有生产组装必须只经过 `internal/di`。

- [ ] **步骤 7：运行完整验证并提交**

```bash
go test ./...
go test -race ./...
go build -o /tmp/llm-proxy ./cmd/proxy
git diff --check
git add cmd internal go.mod go.sum
git commit -m "build: add Wire application composition"
```

预期：全部通过；二进制启动依赖图由 Wire 生成。

### 任务 9：增加架构依赖测试并同步文档

**文件：**

- 新建：`internal/architecture/dependencies_test.go`
- 修改：`AGENTS.md`
- 修改：`README.md`

- [ ] **步骤 1：写一个先失败的架构测试**

测试使用 `go/parser` 遍历 `internal` Go 文件并执行规则：

```go
var forbidden = map[string][]string{
	"domain":         {"/internal/application/", "/internal/interfaces/", "/internal/infrastructure/", "/internal/di/"},
	"application":    {"/internal/interfaces/", "/internal/infrastructure/", "/internal/di/"},
	"interfaces":     {"/internal/infrastructure/", "/internal/di/"},
	"infrastructure": {"/internal/interfaces/", "/internal/di/"},
}
```

遍历时忽略 `_test.go`，因为黑盒/集成测试可以跨层。对每个 Import 去引号后，若包含禁止片段则 `t.Errorf("%s imports forbidden package %s", path, imported)`。

先临时在 `internal/application/runtime/readiness.go` 增加以下 infrastructure Import：

```go
import (
	"sync/atomic"

	_ "github.com/goairix/llm-proxy/internal/infrastructure/config"
)
```

运行：

```bash
go test ./internal/architecture -run TestLayerDependencies -v
```

预期：FAIL 并准确指出文件和 Import；随后立即删除临时 Import。

- [ ] **步骤 2：运行架构测试确认通过**

```bash
go test ./internal/architecture -v
```

预期：PASS。

- [ ] **步骤 3：同步中文项目结构文档**

更新 `AGENTS.md` 和 `README.md` 的目录职责，明确：

```text
interfaces → application → domain
infrastructure 实现内层端口
di 是唯一组装根
透明代理位于 infrastructure/proxy，不经过统一推理域
```

删除所有已经不存在的旧路径说明；保留现有路由与中间件顺序描述。

- [ ] **步骤 4：执行最终门禁**

```bash
go test ./...
go test -race ./...
go build -o /tmp/llm-proxy ./cmd/proxy
git diff --check
git status --short
```

预期：全部通过；`git status` 只显示本任务待提交文件。

- [ ] **步骤 5：提交文档与架构门禁**

```bash
git add internal/architecture AGENTS.md README.md
git commit -m "test: enforce DDD dependency boundaries"
```

## 三、完成定义

阶段一 A 完成时必须满足：

- 现有功能、路径、中间件、流式、Token、OTel 和关闭语义全部通过原测试与新增特征测试；
- 旧的 `internal/config`、`logger`、`observability`、`proxy`、`tokenusage`、`dashboard`、`middleware`、`server` 目录均已消失；
- `cmd/proxy/main.go` 只负责配置入口、Signal 和生命周期顺序；
- `internal/di/wire_gen.go` 可重复生成且无差异；
- 架构测试禁止反向依赖；
- 没有新增 `/v1` 路由、供应商或协议转换代码；
- `go test ./...`、`go test -race ./...`、Build 和 `git diff --check` 全部通过。

## 四、计划自审记录

- **范围一致：** 只实施设计中的阶段一 A；平台凭据池、数据库加密凭据和租户作用域仍由后续阶段实现，本计划不提前建立错误的数据模型。
- **迁移可恢复：** 任务 1～7 每次提交后旧 Main 都能构建；任务 7 使用临时兼容门面，直到任务 8 的 Wire 组装根接管后才删除旧 Server。
- **行为受保护：** 路由、RawPath、Header、限流优先级、SSE Flush、Token、Dashboard、健康检查、OTel 与关闭顺序都有原测试或新增特征测试覆盖。
- **依赖方向明确：** `domain`、`application`、`interfaces`、`infrastructure`、`di` 的允许依赖由任务 9 自动检查，`di` 是唯一可以同时引用接口层与基础设施层的组装根。
- **类型可装配：** 多个 `http.Handler` 使用强类型包装消除 Wire 歧义；配置通过显式映射进入接口层，不把 Viper 配置结构向内扩散。
- **无悬空步骤：** 所有新增文件都有调用方或测试；阶段一 B/C 的统一协议、供应商路由和凭据功能未作为占位代码混入本次迁移。
