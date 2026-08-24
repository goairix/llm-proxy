# Dashboard OpenAI Responses API Examples Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 Dashboard 使用指南的 CURL 和 Python 区块增加 OpenAI Responses API 调用示例。

**Architecture:** 仅修改已内嵌的 Dashboard HTML，把 Responses API 示例追加到现有 OpenAI 区块。CURL 使用独立 URL DOM ID 并复用当前 `data.base_url` 替换逻辑；Python 复用已有 `OpenAI` client。

**Tech Stack:** Go 1.25、`go:embed`、静态 HTML/JavaScript、标准库 `httptest` 与 `strings`

---

## File Map

- `internal/dashboard/handler_test.go`：锁定 Responses CURL/Python 示例、DOM ID 和 base URL 替换列表。
- `internal/dashboard/web/index.html`：增加两个调用示例并注册 CURL URL ID。

### Task 1: 增加 Dashboard Responses API 示例

**Files:**
- Modify: `internal/dashboard/handler_test.go`
- Modify: `internal/dashboard/web/index.html`

- [ ] **Step 1: 写 Dashboard HTML 失败测试**

在 `internal/dashboard/handler_test.go` 增加：

```go
func TestHandler_OpenAIResponsesExamples(t *testing.T) {
	h, _ := newTestHandler("1.0.0")
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	body := recorder.Body.String()
	for _, marker := range []string{
		`id="url-openai-responses"`,
		`/openai/v1/responses`,
		`client.responses.create(`,
		`print(response.output_text)`,
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("dashboard HTML missing Responses API marker %q", marker)
		}
	}
	if got := strings.Count(body, "url-openai-responses"); got < 2 {
		t.Errorf("url-openai-responses occurrences = %d, want at least 2 (element and replacement list)", got)
	}
}
```

- [ ] **Step 2: 运行测试并确认 RED**

Run: `gofmt -w internal/dashboard/handler_test.go && go test ./internal/dashboard -run TestHandler_OpenAIResponsesExamples -count=1`

Expected: FAIL，至少报告缺失 `id="url-openai-responses"` 和 `client.responses.create(`。

- [ ] **Step 3: 追加 CURL Responses API 示例**

在 `internal/dashboard/web/index.html` 的 CURL tab/OpenAI `<pre>` 中，保留现有 Chat Completions 内容，在流式示例前插入：

```html
<span class="comment"># Responses API</span>
curl <span class="url" id="url-openai-responses">http://localhost:8080</span>/openai/v1/responses \
  <span class="flag">-H</span> <span class="string">"Authorization: Bearer $OPENAI_API_KEY"</span> \
  <span class="flag">-H</span> <span class="string">"Content-Type: application/json"</span> \
  <span class="flag">-d</span> <span class="string">'{"model": "gpt-5", "input": "Hello"}'</span>
```

把 CURL tab 现有的 `# 普通请求` 标题改为 `# Chat Completions`，避免与新示例混淆。

- [ ] **Step 4: 追加 Python Responses API 示例**

在 Python tab 的 OpenAI SDK `<pre>` 中，保留已有 client 和 Chat Completions 调用。在现有 `print(response.choices[0].message.content)` 后追加：

```html

<span class="comment"># Responses API</span>
response = client.responses.create(
    model=<span class="string">"gpt-5"</span>,
    input=<span class="string">"Hello"</span>,
)
print(response.output_text)
```

同时在现有 Chat 调用前增加 `<span class="comment"># Chat Completions</span>`。

- [ ] **Step 5: 注册 Responses CURL URL ID**

在页面脚本 `urlIds` 的 CURL 分组中改为：

```javascript
'url-openai', 'url-openai-responses', 'url-openai-stream',
'url-anthropic', 'url-anthropic-stream',
```

不增加 fetch、timer、后端 JSON 字段或 Python 的新 URL ID。

- [ ] **Step 6: 运行 Dashboard 测试并确认 GREEN**

Run: `gofmt -w internal/dashboard/handler_test.go && go test ./internal/dashboard -count=1`

Expected: `ok github.com/goairix/llm-proxy/internal/dashboard`。

- [ ] **Step 7: 运行全量验证**

Run: `go test ./...`

Run: `go build -o /tmp/llm-proxy ./cmd/proxy`

Run: `git diff --check`

Expected: 三条命令均退出码 0，无测试失败、构建错误或 whitespace error。

- [ ] **Step 8: 提交实现**

```bash
git add internal/dashboard/handler_test.go internal/dashboard/web/index.html
git commit -m "feat: add Responses API dashboard examples"
```
