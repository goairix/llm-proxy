# Environment Configuration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让全部运行配置可被 `LLM_PROXY_` 前缀的进程环境变量或 `.env` 文件可预期地覆盖。

**Architecture:** `config.Load` 先用 `godotenv` 加载当前目录的 `.env`，再将已知标量字段显式绑定到 Viper。数字和布尔环境变量在 Unmarshal 前显式验证，`whitelist` 和 `overrides` 在 Unmarshal 后按 JSON 解析，从而获得明确的变量名错误信息。

**Tech Stack:** Go 1.25、Viper 1.21、godotenv 1.5.1、`encoding/json`、`testing`

---

## 执行修订

实际执行时为严格遵循 TDD，将 Task 3 Step 1 的 `.env` 加载、进程变量优先级和 malformed `.env` 测试与 Task 1 Step 2 一起写入，并在修改生产代码前统一验证为 RED。Task 1 的最小实现随后同时使标量环境变量和 `.env` 行为转绿；Task 3 仅作为对应行为的回归检查记录。

## 文件结构

- 新建 `internal/config/env.go`：集中管理环境变量常量、Viper 绑定、类型验证、`.env` 加载和 JSON 字段覆盖。
- 修改 `internal/config/config.go`：调用 `env.go` 的加载阶段，并为 `RateLimitRule` 增加 JSON tag。
- 修改 `internal/config/config_test.go`：隔离进程环境与工作目录，覆盖标量、JSON、优先级和错误语义。
- 新建 `.env.example`：提供全部变量的非敏感示例。
- 修改 `README.md`、`config.yaml`、`AGENTS.md`：记录命名、优先级、复杂字段格式和维护约束。
- 修改 `go.mod`、`go.sum`：通过 `go mod tidy` 将 `godotenv` 确认为直接依赖。
- 保留 `Dockerfile` 的用户改动，不在本计划的任何提交中暂存它。

### Task 1: 标量环境变量覆盖

**Files:**
- Create: `internal/config/env.go`
- Modify: `internal/config/config.go:3-95`
- Test: `internal/config/config_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

- [x] **Step 1: 为测试增加可重复的环境隔离 helper**

在 `internal/config/config_test.go` 中增加 `path/filepath`，并添加：

```go
var configEnvNames = []string{
	"LLM_PROXY_SERVER_PORT",
	"LLM_PROXY_SERVER_SHOW_BASE_URL",
	"LLM_PROXY_LOG_LEVEL",
	"LLM_PROXY_LOG_FILE",
	"LLM_PROXY_LOG_MAX_AGE",
	"LLM_PROXY_RATE_LIMIT_ENABLED",
	"LLM_PROXY_RATE_LIMIT_DEFAULT_REQUESTS_PER_SECOND",
	"LLM_PROXY_RATE_LIMIT_DEFAULT_BURST",
	"LLM_PROXY_RATE_LIMIT_WHITELIST",
	"LLM_PROXY_RATE_LIMIT_OVERRIDES",
	"LLM_PROXY_PROVIDERS_OPENAI_BASE_URL",
	"LLM_PROXY_PROVIDERS_ANTHROPIC_BASE_URL",
}

func isolateConfigEnvironment(t *testing.T) string {
	t.Helper()

	workDir := t.TempDir()
	t.Chdir(workDir)

	for _, name := range configEnvNames {
		value, exists := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
		t.Cleanup(func() {
			if exists {
				_ = os.Setenv(name, value)
				return
			}
			_ = os.Unsetenv(name)
		})
	}

	return workDir
}

func writeConfigFile(t *testing.T, dir, contents string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
```

在现有 `TestLoad` 的每个 `t.Run` 开头调用 `isolateConfigEnvironment(t)`，避免本机 `.env` 或 CI 变量影响旧测试。

- [x] **Step 2: 编写标量覆盖与非法值测试**

```go
func TestLoadScalarEnvironmentOverrides(t *testing.T) {
	workDir := isolateConfigEnvironment(t)
	configPath := writeConfigFile(t, workDir, `
server:
  port: 7070
log:
  level: info
rate_limit:
  enabled: true
providers:
  openai:
    base_url: https://yaml.openai.example
`)

	t.Setenv("LLM_PROXY_SERVER_PORT", "9090")
	t.Setenv("LLM_PROXY_SERVER_SHOW_BASE_URL", "https://proxy.example")
	t.Setenv("LLM_PROXY_LOG_LEVEL", "debug")
	t.Setenv("LLM_PROXY_LOG_FILE", "/tmp/env-proxy.log")
	t.Setenv("LLM_PROXY_LOG_MAX_AGE", "14")
	t.Setenv("LLM_PROXY_RATE_LIMIT_ENABLED", "false")
	t.Setenv("LLM_PROXY_RATE_LIMIT_DEFAULT_REQUESTS_PER_SECOND", "12.5")
	t.Setenv("LLM_PROXY_RATE_LIMIT_DEFAULT_BURST", "25")
	t.Setenv("LLM_PROXY_PROVIDERS_OPENAI_BASE_URL", "https://env.openai.example")
	t.Setenv("LLM_PROXY_PROVIDERS_ANTHROPIC_BASE_URL", "https://env.anthropic.example")

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.Port != 9090 || cfg.Server.ShowBaseURL != "https://proxy.example" {
		t.Fatalf("server config = %+v", cfg.Server)
	}
	if cfg.Log.Level != "debug" || cfg.Log.File != "/tmp/env-proxy.log" || cfg.Log.MaxAge != 14 {
		t.Fatalf("log config = %+v", cfg.Log)
	}
	if cfg.RateLimit.Enabled || cfg.RateLimit.Default.RequestsPerSecond != 12.5 || cfg.RateLimit.Default.Burst != 25 {
		t.Fatalf("rate limit config = %+v", cfg.RateLimit)
	}
	if cfg.Providers.OpenAI.BaseURL != "https://env.openai.example" || cfg.Providers.Anthropic.BaseURL != "https://env.anthropic.example" {
		t.Fatalf("providers config = %+v", cfg.Providers)
	}
}

func TestLoadInvalidScalarEnvironment(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "LLM_PROXY_SERVER_PORT", value: "not-an-int"},
		{name: "LLM_PROXY_RATE_LIMIT_ENABLED", value: "not-a-bool"},
		{name: "LLM_PROXY_RATE_LIMIT_DEFAULT_REQUESTS_PER_SECOND", value: "not-a-float"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workDir := isolateConfigEnvironment(t)
			t.Setenv(tc.name, tc.value)
			_, err := Load(filepath.Join(workDir, "missing.yaml"))
			if err == nil || !strings.Contains(err.Error(), tc.name) {
				t.Fatalf("Load() error = %v, want error containing %s", err, tc.name)
			}
		})
	}
}
```

将 `strings` 加入测试 import。

- [x] **Step 3: 运行测试确认红色状态**

Run: `go test ./internal/config/ -run 'TestLoadScalarEnvironmentOverrides|TestLoadInvalidScalarEnvironment' -count=1`

Expected: FAIL，标量值仍来自 YAML/默认值，且非法环境变量尚未返回带变量名的错误。

- [x] **Step 4: 实现环境变量绑定和标量验证**

新建 `internal/config/env.go`：

```go
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

const (
	envServerPort                 = "LLM_PROXY_SERVER_PORT"
	envServerShowBaseURL          = "LLM_PROXY_SERVER_SHOW_BASE_URL"
	envLogLevel                   = "LLM_PROXY_LOG_LEVEL"
	envLogFile                    = "LLM_PROXY_LOG_FILE"
	envLogMaxAge                  = "LLM_PROXY_LOG_MAX_AGE"
	envRateLimitEnabled           = "LLM_PROXY_RATE_LIMIT_ENABLED"
	envRateLimitRequestsPerSecond = "LLM_PROXY_RATE_LIMIT_DEFAULT_REQUESTS_PER_SECOND"
	envRateLimitBurst             = "LLM_PROXY_RATE_LIMIT_DEFAULT_BURST"
	envRateLimitWhitelist         = "LLM_PROXY_RATE_LIMIT_WHITELIST"
	envRateLimitOverrides         = "LLM_PROXY_RATE_LIMIT_OVERRIDES"
	envOpenAIBaseURL              = "LLM_PROXY_PROVIDERS_OPENAI_BASE_URL"
	envAnthropicBaseURL           = "LLM_PROXY_PROVIDERS_ANTHROPIC_BASE_URL"
)

type envBinding struct {
	key      string
	name     string
	validate func(string) error
}

var envBindings = []envBinding{
	{key: "server.port", name: envServerPort, validate: validateInt},
	{key: "server.show_base_url", name: envServerShowBaseURL},
	{key: "log.level", name: envLogLevel},
	{key: "log.file", name: envLogFile},
	{key: "log.max_age", name: envLogMaxAge, validate: validateInt},
	{key: "rate_limit.enabled", name: envRateLimitEnabled, validate: validateBool},
	{key: "rate_limit.default.requests_per_second", name: envRateLimitRequestsPerSecond, validate: validateFloat},
	{key: "rate_limit.default.burst", name: envRateLimitBurst, validate: validateInt},
	{key: "providers.openai.base_url", name: envOpenAIBaseURL},
	{key: "providers.anthropic.base_url", name: envAnthropicBaseURL},
}

func loadDotEnv() error {
	err := godotenv.Load()
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("load .env: %w", err)
}

func bindEnvironment(v *viper.Viper) error {
	for _, binding := range envBindings {
		if value, ok := os.LookupEnv(binding.name); ok && binding.validate != nil {
			if err := binding.validate(value); err != nil {
				return fmt.Errorf("%s: %w", binding.name, err)
			}
		}
		if err := v.BindEnv(binding.key, binding.name); err != nil {
			return fmt.Errorf("bind %s: %w", binding.name, err)
		}
	}
	return nil
}

func validateInt(value string) error {
	_, err := strconv.Atoi(value)
	return err
}

func validateBool(value string) error {
	_, err := strconv.ParseBool(value)
	return err
}

func validateFloat(value string) error {
	_, err := strconv.ParseFloat(value, 64)
	return err
}

func applyComplexEnvironment(cfg *Config) error {
	if value, ok := os.LookupEnv(envRateLimitWhitelist); ok {
		if err := json.Unmarshal([]byte(value), &cfg.RateLimit.Whitelist); err != nil {
			return fmt.Errorf("%s: %w", envRateLimitWhitelist, err)
		}
	}
	if value, ok := os.LookupEnv(envRateLimitOverrides); ok {
		if err := json.Unmarshal([]byte(value), &cfg.RateLimit.Overrides); err != nil {
			return fmt.Errorf("%s: %w", envRateLimitOverrides, err)
		}
	}
	return nil
}
```

`applyComplexEnvironment` 在 Task 2 才接入 `Load`；先保留完整函数，避免下一任务重复搭骨架。

修改 `config.Load`，删除 `config.go` 中的 `godotenv` import，并将函数改为：

```go
func Load(path string) (*Config, error) {
	if err := loadDotEnv(); err != nil {
		return nil, err
	}

	v := viper.New()

	v.SetDefault("server.port", 8080)
	v.SetDefault("server.show_base_url", "")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.max_age", 30)
	v.SetDefault("rate_limit.enabled", true)
	v.SetDefault("rate_limit.default.requests_per_second", 10)
	v.SetDefault("rate_limit.default.burst", 20)
	v.SetDefault("providers.openai.base_url", "https://api.openai.com")
	v.SetDefault("providers.anthropic.base_url", "https://api.anthropic.com")

	if err := bindEnvironment(v); err != nil {
		return nil, err
	}

	v.SetConfigFile(path)
	v.SetConfigType("yaml")

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok && !isNotFoundError(err) {
			return nil, err
		}
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}
```

- [x] **Step 5: 整理依赖并验证标量测试转绿**

Run: `gofmt -w internal/config/config.go internal/config/env.go internal/config/config_test.go && go mod tidy`

Run: `go test ./internal/config/ -run 'TestLoadScalarEnvironmentOverrides|TestLoadInvalidScalarEnvironment|TestLoad$' -count=1`

Expected: PASS。`go.mod` 中 `github.com/joho/godotenv v1.5.1` 位于直接 `require` 组。

- [x] **Step 6: 提交标量支持**

```bash
git add internal/config/config.go internal/config/env.go internal/config/config_test.go go.mod go.sum
git commit -m "feat: support scalar environment configuration"
```

### Task 2: JSON 复杂配置

**Files:**
- Modify: `internal/config/config.go:37-41,89-95`
- Test: `internal/config/config_test.go`

- [x] **Step 1: 编写 JSON 覆盖与错误测试**

```go
func TestLoadComplexEnvironmentOverrides(t *testing.T) {
	workDir := isolateConfigEnvironment(t)
	t.Setenv("LLM_PROXY_RATE_LIMIT_WHITELIST", `["sk-env-a","sk-env-b"]`)
	t.Setenv("LLM_PROXY_RATE_LIMIT_OVERRIDES", `{"sk-env":{"requests_per_second":42.5,"burst":84}}`)

	cfg, err := Load(filepath.Join(workDir, "missing.yaml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(cfg.RateLimit.Whitelist, []string{"sk-env-a", "sk-env-b"}) {
		t.Fatalf("Whitelist = %#v", cfg.RateLimit.Whitelist)
	}
	want := RateLimitRule{RequestsPerSecond: 42.5, Burst: 84}
	if got := cfg.RateLimit.Overrides["sk-env"]; got != want {
		t.Fatalf("override = %+v, want %+v", got, want)
	}
}

func TestLoadInvalidComplexEnvironment(t *testing.T) {
	for _, name := range []string{
		"LLM_PROXY_RATE_LIMIT_WHITELIST",
		"LLM_PROXY_RATE_LIMIT_OVERRIDES",
	} {
		t.Run(name, func(t *testing.T) {
			workDir := isolateConfigEnvironment(t)
			t.Setenv(name, "not-json")
			_, err := Load(filepath.Join(workDir, "missing.yaml"))
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("Load() error = %v, want error containing %s", err, name)
			}
		})
	}
}
```

将 `reflect` 加入测试 import。

- [x] **Step 2: 运行测试确认红色状态**

Run: `go test ./internal/config/ -run 'TestLoadComplexEnvironmentOverrides|TestLoadInvalidComplexEnvironment' -count=1`

Expected: FAIL，因为 `Load` 还没有调用 `applyComplexEnvironment`。

- [x] **Step 3: 接入 JSON 覆盖**

为 `RateLimitRule` 增加 JSON tag：

```go
type RateLimitRule struct {
	RequestsPerSecond float64 `mapstructure:"requests_per_second" json:"requests_per_second"`
	Burst             int     `mapstructure:"burst" json:"burst"`
}
```

在 `config.Load` 的 `v.Unmarshal(cfg)` 成功后调用：

```go
if err := applyComplexEnvironment(cfg); err != nil {
	return nil, err
}
```

- [x] **Step 4: 运行配置包全量测试**

Run: `gofmt -w internal/config/config.go internal/config/config_test.go && go test ./internal/config/ -count=1`

Expected: PASS。

- [x] **Step 5: 提交 JSON 支持**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: support complex environment configuration"
```

### Task 3: `.env` 加载与优先级

**Files:**
- Modify: `internal/config/env.go`
- Test: `internal/config/config_test.go`

- [x] **Step 1: 编写 `.env` 值、优先级和解析错误测试**

```go
func TestLoadDotEnv(t *testing.T) {
	workDir := isolateConfigEnvironment(t)
	dotEnv := "LLM_PROXY_SERVER_PORT=7070\nLLM_PROXY_LOG_LEVEL=warn\n"
	if err := os.WriteFile(filepath.Join(workDir, ".env"), []byte(dotEnv), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	cfg, err := Load(filepath.Join(workDir, "missing.yaml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.Port != 7070 || cfg.Log.Level != "warn" {
		t.Fatalf("config from .env = %+v", cfg)
	}
}

func TestLoadProcessEnvironmentOverridesDotEnv(t *testing.T) {
	workDir := isolateConfigEnvironment(t)
	if err := os.WriteFile(filepath.Join(workDir, ".env"), []byte("LLM_PROXY_SERVER_PORT=7070\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Setenv("LLM_PROXY_SERVER_PORT", "9090")

	cfg, err := Load(filepath.Join(workDir, "missing.yaml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.Port != 9090 {
		t.Fatalf("Server.Port = %d, want 9090", cfg.Server.Port)
	}
}

func TestLoadMalformedDotEnv(t *testing.T) {
	workDir := isolateConfigEnvironment(t)
	if err := os.WriteFile(filepath.Join(workDir, ".env"), []byte("BROKEN=\"unterminated\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	_, err := Load(filepath.Join(workDir, "missing.yaml"))
	if err == nil || !strings.Contains(err.Error(), "load .env") {
		t.Fatalf("Load() error = %v, want .env parse error", err)
	}
}
```

- [x] **Step 2: 运行优先级测试**

Run: `go test ./internal/config/ -run 'TestLoadDotEnv|TestLoadProcessEnvironmentOverridesDotEnv|TestLoadMalformedDotEnv' -count=1`

Expected: PASS。`BROKEN="unterminated` 会命中 godotenv 1.5.1 的 `unterminated quoted value` 解析错误。

- [x] **Step 3: 运行配置包回归测试**

Run: `go test ./internal/config/ -count=1`

Expected: PASS，包括原有 YAML、缺失文件和默认值用例。

- [x] **Step 4: 提交 `.env` 优先级测试**

```bash
git add internal/config/env.go internal/config/config_test.go
git commit -m "test: cover dotenv configuration precedence"
```

### Task 4: 示例和维护文档

**Files:**
- Create: `.env.example`
- Modify: `README.md:39-103`
- Modify: `config.yaml:1-8`
- Modify: `AGENTS.md:12,32,59-67,99-109`

- [x] **Step 1: 新增完整 `.env.example`**

```dotenv
LLM_PROXY_SERVER_PORT=8080
LLM_PROXY_SERVER_SHOW_BASE_URL=

LLM_PROXY_LOG_LEVEL=info
LLM_PROXY_LOG_FILE=./logs/proxy.log
LLM_PROXY_LOG_MAX_AGE=30

LLM_PROXY_RATE_LIMIT_ENABLED=true
LLM_PROXY_RATE_LIMIT_DEFAULT_REQUESTS_PER_SECOND=10
LLM_PROXY_RATE_LIMIT_DEFAULT_BURST=20
LLM_PROXY_RATE_LIMIT_WHITELIST=["sk-example-whitelist"]
LLM_PROXY_RATE_LIMIT_OVERRIDES={"sk-example-override":{"requests_per_second":100,"burst":200}}

LLM_PROXY_PROVIDERS_OPENAI_BASE_URL=https://api.openai.com
LLM_PROXY_PROVIDERS_ANTHROPIC_BASE_URL=https://api.anthropic.com
```

- [x] **Step 2: 更新用户配置文档**

在 README 的配置章节增加：

```markdown
### 环境变量

服务会自动加载当前目录的 `.env`。配置优先级为：进程环境变量 > `.env` > `config.yaml` > 默认值。

```bash
cp .env.example .env
```

变量名使用 `LLM_PROXY_` 前缀，并将配置层级转为大写下划线，例如 `server.port` 对应 `LLM_PROXY_SERVER_PORT`。完整列表见 `.env.example`。

`LLM_PROXY_RATE_LIMIT_WHITELIST` 必须是 JSON 字符串数组，`LLM_PROXY_RATE_LIMIT_OVERRIDES` 必须是 JSON 对象。无效的数字、布尔值或 JSON 会导致启动失败。
```

在 `config.yaml` 顶部的加载说明中写明优先级和 `.env.example`。

- [x] **Step 3: 更新代理工作约束**

在 `AGENTS.md` 中将配置栈更新为 `Viper + config.yaml + godotenv`，并在“配置与生命周期”增加：

```markdown
- 配置优先级为进程环境变量 > `.env` > `config.yaml` > 默认值；环境变量使用 `LLM_PROXY_` 前缀。
- 新增配置字段时还要同步 `internal/config/env.go` 和 `.env.example`；列表/映射环境变量使用 JSON。
```

- [x] **Step 4: 检查示例可跟踪且无真实凭据**

Run: `git check-ignore .env.example; test $? -eq 1`

Expected: `.env.example` 未被忽略。

Run: `rg -n 'sk-(live|prod)|api[_-]?key\s*=\s*[^<]' .env.example README.md config.yaml AGENTS.md`

Expected: 无真实凭据命中；所有 Key 均显式使用 `example`/`test` 语义。

- [x] **Step 5: 提交示例和文档**

```bash
git add .env.example README.md config.yaml AGENTS.md
git commit -m "docs: document environment configuration"
```

### Task 5: 全量验证

**Files:**
- Verify only; no planned source changes

- [x] **Step 1: 检查格式和差异**

Run: `gofmt -w internal/config/config.go internal/config/env.go internal/config/config_test.go`

Run: `git diff --check`

Expected: 无输出，退出码 0。

- [x] **Step 2: 运行配置包和全量测试**

Run: `go test ./internal/config/ -count=1`

Run: `go test ./... -count=1`

Expected: 全部 PASS。

- [x] **Step 3: 运行竞态检测和构建**

Run: `go test -race ./... -count=1`

Run: `go build -o /tmp/llm-proxy ./cmd/proxy`

Expected: 全部退出码 0。

- [x] **Step 4: 核对工作区和提交范围**

Run: `git status --short && git log --oneline -6`

Expected: 功能文件均已提交；`Dockerfile` 仍保持为用户的未提交修改，未出现二进制、`.env` 或日志文件。
