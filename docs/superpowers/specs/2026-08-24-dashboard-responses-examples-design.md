# Dashboard OpenAI Responses API 示例设计

## 目标

在 Dashboard 的“使用指南”中增加 OpenAI Responses API 的 CURL 和 Python 调用示例，保留现有 Chat Completions 示例。

## 范围

- 修改 `internal/dashboard/web/index.html`。
- 修改 `internal/dashboard/handler_test.go`。
- 不修改 Dashboard 布局、后端接口、代理路由、配置或其他语言 tab。

## 页面内容

### CURL

在现有 OpenAI CURL 区块中，保留 Chat Completions 普通/流式示例，并追加 Responses API 普通请求：

- 路径：`POST <base_url>/openai/v1/responses`
- Header：`Authorization: Bearer $OPENAI_API_KEY`、`Content-Type: application/json`
- Body：`{"model":"gpt-5","input":"Hello"}`

Responses URL 使用独立 DOM ID `url-openai-responses`，便于注入配置的 Dashboard base URL。

### Python

在现有 OpenAI SDK 区块中复用：

```python
client = OpenAI(
    base_url="<base_url>/openai/v1",
    api_key="your-openai-api-key",
)
```

保留 `client.chat.completions.create(...)`，并追加：

```python
response = client.responses.create(
    model="gpt-5",
    input="Hello",
)
print(response.output_text)
```

Python base URL 继续使用现有 `url-py-openai` ID，不重复创建 client 或 URL 占位符。

## Base URL 注入

将 `url-openai-responses` 加入现有 `urlIds` 列表。Dashboard 渲染时继续由当前 `data.base_url` 替换占位地址，不增加 fetch、timer 或新的后端数据字段。

## 测试

在 Dashboard handler 测试中断言渲染后 HTML 包含：

- `id="url-openai-responses"`；
- `/openai/v1/responses`；
- `client.responses.create(`；
- `response.output_text`；
- `url-openai-responses` 已出现在 base URL 替换列表。

测试先在示例缺失时失败，再增加页面内容使其通过。最后运行 `go test ./internal/dashboard -count=1`、`go test ./...` 和 `go build -o /tmp/llm-proxy ./cmd/proxy`。
