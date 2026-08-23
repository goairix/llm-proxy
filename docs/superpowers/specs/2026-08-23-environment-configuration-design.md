# 环境变量配置支持设计

**日期：** 2026-08-23
**状态：** 已确认

## 目标

在保留现有 `config.yaml` 和默认值的前提下，允许通过进程环境变量或当前目录的 `.env` 文件覆盖全部运行配置。环境变量统一使用 `LLM_PROXY_` 前缀。

## 配置优先级

从高到低为：

1. 进程环境变量
2. 当前工作目录中的 `.env`
3. `config.yaml`
4. Viper 默认值

`godotenv.Load()` 不覆盖已存在的进程环境变量，因此可自然保证前两级的优先关系。`.env` 不存在时继续启动；文件存在但无法解析时返回带上下文的错误。

## 环境变量映射

| 配置项 | 环境变量 |
| --- | --- |
| `server.port` | `LLM_PROXY_SERVER_PORT` |
| `server.show_base_url` | `LLM_PROXY_SERVER_SHOW_BASE_URL` |
| `log.level` | `LLM_PROXY_LOG_LEVEL` |
| `log.file` | `LLM_PROXY_LOG_FILE` |
| `log.max_age` | `LLM_PROXY_LOG_MAX_AGE` |
| `rate_limit.enabled` | `LLM_PROXY_RATE_LIMIT_ENABLED` |
| `rate_limit.default.requests_per_second` | `LLM_PROXY_RATE_LIMIT_DEFAULT_REQUESTS_PER_SECOND` |
| `rate_limit.default.burst` | `LLM_PROXY_RATE_LIMIT_DEFAULT_BURST` |
| `rate_limit.whitelist` | `LLM_PROXY_RATE_LIMIT_WHITELIST` |
| `rate_limit.overrides` | `LLM_PROXY_RATE_LIMIT_OVERRIDES` |
| `providers.openai.base_url` | `LLM_PROXY_PROVIDERS_OPENAI_BASE_URL` |
| `providers.anthropic.base_url` | `LLM_PROXY_PROVIDERS_ANTHROPIC_BASE_URL` |

## 解析方式

- 显式使用 Viper `BindEnv` 绑定每个已知字段，不依赖 `AutomaticEnv` 对 `Unmarshal` 的隐式行为。
- `string`、`bool`、`int` 和 `float64` 由 Viper/Mapstructure 按配置结构的目标类型转换。
- `whitelist` 使用 JSON 字符串数组，例如 `["sk-a","sk-b"]`。
- `overrides` 使用 JSON 对象，例如 `{"sk-a":{"requests_per_second":100,"burst":200}}`。
- 复杂字段在常规 `Unmarshal` 后显式使用 `encoding/json` 解析，以保证格式稳定且错误清晰。

## 错误处理

- 环境变量中的数字、布尔值或 JSON 无法转换时，`config.Load` 返回错误，不静默回退到 YAML 或默认值。
- JSON 解析错误包含对应环境变量名，便于定位。
- `.env` 缺失与 `config.yaml` 缺失都是非致命情况，其他读取或解析错误保持致命。

## 文档与示例

- 新增 `.env.example`，列出全部变量并只使用非敏感示例值。
- 更新 README，说明命名规则、优先级、JSON 格式和 `.env` 用法。
- 更新 `AGENTS.md` 中的配置加载约束和改动落点。
- 保留现有 `.gitignore` 对 `.env` 的忽略，`.env.example` 必须可跟踪。

## 测试策略

1. 先编写失败测试，证明标量环境变量覆盖 YAML 和默认值。
2. 验证已存在的进程环境变量不被 `.env` 覆盖。
3. 验证 `whitelist` 数组和 `overrides` 对象的 JSON 覆盖。
4. 验证非法标量和非法 JSON 返回包含变量名的错误。
5. 验证 `.env` 不存在时仍正常使用 YAML/默认值。
6. 运行 `go test ./...`、`go test -race ./...` 和 `go build -o /tmp/llm-proxy ./cmd/proxy`。

## 范围边界

- 不实现配置热更新。
- 不支持为 `overrides` 中的每个动态 Key 生成独立环境变量；整个映射通过一个 JSON 值提供。
- 不修改限流、代理、日志或 Dashboard 的业务语义。
- 用户已有的 Dockerfile 改动保留，不纳入本功能实现。
