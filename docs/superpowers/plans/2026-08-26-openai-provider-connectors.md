# OpenAI 与 OpenAI-Compatible Provider Connector Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **中文说明：** 执行时必须使用“子代理驱动开发”（推荐）或“按计划执行”技能，逐项勾选并完成任务。

**Goal:** 把统一网关从 Fake Connector 扩展为平台托管的 OpenAI/OpenAI-Compatible 双协议真实上游，并新增 OpenAI Responses 兼容入口和租户透明的分层凭据池。

**Architecture:** Provider 是 Connector 类型和 BaseURL 的唯一事实来源，Deployment 只保存上游模型、上游协议、作用域和能力。请求固定使用同一不可变 Snapshot Session 完成 Virtual Key 认证、Alias 路由、Provider 解析和凭据池选择，再由标准库 `net/http` Connector 把统一 Request/Response/Event 映射到 Responses 或 Chat Completions；明文上游 Key 只在 Connector 内短暂存在。

**Tech Stack:** Go 1.25、标准库 `net/http`/`encoding/json`/`bufio`、GORM、gormigrate、PostgreSQL、Google Wire、OpenTelemetry `otelhttp`、`httptest.Server`。

---

## 实施约束

- 全程在普通分支 `feat/provider-connectors` 上工作，禁止使用 worktree。
- 严格按任务顺序执行；每个任务先写失败测试，再写最小实现，再提交。
- 不修改现有 `/openai/*`、`/anthropic/*` 透明代理语义。
- 数据库迁移、Entity 和 Repository 禁止外键、GORM Relationship、Association、Preload 和级联。
- 所有新增实体 ID 由代码生成 UUIDv7；数据库默认值只作手工 SQL 兜底。
- PostgreSQL DSN 继续要求 `TimeZone=Asia/Shanghai`。
- 不加入自动重试、协议探测、跨凭据切换、多 BaseURL、健康检查或熔断。
- 每个 Go 任务结束运行 `gofmt` 和目标包测试；只在依赖变化时运行 `go mod tidy`。本计划不需要新增第三方依赖。

## 文件职责总览

### 领域与应用

- `internal/domain/catalog/model/provider.go`：Provider Connector 类型、BaseURL 规范化和协议支持矩阵。
- `internal/domain/catalog/model/deployment.go`：Deployment 新结构。
- `internal/domain/catalog/model/upstream_protocol.go`：上游协议值对象。
- `internal/domain/inference/model/content.go`：Refusal 内容块。
- `internal/domain/inference/model/event.go`：RefusalDelta 与事件序列约束。
- `internal/application/gateway/snapshot/model.go`：Provider、Deployment、CredentialPool 运行时只读视图。
- `internal/application/gateway/snapshot/credential_selector.go`：分层凭据池与进程内原子轮询。
- `internal/application/gateway/port/connector.go`：不含明文 Key 的 Invocation。
- `internal/application/gateway/port/errors.go`：Connector 内部稳定错误分类。
- `internal/application/gateway/service/gateway.go`：同一 Snapshot Session 内完成路由、Provider 和凭据选择。

### 持久化与控制面

- `internal/infrastructure/persistence/migration/v2026082601_provider_connectors.go`：新增 BaseURL/协议并移除 Deployment 旧列。
- `internal/infrastructure/persistence/entity/catalog.go`：新 Entity 字段，无关联声明。
- `internal/infrastructure/persistence/repository/catalog/mapper.go`：新领域字段逐字段映射。
- `internal/application/controlplane/service/catalog.go`：平台管理 Provider、分层凭据和 Deployment 协议。
- `internal/interfaces/http/handler/controlplane/catalog.go`、`view.go`：控制面 DTO。

### 上游 Connector

- `internal/infrastructure/connector/openai/connector.go`：协议选择与调用编排。
- `internal/infrastructure/connector/openai/credential.go`：凭据解密和严格 `api_key` 解析。
- `internal/infrastructure/connector/openai/client.go`：URL、HTTP 请求、大小限制和取消。
- `internal/infrastructure/connector/openai/errors.go`：上游状态与网络错误分类。
- `internal/infrastructure/connector/openai/chat_request.go`、`chat_response.go`、`chat_stream.go`：Chat Completions Wire Adapter。
- `internal/infrastructure/connector/openai/responses_request.go`、`responses_response.go`、`responses_stream.go`：Responses Wire Adapter。
- `internal/infrastructure/connector/openai/sse.go`：有大小和空闲超时边界的 SSE Reader。

### HTTP 接口与组装

- `internal/interfaces/http/protocol/openai/responses/`：OpenAI Responses 入口 DTO、Decoder、Encoder 和 SSE。
- `internal/interfaces/http/handler/gateway/responses.go`：`POST /v1/responses` Handler。
- `internal/interfaces/http/router/router.go`：新数据面路由和静态遥测 Endpoint。
- `internal/infrastructure/config/config.go`、`env.go`：全局上游 Transport 配置。
- `internal/infrastructure/observability/http.go`：按固定上游 Provider/Endpoint 脱敏的 Client Transport。
- `internal/di/provider/gateway.go`、`controlplane.go`、`transparent.go`：共享 Cipher、Connector、Handler 和 Router 组装。
- `internal/di/wire_gen.go`：Wire 生成结果。

---

### Task 1: 重塑 Provider 与 Deployment 领域模型

**Files:**
- Create: `internal/domain/catalog/model/upstream_protocol.go`
- Modify: `internal/domain/catalog/model/provider.go`
- Modify: `internal/domain/catalog/model/deployment.go`
- Modify: `internal/domain/catalog/model/model_test.go`

- [ ] **Step 1: 写 Provider URL 和协议矩阵失败测试**

在 `model_test.go` 增加表驱动用例，锁定：OpenAI/Compatible 必须有规范化绝对 URL；Fake 必须没有 URL；Provider 只支持明确协议。

```go
func TestProviderNormalizesBaseURLAndSupportsProtocols(t *testing.T) {
	provider, err := NewProviderWithBaseURL("OpenAI", ConnectorOpenAI, "https://api.openai.com/")
	if err != nil {
		t.Fatal(err)
	}
	if provider.BaseURL != "https://api.openai.com" {
		t.Fatalf("BaseURL=%q", provider.BaseURL)
	}
	if !provider.Supports(UpstreamResponses) || !provider.Supports(UpstreamChatCompletions) || provider.Supports(UpstreamFake) {
		t.Fatalf("unexpected protocol support")
	}
}

func TestProviderRejectsUnsafeBaseURL(t *testing.T) {
	for _, value := range []string{"", "api.openai.com", "ftp://api.openai.com", "https://u:p@api.openai.com", "https://api.openai.com?q=1", "https://api.openai.com#x"} {
		if _, err := NewProviderWithBaseURL("OpenAI", ConnectorOpenAI, value); err == nil {
			t.Fatalf("BaseURL %q accepted", value)
		}
	}
}
```

- [ ] **Step 2: 写 Deployment 去除重复字段的失败测试**

```go
func TestDeploymentOwnsProtocolButNotProviderTransportConfiguration(t *testing.T) {
	providerID := uuid.Must(uuid.NewV7())
	deployment, err := NewDeploymentWithProtocol(providerID, "gpt-5", "gpt-5", UpstreamResponses, Scope{Kind: ScopePlatform}, CapabilitySet{Text: true, Streaming: true})
	if err != nil {
		t.Fatal(err)
	}
	if deployment.ProviderID != providerID || deployment.UpstreamProtocol != UpstreamResponses {
		t.Fatalf("deployment=%+v", deployment)
	}
}
```

- [ ] **Step 3: 运行领域测试并确认失败**

Run: `go test -count=1 ./internal/domain/catalog/model`

Expected: FAIL，提示 `ConnectorOpenAI`、`UpstreamResponses`、新构造器或 `BaseURL` 尚不存在。

- [ ] **Step 4: 实现 Connector 类型、BaseURL 和 UpstreamProtocol**

`upstream_protocol.go` 使用明确枚举：

```go
package model

import "fmt"

type UpstreamProtocol string

const (
	UpstreamResponses       UpstreamProtocol = "responses"
	UpstreamChatCompletions UpstreamProtocol = "chat_completions"
	UpstreamFake            UpstreamProtocol = "fake"
)

func (p UpstreamProtocol) Validate() error {
	switch p {
	case UpstreamResponses, UpstreamChatCompletions, UpstreamFake:
		return nil
	default:
		return fmt.Errorf("unsupported upstream protocol %q", p)
	}
}
```

`provider.go` 增加 `ConnectorFake`、`ConnectorOpenAI`、`ConnectorOpenAICompatible`、`BaseURL`、`normalizeBaseURL` 和 `Supports`。URL 规范化必须使用 `net/url`，允许 `http`/`https` 和路径前缀，拒绝 userinfo/query/fragment，移除尾部 `/`。

```go
func (p Provider) Supports(protocol UpstreamProtocol) bool {
	switch p.ConnectorType {
	case ConnectorFake:
		return protocol == UpstreamFake
	case ConnectorOpenAI, ConnectorOpenAICompatible:
		return protocol == UpstreamResponses || protocol == UpstreamChatCompletions
	default:
		return false
	}
}
```

`provider.go` 先增加迁移期构造器：

```go
func NewProviderWithBaseURL(name, connectorType, baseURL string) (*Provider, error)
```

现有二参数 `NewProvider(name, connectorType)` 暂时只服务尚未迁移的 Fake 调用点，并在注释中标明会由 Task 6 删除；它内部给 Fake Provider 设置空 BaseURL。`Provider.Validate` 对 Fake 强制 BaseURL 为空，对 OpenAI/Compatible 强制规范化后的 BaseURL 非空。

`deployment.go` 在迁移期同时保留旧字段/旧构造器，并新增最终字段与迁移构造器：

```go
type Deployment struct {
	sharedmodel.Entity
	ProviderID       uuid.UUID
	// CredentialID 与 ConnectorType 仅为逐层迁移保留，Task 6 删除。
	CredentialID     *uuid.UUID
	ConnectorType    string
	Name             string
	UpstreamModel    string
	UpstreamProtocol UpstreamProtocol
	Scope            Scope
	Capabilities     CapabilitySet
	Status           sharedmodel.Status
}

func NewDeploymentWithProtocol(providerID uuid.UUID, name, upstreamModel string, protocol UpstreamProtocol, scope Scope, capabilities CapabilitySet) (*Deployment, error)
```

保留现有 `NewDeployment(providerID, credentialID, name, upstreamModel, connectorType, scope, capabilities)` 以维持尚未迁移调用点编译；它只允许 `connectorType == "fake"`，并把 `UpstreamProtocol` 设为 `UpstreamFake`。`Validate` 在迁移期接受“新协议字段”或“旧 Fake 字段”两种形态，但拒绝混合矛盾值。Task 3-5 只写新字段，Task 6 删除兼容字段和旧构造器。

- [ ] **Step 5: 格式化并运行领域测试**

Run: `gofmt -w internal/domain/catalog/model && go test -count=1 ./internal/domain/catalog/model`

Expected: PASS。

- [ ] **Step 6: 提交领域模型**

```bash
git add internal/domain/catalog/model
git commit -m "feat: model provider transport configuration"
```

---

### Task 2: 增加统一 Refusal 响应语义

**Files:**
- Modify: `internal/domain/inference/model/content.go`
- Modify: `internal/domain/inference/model/response.go`
- Modify: `internal/domain/inference/model/event.go`
- Modify: `internal/domain/inference/model/event_test.go`
- Modify: `internal/domain/inference/model/model_test.go`
- Modify: `internal/interfaces/http/protocol/openai/response.go`
- Modify: `internal/interfaces/http/protocol/openai/encode.go`
- Modify: `internal/interfaces/http/protocol/openai/protocol_test.go`
- Modify: `internal/interfaces/http/protocol/anthropic/encode.go`
- Modify: `internal/interfaces/http/protocol/anthropic/protocol_test.go`
- Modify: `internal/infrastructure/connector/fake/connector.go`
- Modify: `internal/infrastructure/connector/fake/connector_test.go`

- [ ] **Step 1: 写 Refusal Content 和 Event 失败测试**

```go
func TestRefusalContentAndDeltaValidate(t *testing.T) {
	block := ContentBlock{Type: ContentRefusal, Refusal: &RefusalContent{Text: "无法协助"}}
	if err := block.Validate(); err != nil {
		t.Fatal(err)
	}
	validator := NewSequenceValidator()
	events := []Event{
		NewResponseStartAt(uuid.Must(uuid.NewV7()), "assistant", time.Now()),
		NewContentBlockStart(0, ContentRefusal),
		NewRefusalDelta(0, "无法协助"),
		NewContentBlockStop(0),
		NewResponseFinish(StopContentFilter),
	}
	for _, event := range events {
		if err := validator.Push(event); err != nil {
			t.Fatal(err)
		}
	}
}
```

另加反例：RefusalDelta 指向 Text 活动块、空文本、Finish 后 Delta 都必须失败。

- [ ] **Step 2: 运行统一模型测试并确认失败**

Run: `go test -count=1 ./internal/domain/inference/model`

Expected: FAIL，提示 Refusal 类型和构造器不存在。

- [ ] **Step 3: 实现 Refusal 内容块和流事件**

在 `content.go` 增加：

```go
const ContentRefusal ContentType = "refusal"

type RefusalContent struct { Text string }
```

`ContentBlock` 增加 `Refusal *RefusalContent`，继续执行“Type 与唯一 Payload 一致”校验。`Response.Validate` 允许 Text、ToolCall、Refusal。

在 `event.go` 增加：

```go
const EventRefusalDelta EventType = "refusal_delta"

type RefusalDeltaEvent struct {
	Index int
	Text  string
}

func NewRefusalDelta(index int, value string) Event {
	return Event{Type: EventRefusalDelta, RefusalDelta: &RefusalDeltaEvent{Index: index, Text: value}}
}
```

SequenceValidator 只允许 RefusalDelta 写入 `ContentRefusal` 活动块。

- [ ] **Step 4: 更新现有入口 Encoder 和 Fake 事件生成器**

OpenAI Chat 普通响应增加 `assistantMessageDTO.Refusal *string`，流 Delta 增加 `Refusal string`；Anthropic 把 Refusal 编码为普通 text block，并保持 `StopContentFilter → refusal`。Fake 的通用 `responseEvents` 对 Refusal 内容生成 ContentBlockStart、RefusalDelta、ContentBlockStop。

```go
case inference.ContentRefusal:
	value := block.Refusal.Text
	message.Refusal = &value
```

- [ ] **Step 5: 运行领域与现有协议 Race 测试**

Run: `gofmt -w internal/domain/inference internal/interfaces/http/protocol/openai internal/interfaces/http/protocol/anthropic internal/infrastructure/connector/fake`

Run: `go test -race -count=1 ./internal/domain/inference/model ./internal/interfaces/http/protocol/openai ./internal/interfaces/http/protocol/anthropic ./internal/infrastructure/connector/fake`

Expected: PASS。

- [ ] **Step 6: 提交 Refusal 语义**

```bash
git add internal/domain/inference internal/interfaces/http/protocol/openai internal/interfaces/http/protocol/anthropic internal/infrastructure/connector/fake
git commit -m "feat: add provider refusal semantics"
```

---

### Task 3: 迁移持久化模型

**Files:**
- Modify: `internal/domain/catalog/repository/repository.go`
- Modify: `internal/infrastructure/persistence/entity/catalog.go`
- Modify: `internal/infrastructure/persistence/entity/no_foreign_key_test.go`
- Modify: `internal/infrastructure/persistence/migration/migration.go`
- Create: `internal/infrastructure/persistence/migration/v2026082601_provider_connectors.go`
- Modify: `internal/infrastructure/persistence/migration/migration_test.go`
- Modify: `internal/infrastructure/persistence/repository/catalog/mapper.go`
- Modify: `internal/infrastructure/persistence/repository/catalog/repository.go`
- Modify: `internal/infrastructure/persistence/repository/catalog/repository_test.go`
- Modify: `internal/infrastructure/persistence/repository/runtime/reader_test.go`

- [ ] **Step 1: 写 Entity、Mapper 和迁移失败测试**

测试必须断言：Provider 有 BaseURL；Deployment 只有 UpstreamProtocol；迁移后数据库不存在旧列和外键。

```go
func assertProviderConnectorMigrationShape(t *testing.T, db *gorm.DB) {
	t.Helper()
	if !db.Migrator().HasColumn(&entity.Provider{}, "base_url") {
		t.Fatal("providers.base_url missing")
	}
	if !db.Migrator().HasColumn(&entity.Deployment{}, "upstream_protocol") {
		t.Fatal("deployments.upstream_protocol missing")
	}
	if db.Migrator().HasColumn("deployments", "credential_id") || db.Migrator().HasColumn("deployments", "connector_type") {
		t.Fatal("legacy deployment columns remain")
	}
}
```

在现有 `TestPostgresInitialMigration` 的 `Up(db)` 成功后调用 `assertProviderConnectorMigrationShape(t, db)`；不新增第二套数据库启动 helper。

Repository 测试构造 Responses Deployment，保存并读取后逐字段相等。

- [ ] **Step 2: 运行持久化测试并确认失败**

Run: `go test -count=1 ./internal/infrastructure/persistence/entity ./internal/infrastructure/persistence/migration ./internal/infrastructure/persistence/repository/catalog`

Expected: FAIL，提示新列、迁移或 Mapper 缺失。

- [ ] **Step 3: 修改 Entity、Mapper 和 Repository 接口**

```go
type Provider struct {
	BaseEntity
	Name          string `gorm:"type:varchar(255);not null"`
	ConnectorType string `gorm:"type:varchar(64);not null;index:idx_providers_connector_type"`
	BaseURL       string `gorm:"type:text;not null;default:''"`
	Status        string `gorm:"type:varchar(32);not null;index:idx_providers_status"`
}

type Deployment struct {
	BaseEntity
	ProviderID       uuid.UUID `gorm:"type:uuid;not null;index:idx_deployments_provider_id"`
	Name             string    `gorm:"type:varchar(255);not null"`
	UpstreamModel    string    `gorm:"type:varchar(255);not null"`
	UpstreamProtocol string    `gorm:"type:varchar(32);not null;default:'';index:idx_deployments_upstream_protocol"`
	ScopeKind        string    `gorm:"type:varchar(32);not null;index:idx_deployments_scope"`
	OrganizationID   *uuid.UUID `gorm:"type:uuid;index:idx_deployments_organization_id"`
	ProjectID        *uuid.UUID `gorm:"type:uuid;index:idx_deployments_project_id"`
	Capabilities     string    `gorm:"type:text;not null"`
	Status           string    `gorm:"type:varchar(32);not null;index:idx_deployments_status"`
}
```

Mapper 显式映射 BaseURL 和 UpstreamProtocol，不保留旧字段。为保持本提交之后全仓编译，迁移期暂保留 `DeploymentRepository.HasActiveByCredential`，其 PostgreSQL 实现固定返回 `(false, nil)`，因为 Deployment 表已无 Credential 绑定；Task 4 会同时删除接口方法和最后一个 Application 调用点。

- [ ] **Step 4: 增加前向迁移**

迁移 ID 使用 `2026082601_provider_connectors`。迁移开始先统计非 Fake Provider；Phase 1B 正式只支持 Fake，若发现非 Fake 旧数据则返回中文错误，避免猜测 BaseURL。

```go
const ProviderConnectorMigrationID = "2026082601_provider_connectors"

func migrateProviderConnectors(db *gorm.DB) error {
	var legacy int64
	if err := db.Model(&entity.Provider{}).Where("connector_type <> ?", "fake").Count(&legacy).Error; err != nil {
		return err
	}
	if legacy != 0 {
		return fmt.Errorf("检测到 %d 条未支持的非 Fake 旧 Provider，请先清理后再迁移", legacy)
	}
	if err := db.Migrator().AddColumn(&entity.Provider{}, "BaseURL"); err != nil { return err }
	if err := db.Migrator().AddColumn(&entity.Deployment{}, "UpstreamProtocol"); err != nil { return err }
	if err := db.Model(&entity.Deployment{}).Where("upstream_protocol = ''").Update("upstream_protocol", "fake").Error; err != nil { return err }
	if err := db.Migrator().DropColumn("deployments", "credential_id"); err != nil { return err }
	return db.Migrator().DropColumn("deployments", "connector_type")
}
```

Rollback 恢复 `connector_type` 并从 Provider 回填，恢复 nullable `credential_id`，再删除新列；明确测试 Credential 旧绑定无法恢复为非空。

- [ ] **Step 5: 运行持久化与无外键测试**

Run: `gofmt -w internal/domain/catalog/repository internal/infrastructure/persistence`

Run: `go test -count=1 ./internal/infrastructure/persistence/...`

Expected: PASS；未配置真实 PG 的纵向用例可 Skip，但基于测试数据库的迁移测试必须通过。

Run: `go test -count=1 ./...`

Expected: PASS，迁移期 Repository 接口仍保证全仓可编译。

- [ ] **Step 6: 提交持久化迁移**

```bash
git add internal/domain/catalog/repository internal/infrastructure/persistence
git commit -m "feat: persist provider connector configuration"
```

---

### Task 4: 更新平台控制面

**Files:**
- Modify: `internal/domain/catalog/repository/repository.go`
- Modify: `internal/infrastructure/persistence/repository/catalog/repository.go`
- Modify: `internal/application/controlplane/dto/command.go`
- Modify: `internal/application/controlplane/service/catalog.go`
- Modify: `internal/application/controlplane/service/service_test.go`
- Modify: `internal/interfaces/http/handler/controlplane/catalog.go`
- Modify: `internal/interfaces/http/handler/controlplane/view.go`
- Modify: `internal/interfaces/http/handler/controlplane/handler_test.go`
- Modify: `internal/interfaces/http/handler/controlplane/integration_test.go`

- [ ] **Step 1: 写控制面失败测试**

覆盖以下用例：

- 创建 OpenAI Provider 必须保存规范化 BaseURL。
- 创建 Organization/Project Scope Credential 成功。
- OpenAI Credential 只接受 `{"api_key":"non-empty"}`。
- Credential 创建响应、查询和列表只返回 ID、Provider、Scope、状态与审计时间，不返回密文、包装数据密钥或可恢复秘密。
- 停用最后一个 Credential 不查询 Deployment 引用并成功递增 Revision。
- Deployment 请求拒绝旧 `credential_id`/`connector_type`，接受 `upstream_protocol`。
- OpenAI/Compatible 可建两种协议，Fake 只能建 Fake。
- RouteTarget 不再限制为 Fake Deployment。
- 所有 Provider/Credential/Deployment 写接口继续只挂载在平台控制面认证下；数据面不存在这些管理路由。

```go
func TestCanDisableCredentialReferencedByActiveDeployment(t *testing.T) {
	provider, _ := catalogmodel.NewProviderWithBaseURL("OpenAI", catalogmodel.ConnectorOpenAI, "https://api.openai.com")
	credential, _ := catalogmodel.NewProviderCredential(provider.ID, catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, catalogmodel.SealedCredential{
		KeyVersion: "v1", WrappedKeyNonce: []byte{1}, WrappedDataKey: []byte{2}, PayloadNonce: []byte{3}, Ciphertext: []byte{4},
	})
	credentials := &credentialRepo{items: map[uuid.UUID]*catalogmodel.ProviderCredential{credential.ID: credential}}
	notifier := &recordingRefreshNotifier{}
	service := NewCatalogService(
		&providerRepo{items: map[uuid.UUID]*catalogmodel.Provider{provider.ID: provider}}, credentials,
		&deploymentRepo{}, &aliasRepo{}, &targetRepo{}, &organizationRepo{}, &projectRepo{},
		&revisionRepo{}, &testTransactions{}, &recordingCipher{}, notifier,
	)
	status := sharedmodel.StatusDisabled
	result, err := service.UpdateProviderCredential(context.Background(), dto.UpdateProviderCredential{ID: credential.ID, Status: &status})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != status || result.Revision == 0 || notifier.calls != 1 {
		t.Fatalf("result=%+v notify=%d", result, notifier.calls)
	}
}
```

- [ ] **Step 2: 运行控制面测试并确认失败**

Run: `go test -count=1 ./internal/application/controlplane/service ./internal/interfaces/http/handler/controlplane`

Expected: FAIL，旧 DTO、平台级限制和固定凭据保护仍生效。

- [ ] **Step 3: 修改 Command 和 HTTP DTO**

```go
type CreateProvider struct {
	Name          string
	ConnectorType string
	BaseURL       string
}

type UpdateProvider struct {
	ID      uuid.UUID
	Name    *string
	BaseURL *string
	Status  *sharedmodel.Status
}

type CreateDeployment struct {
	ProviderID       uuid.UUID
	Name             string
	UpstreamModel    string
	UpstreamProtocol catalogmodel.UpstreamProtocol
	Scope            catalogmodel.Scope
	Capabilities     catalogmodel.CapabilitySet
}

type UpdateDeployment struct {
	ID               uuid.UUID
	Name             *string
	UpstreamModel    *string
	UpstreamProtocol *catalogmodel.UpstreamProtocol
	Capabilities     *catalogmodel.CapabilitySet
	Status           *sharedmodel.Status
}
```

Provider View 增加 `base_url`；Deployment View 增加 `upstream_protocol` 并删除旧字段。

Catalog Service 在本任务中使用迁移构造器 `catalogmodel.NewProviderWithBaseURL` 和 `catalogmodel.NewDeploymentWithProtocol`；最终构造器命名在 Task 6 清理兼容入口时统一。

- [ ] **Step 4: 实现 Provider-aware Credential 校验**

```go
func validateCredentialPayload(connectorType string, payload []byte) error {
	if connectorType == catalogmodel.ConnectorFake {
		return controlerrors.New(controlerrors.InvalidRequest, "Fake Provider 不接受供应商凭据", "credential", nil)
	}
	var value struct { APIKey string `json:"api_key"` }
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil || strings.TrimSpace(value.APIKey) == "" {
		return controlerrors.New(controlerrors.InvalidRequest, "credential 必须只包含非空 api_key", "credential", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return controlerrors.New(controlerrors.InvalidRequest, "credential 只能包含一个 JSON 值", "credential", nil)
	}
	return nil
}
```

在 Seal 前调用该校验；创建凭据时删除仅 Platform Scope 限制，继续在事务内验证 Organization/Project。

- [ ] **Step 5: 移除固定凭据和 Fake-only 路由规则**

Create/Update Deployment 只校验 Provider、协议支持和 Scope。Credential 停用不再调用 `HasActiveByCredential`，并在同一步从 `DeploymentRepository` 及 PostgreSQL Repository 删除这个迁移期方法。Alias/Target 激活继续校验 Provider、Deployment 和可见性，但不检查池是否非空，也不限制 Connector 为 Fake。

- [ ] **Step 6: 格式化并运行控制面测试**

Run: `gofmt -w internal/domain/catalog/repository internal/infrastructure/persistence/repository/catalog internal/application/controlplane internal/interfaces/http/handler/controlplane`

Run: `go test -race -count=1 ./internal/application/controlplane/... ./internal/interfaces/http/handler/controlplane`

Expected: PASS。

Run: `go test -count=1 ./...`

Expected: PASS。

- [ ] **Step 7: 提交控制面变更**

```bash
git add internal/domain/catalog/repository internal/infrastructure/persistence/repository/catalog internal/application/controlplane internal/interfaces/http/handler/controlplane
git commit -m "feat: manage provider connector resources"
```

---

### Task 5: 编译不可变 Provider 与凭据池快照

**Files:**
- Modify: `internal/application/gateway/snapshot/model.go`
- Modify: `internal/application/gateway/snapshot/authenticator.go`
- Modify: `internal/application/gateway/snapshot/compiler.go`
- Modify: `internal/application/gateway/snapshot/compiler_test.go`
- Modify: `internal/application/gateway/snapshot/store_test.go`
- Create: `internal/application/gateway/snapshot/credential_selector.go`
- Create: `internal/application/gateway/snapshot/credential_selector_test.go`
- Modify: `internal/infrastructure/persistence/repository/runtime/reader_test.go`
- Modify: `internal/infrastructure/snapshot/integration_test.go`

- [ ] **Step 1: 写快照结构和撤销优先失败测试**

```go
func TestCompilerPublishesRouteWithEmptyCredentialPoolAfterRevocation(t *testing.T) {
	source := validOpenAISourceConfig(t)
	source.Credentials[0].Status = sharedmodel.StatusDisabled
	source.Revision++
	snapshot, err := NewCompiler().Compile(source, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	session := Session{snapshot: snapshot}
	access, err := session.Authenticate(snapshotTestVirtualKey, time.Now())
	if err != nil { t.Fatal(err) }
	plan, err := session.Resolve(access.ProjectID, "assistant")
	if err != nil || plan.Deployment.ProviderID == uuid.Nil {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if _, err := session.CredentialPool(access, plan.Deployment.ProviderID); !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("err=%v", err)
	}
}
```

在同一测试文件新增本任务专用 helper，避免把现有 Fake fixture 当成有凭据的配置：

```go
const snapshotTestVirtualKey = "llmp_v1_snapshot-provider-test"

func validOpenAISourceConfig(t *testing.T) SourceConfig {
	t.Helper()
	source := validSourceConfig(t)
	provider, err := catalogmodel.NewProviderWithBaseURL("OpenAI", catalogmodel.ConnectorOpenAI, "https://api.openai.com")
	if err != nil { t.Fatal(err) }
	credential, err := catalogmodel.NewProviderCredential(provider.ID, catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, testSealedCredential())
	if err != nil { t.Fatal(err) }
	deployment, err := catalogmodel.NewDeploymentWithProtocol(
		provider.ID, "GPT-5", "gpt-5", catalogmodel.UpstreamResponses,
		catalogmodel.Scope{Kind: catalogmodel.ScopePlatform},
		catalogmodel.CapabilitySet{Text: true, Streaming: true},
	)
	if err != nil { t.Fatal(err) }
	source.Providers = []catalogmodel.Provider{*provider}
	source.Credentials = []catalogmodel.ProviderCredential{*credential}
	source.Deployments = []catalogmodel.Deployment{*deployment}
	source.RouteTargets[0].DeploymentID = deployment.ID
	source.VirtualKeys[0].Hash = sha256.Sum256([]byte(snapshotTestVirtualKey))
	return source
}
```

另写 Project > Organization > Platform、停用 Scope、不匹配 Provider、稳定 UUID 顺序和深拷贝测试。

更新现有 `TestReaderLoadKeepsOneRepeatableReadViewDuringConcurrentCommit` 的 fixture：旧版本改为 Provider(BaseURL 为空)/Deployment(UpstreamFake)，并在并发事务中同时新增第二个 Provider、Credential 和 Deployment、递增 Revision；Reader 仍必须返回旧 Revision 下三个集合一致的旧视图，`CurrentRevision` 随后才能看到新 Revision。这样直接锁定 Provider、Deployment、CredentialIndex 来自同一个 Repeatable Read 事务。

- [ ] **Step 2: 运行快照测试并确认失败**

Run: `go test -count=1 ./internal/application/gateway/snapshot ./internal/infrastructure/snapshot`

Expected: FAIL，旧 Compiler 仍要求 Deployment Credential。

- [ ] **Step 3: 定义快照 Provider、Deployment 和 CredentialPool**

```go
type Provider struct {
	ID            uuid.UUID
	ConnectorType string
	BaseURL       string
}

type Deployment struct {
	ID               uuid.UUID
	ProviderID       uuid.UUID
	UpstreamModel    string
	UpstreamProtocol catalogmodel.UpstreamProtocol
	Capabilities     catalogmodel.CapabilitySet
	// 仅用于让旧 Gateway 在 Task 7 迁移前继续编译；Compiler 只对 Fake 填充 ConnectorType，Credential 始终 nil。
	ConnectorType    string
	Credential       *CredentialEnvelope
}

type CredentialPoolKey struct {
	ProviderID uuid.UUID
	ScopeKind  catalogmodel.ScopeKind
	TargetID   uuid.UUID
}

type CredentialPool struct {
	Key         CredentialPoolKey
	Credentials []CredentialEnvelope
}
```

RuntimeSnapshot 增加 `providers map[uuid.UUID]Provider` 和 `credentials map[CredentialPoolKey][]CredentialEnvelope`。迁移期 `RoutePlan.Deployment.Credential` 固定为 nil，真实凭据只能通过 `Session.CredentialPool` 取得；Task 7 在 Gateway 改用 Provider/Selector 的同一提交中删除两个兼容字段。

- [ ] **Step 4: 重写 Compiler 凭据索引**

Compiler 对所有 Provider/Credential 做领域与引用校验。只把启用 Provider 下、启用 Scope 内的启用 Credential 放入索引并按 UUID 排序。Deployment 是否 Active 只取决于 Provider、Deployment 和 Scope，不依赖 Credential 数量。

```go
key := credentialPoolKey(credential.ProviderID, credential.Scope)
credentialIndex[key] = append(credentialIndex[key], cloneCredentialEnvelope(credential))
```

返回快照时同时填入 providers、credentials、routes、virtualKeys；删除 `credentialScopeCovers`、`deploymentOrganizationID` 和固定凭据逻辑。为保证任务间全仓编译，Fake Deployment 的兼容 `ConnectorType` 暂填 `provider.ConnectorType`，`Credential` 始终为 nil。

- [ ] **Step 5: 实现 Session 查询和原子轮询 Selector**

```go
var ErrCredentialUnavailable = errors.New("provider credential unavailable")

type CredentialSelector struct { cursors sync.Map }

func NewCredentialSelector() *CredentialSelector { return &CredentialSelector{} }

func (s *CredentialSelector) Select(session Session, access AccessContext, providerID uuid.UUID) (CredentialEnvelope, error) {
	pool, err := session.CredentialPool(access, providerID)
	if err != nil { return CredentialEnvelope{}, err }
	cursorValue, _ := s.cursors.LoadOrStore(pool.Key, &atomic.Uint64{})
	index := cursorValue.(*atomic.Uint64).Add(1) - 1
	return cloneCredentialEnvelope(pool.Credentials[index%uint64(len(pool.Credentials))]), nil
}
```

`Session.CredentialPool` 固定查 Project、Organization、Platform 三个 Key，命中第一个非空池即返回深拷贝。

同时实现 `Session.Provider(providerID uuid.UUID) (Provider, error)`，缺失时返回 `ErrProviderUnavailable`，返回值只含不可变标量；Gateway 不允许从 Request 覆盖该 Provider。

- [ ] **Step 6: 运行快照普通与 Race 测试**

Run: `gofmt -w internal/application/gateway/snapshot internal/infrastructure/persistence/repository/runtime internal/infrastructure/snapshot`

Run: `go test -race -count=1 ./internal/application/gateway/snapshot ./internal/infrastructure/persistence/repository/runtime ./internal/infrastructure/snapshot`

Expected: PASS，包括并发 Selector 轮询无数据竞争。

Run: `go test -count=1 ./...`

Expected: PASS，旧 Gateway 仅通过迁移兼容字段继续工作，真实凭据不再嵌入 RoutePlan。

- [ ] **Step 7: 提交快照与凭据池**

```bash
git add internal/application/gateway/snapshot internal/infrastructure/persistence/repository/runtime internal/infrastructure/snapshot
git commit -m "feat: compile scoped provider credential pools"
```

---

### Task 6: 删除 Catalog 迁移兼容入口

**Files:**
- Modify: `internal/domain/catalog/model/provider.go`
- Modify: `internal/domain/catalog/model/deployment.go`
- Modify: `internal/domain/catalog/model/model_test.go`
- Modify: `internal/application/controlplane/service/catalog.go`
- Modify: `internal/application/controlplane/service/service_test.go`
- Modify: `internal/application/gateway/snapshot/compiler_test.go`
- Modify: `internal/infrastructure/persistence/repository/catalog/repository_test.go`
- Modify: `internal/infrastructure/persistence/repository/runtime/reader_test.go`
- Modify: `internal/infrastructure/snapshot/refresher_test.go`
- Modify: `internal/interfaces/http/handler/controlplane/integration_test.go`
- Modify: `internal/interfaces/http/handler/gateway/integration_test.go`

- [ ] **Step 1: 写最终领域形态失败测试**

用反射锁定 Deployment 不再包含重复的 Provider/Credential 配置，并把新构造器调用改为最终名称：

```go
func TestDeploymentDoesNotDuplicateProviderOrCredentialConfiguration(t *testing.T) {
	typeOfDeployment := reflect.TypeOf(Deployment{})
	for _, field := range []string{"CredentialID", "ConnectorType"} {
		if _, found := typeOfDeployment.FieldByName(field); found {
			t.Fatalf("legacy field %s remains", field)
		}
	}
}
```

把 Task 1 的 Provider/Deployment 用例改为调用：

```go
NewProvider("OpenAI", ConnectorOpenAI, "https://api.openai.com")
NewDeployment(providerID, "gpt-5", "gpt-5", UpstreamResponses, Scope{Kind: ScopePlatform}, CapabilitySet{Text: true, Streaming: true})
```

- [ ] **Step 2: 运行领域测试并确认兼容字段仍存在**

Run: `go test -count=1 ./internal/domain/catalog/model`

Expected: FAIL，反射测试报告 `CredentialID` 或 `ConnectorType` 仍存在，且最终构造器签名尚未生效。

- [ ] **Step 3: 删除旧字段和旧构造器**

`Provider` 只保留最终三参数构造器：

```go
func NewProvider(name, connectorType, baseURL string) (*Provider, error)
```

`Deployment` 只保留最终字段和构造器：

```go
type Deployment struct {
	sharedmodel.Entity
	ProviderID       uuid.UUID
	Name             string
	UpstreamModel    string
	UpstreamProtocol UpstreamProtocol
	Scope            Scope
	Capabilities     CapabilitySet
	Status           sharedmodel.Status
}

func NewDeployment(providerID uuid.UUID, name, upstreamModel string, protocol UpstreamProtocol, scope Scope, capabilities CapabilitySet) (*Deployment, error)
```

删除 `NewProviderWithBaseURL`、`NewDeploymentWithProtocol`、`CredentialID`、`ConnectorType` 和迁移期双形态校验。Fake fixture 统一使用：

```go
provider, err := NewProvider("Fake", ConnectorFake, "")
deployment, err := NewDeployment(provider.ID, "Fake", "fake-model", UpstreamFake, Scope{Kind: ScopePlatform}, CapabilitySet{Text: true, Streaming: true})
```

- [ ] **Step 4: 更新全部剩余构造器调用点**

运行以下检查定位未迁移调用点：

Run: `rg -n 'NewProviderWithBaseURL|NewDeploymentWithProtocol|\.CredentialID|\.ConnectorType' internal --glob '*.go'`

逐一更新本任务 Files 中的测试和 fixture。`Provider.ConnectorType` 是最终保留字段，因此只删除命中 Deployment 变量的 `.ConnectorType`；Compiler、控制面和快照必须改读 `provider.ConnectorType` 与 `deployment.UpstreamProtocol`。完成后运行：

Run: `rg -n 'NewProviderWithBaseURL|NewDeploymentWithProtocol' internal --glob '*.go'`

Expected: 无输出。`CredentialID` 仍可作为 ProviderCredential/运行时 CredentialEnvelope 的身份字段；`.ConnectorType` 只允许出现在 Provider、Snapshot Provider 或 Connector 检查中，不能再属于 Deployment。

- [ ] **Step 5: 格式化并运行全量测试**

Run: `gofmt -w internal/domain/catalog/model internal/application/controlplane internal/application/gateway/snapshot internal/infrastructure/persistence/repository internal/infrastructure/snapshot internal/interfaces/http/handler`

Run: `go test -count=1 ./...`

Expected: PASS，仓库不再依赖迁移兼容 API。

- [ ] **Step 6: 提交兼容清理**

```bash
git add internal/domain/catalog/model internal/application/controlplane internal/application/gateway/snapshot internal/infrastructure/persistence/repository internal/infrastructure/snapshot internal/interfaces/http/handler
git commit -m "refactor: remove legacy deployment bindings"
```

---

### Task 7: 扩展 Gateway Invocation 与 Connector 错误契约

**Files:**
- Modify: `internal/application/gateway/port/connector.go`
- Create: `internal/application/gateway/port/errors.go`
- Create: `internal/application/gateway/port/errors_test.go`
- Modify: `internal/application/gateway/service/gateway.go`
- Modify: `internal/application/gateway/service/gateway_test.go`
- Modify: `internal/infrastructure/connector/fake/connector.go`
- Modify: `internal/infrastructure/connector/fake/connector_test.go`
- Modify: `internal/application/gateway/snapshot/model.go`
- Modify: `internal/application/gateway/snapshot/compiler.go`
- Modify: `internal/application/gateway/snapshot/compiler_test.go`

- [ ] **Step 1: 写同一 Session 与无明文 Invocation 失败测试**

测试 Registry 捕获 Invocation，断言包含 Provider、Deployment、加密 Credential，不含客户端 Virtual Key 字段；Fake Invocation 的 Credential 为 nil。

```go
func TestGatewayPreparesProviderAndScopedCredential(t *testing.T) {
	fixture := newGatewayFixture(t, catalogmodel.CapabilitySet{Text: true})
	fixture.configureOpenAIProvider(t, catalogmodel.Scope{Kind: catalogmodel.ScopePlatform})
	_, err := fixture.gateway.Complete(context.Background(), fixture.virtualKey, validRequest())
	if err != nil { t.Fatal(err) }
	if fixture.connector.invocation.Provider.BaseURL != "https://api.openai.com" || fixture.connector.invocation.Credential == nil {
		t.Fatalf("invocation=%+v", fixture.connector.invocation)
	}
	encoded, _ := json.Marshal(fixture.connector.invocation)
	if bytes.Contains(encoded, []byte(fixture.virtualKey)) { t.Fatal("Virtual Key leaked") }
}
```

在现有 `gatewayFixture` 增加 `source gatewaysnapshot.SourceConfig` 字段，`newGatewayFixture` 构造返回值时保存其已编译的 `source`。新增以下 helper；它复用 fixture 的租户、VirtualKey、Alias 和 Target，不创建第二套 Gateway 测试框架：

```go
func (f *gatewayFixture) configureOpenAIProvider(t *testing.T, scope catalogmodel.Scope) {
	t.Helper()
	provider, err := catalogmodel.NewProvider("OpenAI", catalogmodel.ConnectorOpenAI, "https://api.openai.com")
	if err != nil { t.Fatal(err) }
	credential, err := catalogmodel.NewProviderCredential(provider.ID, scope, testGatewaySealedCredential())
	if err != nil { t.Fatal(err) }
	deployment, err := catalogmodel.NewDeployment(provider.ID, "GPT-5", "gpt-5", catalogmodel.UpstreamResponses, scope, catalogmodel.CapabilitySet{Text: true})
	if err != nil { t.Fatal(err) }
	f.source.Providers = []catalogmodel.Provider{*provider}
	f.source.Credentials = []catalogmodel.ProviderCredential{*credential}
	f.source.Deployments = []catalogmodel.Deployment{*deployment}
	f.source.RouteTargets[0].DeploymentID = deployment.ID
	f.source.Revision++
	compiled, err := gatewaysnapshot.NewCompiler().Compile(f.source, time.Now().UTC())
	if err != nil { t.Fatal(err) }
	f.store.store.Publish(compiled)
	f.deploymentID = deployment.ID
	f.gateway = New(f.store, registry{catalogmodel.ConnectorOpenAI: f.connector}, gatewaysnapshot.NewCredentialSelector())
}
```

同文件新增 `testGatewaySealedCredential() catalogmodel.SealedCredential`，返回五个字段都非空的测试 envelope。`newGatewayFixture` 自本任务起也向 `New` 传入 `gatewaysnapshot.NewCredentialSelector()`。

- [ ] **Step 2: 写 ParameterUnsupported 映射失败测试**

Connector 返回 `ConnectorError{Kind: ParameterUnsupported, Param:"stop"}` 时，Gateway 必须返回 `CapabilityUnsupported`；Authentication、RateLimited、Timeout、Unavailable、InvalidResponse 都映射 ConnectorFailed。

- [ ] **Step 3: 运行 Gateway 测试并确认失败**

Run: `go test -count=1 ./internal/application/gateway/... ./internal/infrastructure/connector/fake`

Expected: FAIL，Invocation 和 New 构造器仍为旧结构。

- [ ] **Step 4: 定义 Invocation 和 ConnectorError**

```go
type Invocation struct {
	Request    inference.Request
	Access     gatewaysnapshot.AccessContext
	Provider   gatewaysnapshot.Provider
	Deployment gatewaysnapshot.Deployment
	Credential *gatewaysnapshot.CredentialEnvelope
	Revision   int64
}

type ConnectorErrorKind string

const (
	ParameterUnsupported ConnectorErrorKind = "parameter_unsupported"
	UpstreamAuthentication ConnectorErrorKind = "authentication"
	UpstreamRateLimited ConnectorErrorKind = "rate_limited"
	UpstreamTimeout ConnectorErrorKind = "timeout"
	UpstreamUnavailable ConnectorErrorKind = "unavailable"
	UpstreamRequestRejected ConnectorErrorKind = "request_rejected"
	UpstreamInvalidResponse ConnectorErrorKind = "invalid_response"
	CredentialUnavailable ConnectorErrorKind = "credential_unavailable"
)
```

ConnectorError 的 `Error()` 只返回安全消息，`Unwrap()` 返回 Cause；不把 URL、Body 或 Header拼入错误文本。

- [ ] **Step 5: 修改 Gateway prepare 和错误映射**

`New` 增加 `*snapshot.CredentialSelector`。prepare 在同一 Session 中依次 Authenticate、Resolve、Provider、Select Credential；Fake 跳过凭据。Registry 使用 `Provider.ConnectorType`。

`ErrProviderUnavailable` 视为内部快照错误；非 Fake 的 `ErrCredentialUnavailable` 在调用 Connector 前映射为 `ConnectorFailed`（HTTP 502，安全消息“供应商凭据不可用”），不回退到较低层之外的新池、不重试、不继续使用旧 Credential。

在同一步从 Snapshot Deployment 删除 Task 5 的迁移兼容 `ConnectorType` 和 `Credential`，`cloneRoutePlan` 不再处理固定 Credential；Compiler 只发布最终 Deployment 字段。

```go
if connectorError := new(gatewayport.ConnectorError); errors.As(err, &connectorError) && connectorError.Kind == gatewayport.ParameterUnsupported {
	return NewError(CapabilityUnsupported, "当前模型不支持请求参数", connectorError.Param, err)
}
return NewError(ConnectorFailed, "供应商请求失败", "", err)
```

Complete、Stream 创建和 Stream Recv 都使用同一映射函数。

- [ ] **Step 6: 更新 Fake Connector 并运行 Race 测试**

Fake 只验证 `invocation.Provider.ConnectorType == fake`，不读取 Credential。

Run: `gofmt -w internal/application/gateway internal/infrastructure/connector/fake && go test -race -count=1 ./internal/application/gateway/... ./internal/infrastructure/connector/fake`

Expected: PASS。

Run: `go test -count=1 ./...`

Expected: PASS。

- [ ] **Step 7: 提交 Gateway 契约**

```bash
git add internal/application/gateway internal/infrastructure/connector/fake
git commit -m "feat: prepare provider connector invocations"
```

---

### Task 8: 增加上游 Transport 配置与 OTel 脱敏

**Files:**
- Modify: `internal/infrastructure/config/config.go`
- Modify: `internal/infrastructure/config/env.go`
- Modify: `internal/infrastructure/config/config_test.go`
- Modify: `config.yaml`
- Modify: `.env.example`
- Modify: `internal/infrastructure/observability/http.go`
- Modify: `internal/infrastructure/observability/http_test.go`

- [ ] **Step 1: 写配置默认值和环境变量失败测试**

```go
func TestGatewayUpstreamDefaults(t *testing.T) {
	isolateConfigEnvironment(t)
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil { t.Fatal(err) }
	upstream := cfg.Gateway.Upstream
	if upstream.ConnectTimeout != 10*time.Second || upstream.CompleteTimeout != 5*time.Minute || upstream.StreamIdleTimeout != 5*time.Minute {
		t.Fatalf("upstream=%+v", upstream)
	}
}
```

环境变量测试覆盖六个时长/连接数字段的合法值和非法值启动失败。

- [ ] **Step 2: 写固定 Provider/Endpoint OTel URL 失败测试**

```go
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestTransportForHidesProviderBaseURL(t *testing.T) {
	runtime, spans, _ := newHTTPTestRuntime(t)
	transport := runtime.TransportFor("openai_compatible", "responses", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://private.internal/prefix/v1/responses" { t.Fatalf("url=%s", r.URL) }
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
	}))
	ctx, parent := runtime.tracerProvider.Tracer("test").Start(context.Background(), "parent")
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://private.internal/prefix/v1/responses", nil)
	response, err := transport.RoundTrip(request)
	if err != nil { t.Fatal(err) }
	_ = response.Body.Close()
	parent.End()
	ended := spans.Ended()
	if len(ended) < 2 { t.Fatalf("ended spans=%d", len(ended)) }
	upstreamSpan := ended[len(ended)-2]
	assertSpanDoesNotContain(t, upstreamSpan, "private.internal", "/prefix", "gpt-5")
	var attributes strings.Builder
	for _, attr := range upstreamSpan.Attributes() { attributes.WriteString(attr.Value.Emit()) }
	if !strings.Contains(attributes.String(), "/openai_compatible/responses") {
		t.Fatalf("static upstream route missing: %s", attributes.String())
	}
}
```

- [ ] **Step 3: 运行配置与 OTel 测试并确认失败**

Run: `go test -count=1 ./internal/infrastructure/config ./internal/infrastructure/observability`

Expected: FAIL，新配置和 `TransportFor` 不存在。

- [ ] **Step 4: 实现 UpstreamTransportConfig**

```go
type UpstreamTransportConfig struct {
	ConnectTimeout            time.Duration `mapstructure:"connect_timeout"`
	TLSHandshakeTimeout       time.Duration `mapstructure:"tls_handshake_timeout"`
	ResponseHeaderTimeout     time.Duration `mapstructure:"response_header_timeout"`
	CompleteTimeout           time.Duration `mapstructure:"complete_timeout"`
	StreamIdleTimeout         time.Duration `mapstructure:"stream_idle_timeout"`
	IdleConnectionTimeout     time.Duration `mapstructure:"idle_connection_timeout"`
	MaxIdleConnections       int           `mapstructure:"max_idle_connections"`
	MaxIdleConnectionsPerHost int           `mapstructure:"max_idle_connections_per_host"`
}
```

把它放入 `GatewayConfig.Upstream`，增加 `LLM_PROXY_GATEWAY_UPSTREAM_*` 环境变量、默认值和严格校验，并同步 YAML 与 `.env.example`。

- [ ] **Step 5: 实现不影响透明代理的 TransportFor**

保留现有 `Runtime.Transport`。新增：

```go
func (r *Runtime) TransportFor(provider, endpoint string, base http.RoundTripper) http.RoundTripper {
	provider = normalizeGatewayProvider(provider)
	endpoint = normalizeGatewayEndpoint(endpoint)
	return fixedRouteTransport{provider: provider, endpoint: endpoint, next: r.Transport(base)}
}
```

fixedRouteTransport 在调用现有 sanitizingTransport 前写入私有 routeInfo Context；只允许 `openai`、`openai_compatible` 和 `responses`、`chat.completions`，非法值归一化为 unknown/other。

- [ ] **Step 6: 运行配置、OTel 和透明代理回归测试**

Run: `gofmt -w internal/infrastructure/config internal/infrastructure/observability`

Run: `go test -race -count=1 ./internal/infrastructure/config ./internal/infrastructure/observability ./internal/infrastructure/proxy/...`

Expected: PASS，现有透明代理 Span Parent 与 URL 测试不变。

- [ ] **Step 7: 提交配置与遥测**

```bash
git add internal/infrastructure/config internal/infrastructure/observability config.yaml .env.example
git commit -m "feat: configure secure upstream transports"
```

---

### Task 9: 建立 OpenAI Connector 安全基础

**Files:**
- Create: `internal/infrastructure/connector/openai/connector.go`
- Create: `internal/infrastructure/connector/openai/credential.go`
- Create: `internal/infrastructure/connector/openai/client.go`
- Create: `internal/infrastructure/connector/openai/errors.go`
- Create: `internal/infrastructure/connector/openai/sse.go`
- Create: `internal/infrastructure/connector/openai/connector_test.go`
- Create: `internal/infrastructure/connector/openai/credential_test.go`
- Create: `internal/infrastructure/connector/openai/client_test.go`
- Create: `internal/infrastructure/connector/openai/sse_test.go`

- [ ] **Step 1: 写凭据、URL 和 Header 安全失败测试**

覆盖：严格 `api_key` JSON、未知字段、空 Key、解密失败、URL 前缀安全拼接、客户端 Authorization/Host 无法覆盖、Redirect 禁止、Body 全路径关闭。

```go
func TestBuildRequestUsesOnlyDecryptedProviderKey(t *testing.T) {
	provider := snapshot.Provider{BaseURL: "https://example.invalid/prefix"}
	key := []byte("upstream-secret")
	request, err := newUpstreamRequest(context.Background(), provider, "chat/completions", []byte(`{}`), key, false)
	if err != nil { t.Fatal(err) }
	if request.URL.String() != "https://example.invalid/prefix/v1/chat/completions" { t.Fatalf("url=%s", request.URL) }
	if request.Host != "example.invalid" || request.Header.Get("Authorization") != "Bearer upstream-secret" {
		t.Fatalf("host=%q authorization=%q", request.Host, request.Header.Get("Authorization"))
	}
	for _, forbidden := range []string{"X-Client-Secret", "X-Api-Key", "Cookie"} {
		if request.Header.Get(forbidden) != "" { t.Fatalf("header %s leaked", forbidden) }
	}
}
```

- [ ] **Step 2: 写 SSE 大小、CRLF、分块和空闲超时失败测试**

SSE Reader 测试单个 event 2 MiB 上限、CRLF、多 data 行、任意分块、EOF 尾事件和 StreamIdleTimeout。

- [ ] **Step 3: 运行新包测试并确认失败**

Run: `go test -count=1 ./internal/infrastructure/connector/openai`

Expected: FAIL，包尚不存在。

- [ ] **Step 4: 定义 Connector Options 和 CredentialOpener**

```go
type CredentialOpener interface {
	Open(context.Context, uuid.UUID, uuid.UUID, catalogmodel.Scope, catalogmodel.SealedCredential) ([]byte, error)
}

type Options struct {
	ConnectorType  string
	ResponsesClient *http.Client
	ChatClient       *http.Client
	CredentialOpener CredentialOpener
	CompleteTimeout  time.Duration
	StreamIdleTimeout time.Duration
	IDGenerator      func() (uuid.UUID, error)
	Clock            func() time.Time
}
```

New 校验 ConnectorType 只能为 OpenAI/Compatible，所有依赖非 nil，时间为正。

- [ ] **Step 5: 实现严格凭据解析和短生命周期清理**

```go
func openAPIKey(ctx context.Context, opener CredentialOpener, envelope snapshot.CredentialEnvelope) ([]byte, error) {
	plaintext, err := opener.Open(ctx, envelope.CredentialID, envelope.ProviderID, envelope.Scope, envelope.Sealed)
	if err != nil { return nil, connectorError(gatewayport.CredentialUnavailable, err) }
	defer clear(plaintext)
	var value struct { APIKey string `json:"api_key"` }
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decodeSingleJSON(decoder, &value); err != nil || strings.TrimSpace(value.APIKey) == "" {
		return nil, connectorError(gatewayport.CredentialUnavailable, err)
	}
	key := append([]byte(nil), value.APIKey...)
	value.APIKey = "" // 尽力缩短 string 引用生命周期；Go 不保证字符串底层内存可原地清零。
	return key, nil
}
```

调用方使用完 Key byte slice 后 `clear`；禁止转成会被长期保存的结构字段。

- [ ] **Step 6: 实现 HTTP Client 和 SSE Reader**

实现 `newUpstreamRequest(ctx context.Context, provider snapshot.Provider, endpoint string, body, apiKey []byte, stream bool) (*http.Request, error)`；BaseURL 使用 `url.JoinPath(base, "v1", endpoint)`。新建 Request，只设置 Content-Type、Accept、Authorization；不复制客户端 Header。普通响应 LimitReader 16 MiB，错误体 64 KiB。Redirect 使用 `CheckRedirect` 返回 `http.ErrUseLastResponse` 后分类为 unavailable，不跟随 Location。

SSE Reader 使用 `bufio.Reader` 和累计 event 字节计数，不使用 Scanner 默认 64 KiB 限制；空闲 Timer 每次读取字节后重置，超时取消并返回 UpstreamTimeout。

- [ ] **Step 7: 运行安全基础 Race 测试**

Run: `gofmt -w internal/infrastructure/connector/openai && go test -race -count=1 ./internal/infrastructure/connector/openai`

Expected: PASS。

- [ ] **Step 8: 提交 Connector 基础**

```bash
git add internal/infrastructure/connector/openai
git commit -m "feat: add secure openai connector transport"
```

---

### Task 10: 实现 Chat Completions 上游 Adapter

**Files:**
- Create: `internal/infrastructure/connector/openai/chat_request.go`
- Create: `internal/infrastructure/connector/openai/chat_response.go`
- Create: `internal/infrastructure/connector/openai/chat_stream.go`
- Create: `internal/infrastructure/connector/openai/chat_test.go`
- Create: `internal/infrastructure/connector/openai/testdata/chat_text_response.json`
- Create: `internal/infrastructure/connector/openai/testdata/chat_tool_stream.sse`

- [ ] **Step 1: 写 Chat 请求 Golden 失败测试**

表驱动覆盖文本、图片 URL/Base64、system/developer、多个 ToolCall/ToolResult、四种 ToolChoice、JSON Schema、Temperature 0、TopP、Stop、Stream 和两个 Connector Flavor。

```go
func TestEncodeChatTokenLimitByConnectorType(t *testing.T) {
	invocation := fullInvocation(t)
	invocation.Request.MaxTokens = inference.Some[int64](12)
	invocation.Provider.ConnectorType = catalogmodel.ConnectorOpenAI
	officialBody, err := encodeChatRequest(invocation)
	if err != nil { t.Fatal(err) }
	official := decodeObject(t, officialBody)
	invocation.Provider.ConnectorType = catalogmodel.ConnectorOpenAICompatible
	compatibleBody, err := encodeChatRequest(invocation)
	if err != nil { t.Fatal(err) }
	compatible := decodeObject(t, compatibleBody)
	assertJSONNumber(t, official, "max_completion_tokens", 12)
	assertAbsent(t, official, "max_tokens")
	assertJSONNumber(t, compatible, "max_tokens", 12)
	assertAbsent(t, compatible, "max_completion_tokens")
}
```

在 `chat_test.go` 定义并由 Responses 测试复用以下包内 helper：

```go
func fullInvocation(t *testing.T) gatewayport.Invocation {
	t.Helper()
	providerID := uuid.Must(uuid.NewV7())
	return gatewayport.Invocation{
		Request: inference.Request{Model: "assistant", Messages: []inference.Message{{Role: inference.RoleUser, Content: []inference.ContentBlock{{Type: inference.ContentText, Text: &inference.TextContent{Text: "hello"}}}}}},
		Provider: snapshot.Provider{ID: providerID, ConnectorType: catalogmodel.ConnectorOpenAI, BaseURL: "https://api.openai.com"},
		Deployment: snapshot.Deployment{ID: uuid.Must(uuid.NewV7()), ProviderID: providerID, UpstreamModel: "gpt-5", UpstreamProtocol: catalogmodel.UpstreamChatCompletions, Capabilities: catalogmodel.CapabilitySet{Text: true, Streaming: true}},
		Revision: 1,
	}
}

func decodeObject(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil { t.Fatal(err) }
	return result
}

func assertAbsent(t *testing.T, value map[string]any, key string) {
	t.Helper()
	if _, exists := value[key]; exists { t.Fatalf("field %s unexpectedly exists", key) }
}

func assertJSONNumber(t *testing.T, value map[string]any, key string, want int64) {
	t.Helper()
	got, ok := value[key].(json.Number)
	if !ok || got.String() != strconv.FormatInt(want, 10) { t.Fatalf("%s=%v", key, value[key]) }
}
```

MaxTokens 0 必须返回 ParameterUnsupported。

- [ ] **Step 2: 写 Chat 普通响应和 SSE 失败测试**

断言：本地 UUIDv7、ModelAlias、文本、Refusal、多个 ToolCall、Usage cache、finish_reason；SSE 按 index 累计 arguments，usage 最终一致，首事件提前可读，`[DONE]` 后 EOF。

- [ ] **Step 3: 运行 Chat 测试并确认失败**

Run: `go test -count=1 ./internal/infrastructure/connector/openai -run 'Test.*Chat'`

Expected: FAIL，Chat Adapter 函数不存在。

- [ ] **Step 4: 实现 Chat 请求映射**

定义私有 DTO，并实现：

```go
func encodeChatRequest(invocation gatewayport.Invocation) ([]byte, error)
```

固定映射：Role→role、Text/Image→content parts、ToolCall→assistant.tool_calls、ToolResult→tool message、Tool→function、StructuredOutput→response_format、Stop→stop、Stream→stream。Official 使用 `max_completion_tokens`，Compatible 使用 `max_tokens`。

- [ ] **Step 5: 实现 Chat 普通响应映射**

```go
func decodeChatResponse(reader io.Reader, invocation gatewayport.Invocation, id func() (uuid.UUID, error), clock func() time.Time) (inference.Response, error)
```

只接受 choice index 0 且恰好一个 Choice。上游 ID 不进入统一 ID；Model 使用 `invocation.Request.Model`。`finish_reason` 映射 stop/length/tool_calls/content_filter，未知值返回 InvalidResponse。Usage 校验非负且 total 与 input+output 一致。

- [ ] **Step 6: 实现 Chat 流式状态机**

`chatStream` 持有 SSE Reader、统一 Sequence 所需状态、上游 choice index→内容块 index、ToolCall index→ID/Name/Arguments。每次 Recv 最多返回一个统一 Event；一个上游 Chunk 产生多个事件时放入短队列。`[DONE]` 之前必须生成 ResponseFinish。

- [ ] **Step 7: 运行 Chat Contract Race 测试**

Run: `gofmt -w internal/infrastructure/connector/openai && go test -race -count=1 ./internal/infrastructure/connector/openai -run 'Test.*Chat'`

Expected: PASS。

- [ ] **Step 8: 提交 Chat Adapter**

```bash
git add internal/infrastructure/connector/openai
git commit -m "feat: adapt chat completions upstream"
```

---

### Task 11: 实现 Responses 上游 Adapter

**Files:**
- Create: `internal/infrastructure/connector/openai/responses_request.go`
- Create: `internal/infrastructure/connector/openai/responses_response.go`
- Create: `internal/infrastructure/connector/openai/responses_stream.go`
- Create: `internal/infrastructure/connector/openai/responses_test.go`
- Create: `internal/infrastructure/connector/openai/testdata/responses_text_response.json`
- Create: `internal/infrastructure/connector/openai/testdata/responses_tool_stream.sse`

- [ ] **Step 1: 写 Responses 请求 Golden 失败测试**

覆盖 Input Message、图片、Function Call、Function Call Output、Function Tool、ToolChoice、`text.format`、`max_output_tokens`、Temperature 0、TopP、Stream、`store:false`。

```go
func TestEncodeResponsesForcesStatelessCalls(t *testing.T) {
	invocation := fullInvocation(t)
	invocation.Deployment.UpstreamProtocol = catalogmodel.UpstreamResponses
	body, err := encodeResponsesRequest(invocation)
	if err != nil { t.Fatal(err) }
	payload := decodeObject(t, body)
	if payload["store"] != false { t.Fatalf("store=%v", payload["store"]) }
}

func TestResponsesRejectsStopAndZeroMaxTokens(t *testing.T) {
	invocation := fullInvocation(t)
	invocation.Deployment.UpstreamProtocol = catalogmodel.UpstreamResponses
	invocation.Request.Stop = inference.Some([]string{"END"})
	_, err := encodeResponsesRequest(invocation)
	assertConnectorErrorKind(t, err, gatewayport.ParameterUnsupported, "stop")
	invocation.Request.Stop = inference.Optional[[]string]{}
	invocation.Request.MaxTokens = inference.Some[int64](0)
	_, err = encodeResponsesRequest(invocation)
	assertConnectorErrorKind(t, err, gatewayport.ParameterUnsupported, "max_tokens")
}
```

在 `responses_test.go` 定义：

```go
func assertConnectorErrorKind(t *testing.T, err error, kind gatewayport.ConnectorErrorKind, param string) {
	t.Helper()
	var connectorErr *gatewayport.ConnectorError
	if !errors.As(err, &connectorErr) || connectorErr.Kind != kind || connectorErr.Param != param {
		t.Fatalf("error=%v kind=%q param=%q", err, kind, param)
	}
}
```

- [ ] **Step 2: 写 Responses 普通响应和命名 SSE 失败测试**

覆盖 output message/output_text、refusal、function_call、usage、completed/incomplete/content_filter；流事件覆盖 response.created、output_item.added、content_part.added、output_text.delta、refusal.delta、function_call_arguments.delta、output_item.done、response.completed 和 response.failed。

- [ ] **Step 3: 运行 Responses 测试并确认失败**

Run: `go test -count=1 ./internal/infrastructure/connector/openai -run 'Test.*Responses'`

Expected: FAIL，Responses Adapter 尚不存在。

- [ ] **Step 4: 实现 Responses 请求映射**

```go
func encodeResponsesRequest(invocation gatewayport.Invocation) ([]byte, error)
```

只生成 Function Tool 和受支持 Input Item；instructions 映射系统/开发者指令；结构化输出映射 `text.format`；固定 `store=false`。无法表达的 Stop、零 MaxTokens 和内建工具请求返回 ParameterUnsupported，不发送请求。

- [ ] **Step 5: 实现 Responses 普通响应映射**

```go
func decodeResponsesResponse(reader io.Reader, invocation gatewayport.Invocation, id func() (uuid.UUID, error), clock func() time.Time) (inference.Response, error)
```

遍历 output，只接受 message、function_call 和不对外暴露的 reasoning metadata；未知可见 Item 失败关闭。Refusal 映射统一 Refusal。status completed/incomplete 和 incomplete_details 映射 StopReason。Usage reasoning tokens 不重复叠加到 OutputTokens。

- [ ] **Step 6: 实现 Responses 流式状态机**

按 `sequence_number` 单调校验，按 output/content index 建立统一 block index。Tool `call_id` 作为统一 ToolCall ID；arguments delta 逐块累计并由统一 SequenceValidator 最终校验 JSON Object。response.failed 产生安全 StreamError。

- [ ] **Step 7: 运行 Responses Contract Race 测试**

Run: `gofmt -w internal/infrastructure/connector/openai && go test -race -count=1 ./internal/infrastructure/connector/openai -run 'Test.*Responses'`

Expected: PASS。

- [ ] **Step 8: 提交 Responses Adapter**

```bash
git add internal/infrastructure/connector/openai
git commit -m "feat: adapt responses api upstream"
```

---

### Task 12: 完成真实 Connector 编排和错误分类

**Files:**
- Modify: `internal/infrastructure/connector/openai/connector.go`
- Modify: `internal/infrastructure/connector/openai/client.go`
- Modify: `internal/infrastructure/connector/openai/errors.go`
- Modify: `internal/infrastructure/connector/openai/connector_test.go`
- Create: `internal/infrastructure/connector/openai/integration_test.go`

- [ ] **Step 1: 写四组合和错误矩阵失败测试**

四组合为 OpenAI/Compatible × Responses/Chat。错误矩阵覆盖 400、401、403、429、5xx、DNS、TLS、Deadline、取消、JSON 超限、SSE 超限、未知事件、断流。

```go
func TestConnectorSelectsExplicitDeploymentProtocol(t *testing.T) {
	for _, protocol := range []catalogmodel.UpstreamProtocol{catalogmodel.UpstreamResponses, catalogmodel.UpstreamChatCompletions} {
		t.Run(string(protocol), func(t *testing.T) {
			upstream := protocolServer(t, protocol)
			connector := connectorForServer(t, catalogmodel.ConnectorOpenAICompatible, upstream)
			invocation := invocationForProtocol(t, protocol)
			if _, err := connector.Complete(context.Background(), invocation); err != nil { t.Fatal(err) }
			if upstream.RequestCount() != 1 { t.Fatalf("count=%d", upstream.RequestCount()) }
		})
	}
}
```

在 `integration_test.go` 同时定义这三个 helper，签名固定为：

```go
type protocolUpstream struct {
	*httptest.Server
	requests atomic.Int64
}

func (s *protocolUpstream) RequestCount() int64 { return s.requests.Load() }
func protocolServer(t *testing.T, protocol catalogmodel.UpstreamProtocol) *protocolUpstream
func connectorForServer(t *testing.T, connectorType string, upstream *protocolUpstream) *Connector
func invocationForProtocol(t *testing.T, protocol catalogmodel.UpstreamProtocol) gatewayport.Invocation
```

`protocolServer` 必须按 protocol 只挂载一个精确路径并返回 Task 10/11 testdata 中的合法普通响应，每次请求先 `requests.Add(1)`；`connectorForServer` 使用测试 Cipher Opener、真实 `http.Client` 和 upstream URL 构造 Connector；`invocationForProtocol` 复用 `fullInvocation` 并设置 Deployment 协议及一个非空测试 CredentialEnvelope。所有资源通过 `t.Cleanup` 关闭。

断言没有第二次请求，证明无协议回退和重试。

- [ ] **Step 2: 运行 Connector 全包测试并确认失败**

Run: `go test -count=1 ./internal/infrastructure/connector/openai`

Expected: FAIL，Connector 编排尚未调用两个 Adapter。

- [ ] **Step 3: 实现 Complete 与 Stream**

```go
func (c *Connector) Complete(ctx context.Context, invocation gatewayport.Invocation) (inference.Response, error) {
	if err := c.validateInvocation(invocation); err != nil { return inference.Response{}, err }
	body, endpoint, client, err := c.encode(invocation)
	if err != nil { return inference.Response{}, err }
	callCtx, cancel := withEarlierTimeout(ctx, c.options.CompleteTimeout)
	defer cancel()
	responseBody, err := c.do(callCtx, client, invocation, body, endpoint, false)
	if err != nil { return inference.Response{}, err }
	defer responseBody.Close()
	return c.decode(responseBody, invocation)
}
```

Stream 同样显式 switch UpstreamProtocol，返回实现 `inferenceport.Stream` 的状态机；Close 必须并发幂等并取消上游 Context。

- [ ] **Step 4: 实现上游错误分类**

固定分类：401/403→Authentication，429→RateLimited，400/404/409/422→RequestRejected，5xx/网络/TLS→Unavailable，Deadline→Timeout，解析/大小/序列→InvalidResponse。SafeMessage 固定中文，Cause 保留但不含读取到的原始错误体。

- [ ] **Step 5: 运行 Connector 普通与 Race 测试**

Run: `gofmt -w internal/infrastructure/connector/openai && go test -race -count=1 ./internal/infrastructure/connector/openai`

Expected: PASS，无数据竞争、无真实网络。

- [ ] **Step 6: 提交 Connector 编排**

```bash
git add internal/infrastructure/connector/openai
git commit -m "feat: invoke openai provider connectors"
```

---

### Task 13: 新增 OpenAI Responses 客户端协议适配器

**Files:**
- Create: `internal/interfaces/http/protocol/openai/responses/request.go`
- Create: `internal/interfaces/http/protocol/openai/responses/decode.go`
- Create: `internal/interfaces/http/protocol/openai/responses/response.go`
- Create: `internal/interfaces/http/protocol/openai/responses/encode.go`
- Create: `internal/interfaces/http/protocol/openai/responses/protocol_test.go`
- Create: `internal/interfaces/http/protocol/openai/responses/testdata/text_request.json`
- Create: `internal/interfaces/http/protocol/openai/responses/testdata/tool_request.json`
- Create: `internal/interfaces/http/protocol/openai/responses/testdata/stream.golden`

- [ ] **Step 1: 写 Responses Create Decoder 失败测试**

测试支持字段：model、字符串/Item Array input、instructions、max_output_tokens、temperature、top_p、Function tools、tool_choice、text.format、stream。测试拒绝：未知字段、内建工具、file/audio、previous_response_id、conversation、background、store=true 和 4 MiB 超限。

```go
func TestDecodeResponsesStringInput(t *testing.T) {
	decoded, err := Decode(strings.NewReader(`{"model":"assistant","input":"hello","stream":true}`))
	if err != nil { t.Fatal(err) }
	if decoded.Request.Model != "assistant" || !decoded.Request.Stream || decoded.Request.Messages[0].Content[0].Text.Text != "hello" {
		t.Fatalf("decoded=%+v", decoded)
	}
}
```

- [ ] **Step 2: 写普通响应与命名 SSE Golden 失败测试**

覆盖 output_text、refusal、function_call、Usage、completed/incomplete，以及 response.created、output_item.added、content_part.added、delta、done、response.completed 的 sequence_number 和 Flush。

- [ ] **Step 3: 运行新协议包测试并确认失败**

Run: `go test -count=1 ./internal/interfaces/http/protocol/openai/responses`

Expected: FAIL，包尚不存在。

- [ ] **Step 4: 实现严格 Decoder**

使用 4 MiB LimitReader 和 `json.Decoder.DisallowUnknownFields`。instructions 转为首个 Developer 消息；Function Call/Output 映射现有 ToolCall/ToolResult 并交由 `Request.Validate` 校验工具图。

```go
type DecodedRequest struct { Request inference.Request }

func Decode(reader io.Reader) (DecodedRequest, error)
```

只允许 Function Tool；所有不支持字段返回 `gatewayservice.InvalidRequest` 和精确 Param。

- [ ] **Step 5: 实现普通 Response Encoder**

Response ID 使用 `resp_` 加 UUIDv7 的无连字符十六进制文本；同时生成 object=response、created_at、status、model alias、output items、usage。每个 Message/FunctionCall Item 生成本地 UUIDv7 派生 ID；ToolCall ID 作为 `call_id` 保留。Refusal 输出 refusal part。

- [ ] **Step 6: 实现命名 SSE Encoder**

`EncodeStream(ctx, FlushWriter, Stream)` 按统一 Event 维护 output index、content index、item ID 和 sequence_number。每帧固定：

```text
event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_0198dd23236d7b6c8c10fcd0fe1a3c11","output_index":0,"content_index":0,"delta":"你"}

```

首个 ResponseStart 立即写 response.created 并 Flush；StreamError 写 response.failed；正常 Finish 写 response.completed 后返回。

- [ ] **Step 7: 运行协议 Race 和 Golden 测试**

Run: `gofmt -w internal/interfaces/http/protocol/openai/responses && go test -race -count=1 ./internal/interfaces/http/protocol/openai/responses`

Expected: PASS。

- [ ] **Step 8: 提交 Responses 入口协议**

```bash
git add internal/interfaces/http/protocol/openai/responses
git commit -m "feat: add openai responses protocol adapter"
```

---

### Task 14: 注册 Responses Handler 与路由

**Files:**
- Modify: `internal/interfaces/http/handler/gateway/dependencies.go`
- Create: `internal/interfaces/http/handler/gateway/responses.go`
- Modify: `internal/interfaces/http/handler/gateway/handler_test.go`
- Modify: `internal/interfaces/http/router/router.go`
- Modify: `internal/interfaces/http/router/router_test.go`
- Modify: `internal/infrastructure/observability/http.go`
- Modify: `internal/infrastructure/observability/http_test.go`

- [ ] **Step 1: 写 Handler 与 Router 失败测试**

覆盖 POST/JSON/4 MiB、Bearer Virtual Key、普通响应、SSE 首帧 Flush、无 Flusher 500、断连取消、GET 405 Allow、Gateway nil 时路由不挂载。

```go
func TestRouterMountsResponsesWithoutChangingTransparentProxy(t *testing.T) {
	handler := New(Config{BaseURL: "http://localhost:8080", Version: "test"}, Dependencies{
		Logger: zap.NewNop(), Instrumenter: identityInstrumenter{}, Readiness: appRuntime.NewReadiness(),
		Stats: &dashboard.Stats{}, ObserverFactory: tokenusage.NewObserver,
		OpenAIProxy: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "transparent") }),
		AnthropicProxy: http.NotFoundHandler(),
		OpenAIResponsesGateway: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "responses") }),
	})
	for _, test := range []struct{ path, want string }{{"/v1/responses", "responses"}, {"/openai/v1/responses", "transparent"}} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(`{}`)))
		if recorder.Body.String() != test.want { t.Fatalf("POST %s body=%q", test.path, recorder.Body.String()) }
	}
}
```

- [ ] **Step 2: 运行 Handler/Router 测试并确认失败**

Run: `go test -count=1 ./internal/interfaces/http/handler/gateway ./internal/interfaces/http/router`

Expected: FAIL，Responses Handler 和依赖字段不存在。

- [ ] **Step 3: 实现 Responses Handler**

复制现有 Gateway 安全骨架但调用 Responses Decoder/Encoder：

```go
type responsesHandler struct{ gateway Gateway }

func NewResponses(gateway Gateway) http.Handler { return &responsesHandler{gateway: gateway} }
```

认证只读 `Authorization: Bearer`；sanitizedRequest 清除 Authorization；流式先成功创建 Stream，再检查 Flusher、写 SSE Header、defer Close。

- [ ] **Step 4: 注册独立路由和日志 Endpoint**

`router.Dependencies` 增加 `OpenAIResponsesGateway`。注册 `POST /v1/responses` 和方法回退，GatewayLogging 使用 provider=`openai`、endpoint=`responses`；现有 `/v1/chat/completions` 不变。

Observability `normalizeEndpoint` 增加精确 `/v1/responses → responses`，不改变 `/openai/v1/responses` 透明代理规则。

- [ ] **Step 5: 运行 HTTP Race 与透明代理回归测试**

Run: `gofmt -w internal/interfaces/http/handler/gateway internal/interfaces/http/router internal/infrastructure/observability`

Run: `go test -race -count=1 ./internal/interfaces/http/handler/gateway ./internal/interfaces/http/router ./internal/infrastructure/observability ./internal/infrastructure/proxy/...`

Expected: PASS。

- [ ] **Step 6: 提交 Handler 与路由**

```bash
git add internal/interfaces/http/handler/gateway internal/interfaces/http/router internal/infrastructure/observability
git commit -m "feat: expose openai responses endpoint"
```

---

### Task 15: 组装 Cipher、真实 Connector 与 Wire

**Files:**
- Modify: `internal/di/provider/shared.go`
- Modify: `internal/di/provider/controlplane.go`
- Modify: `internal/di/provider/gateway.go`
- Modify: `internal/di/provider/gateway_test.go`
- Modify: `internal/di/provider/transparent.go`
- Modify: `internal/di/modules/app.go`
- Modify: `internal/di/wire_gen.go`
- Modify: `internal/di/provider/transparent_test.go`
- Modify: `internal/architecture/dependencies_test.go`

- [ ] **Step 1: 写 DI 失败测试**

断言启用 Gateway 时三个数据面 Handler 非 nil，Registry 有 fake/openai/openai_compatible；禁用时均 nil；OpenAI 与 Compatible 使用各自固定 OTel Provider，但共享底层连接池配置和同一 Credential Cipher。

- [ ] **Step 2: 运行 DI 和架构测试并确认失败**

Run: `go test -count=1 ./internal/di/provider ./internal/architecture`

Expected: FAIL，组装仍只有 Fake 和两个 Handler。

- [ ] **Step 3: 提供按 Gateway 开关构造的共享 Credential Cipher 与 Transport**

```go
type CredentialCipherRuntime struct {
	Cipher *credentialsecurity.Cipher
}

func NewCredentialCipherRuntime(cfg *config.Config) (*CredentialCipherRuntime, error) {
	if cfg == nil || !cfg.Gateway.Enabled {
		return &CredentialCipherRuntime{}, nil
	}
	cipher, err := credentialsecurity.NewCipher(cfg.CredentialEncryption.CurrentKeyVersion, cfg.CredentialEncryption.Keys)
	if err != nil {
		return nil, err
	}
	return &CredentialCipherRuntime{Cipher: cipher}, nil
}
```

ControlPlaneRuntime 和 Gateway Connector 改为接收同一个 `*CredentialCipherRuntime`，仅在 Gateway 启用分支读取其非 nil `Cipher`，不再内部重复创建。这样透明代理单独启用、Gateway 关闭且未配置加密密钥时仍可正常启动。构造一个带配置 Dialer/TLS/Idle 参数的共享 `*http.Transport`；四个 `TransportFor` Wrapper 共享它：OpenAI/Compatible × Responses/Chat。

- [ ] **Step 4: 注册三个 Connector 和三个 Handler**

```go
type UnifiedGatewayHandlers struct {
	OpenAIChat      http.Handler
	OpenAIResponses http.Handler
	Anthropic       http.Handler
}
```

Registry 注册 Fake、Official OpenAI Connector、Compatible Connector。Gateway 使用共享 Store 和 `snapshot.NewCredentialSelector()`。Root Router 分别注入 Chat、Responses、Anthropic。

- [ ] **Step 5: 更新 Wire 并检查生成差异**

Run: `go generate ./internal/di`

Expected: `internal/di/wire_gen.go` 成功更新，没有 duplicate provider 或 missing dependency。

- [ ] **Step 6: 运行 DI、架构与 Build**

Run: `gofmt -w internal/di internal/architecture && go test -race -count=1 ./internal/di/... ./internal/architecture`

Run: `go build -o /tmp/llm-proxy ./cmd/proxy`

Expected: PASS，生成二进制不加入 Git。

- [ ] **Step 7: 提交组装**

```bash
git add internal/di internal/architecture
git commit -m "feat: wire provider connectors"
```

---

### Task 16: 完成跨协议、撤销和安全纵向验收

**Files:**
- Modify: `internal/interfaces/http/handler/gateway/integration_test.go`
- Create: `internal/interfaces/http/handler/gateway/provider_integration_test.go`
- Modify: `internal/infrastructure/snapshot/integration_test.go`
- Modify: `internal/infrastructure/persistence/migration/migration_test.go`
- Modify: `cmd/proxy/main_test.go`

- [ ] **Step 1: 建立双协议上游测试服务器**

`provider_integration_test.go` 创建一个 `httptest.Server`，同时实现 `/v1/chat/completions` 和 `/v1/responses` 的普通/SSE 固定响应，并捕获 Authorization、Path、Body、traceparent 和请求次数。密钥使用测试专用非真实值。

- [ ] **Step 2: 写 3×2×2 纵向矩阵**

客户端入口三种：Chat、Responses、Anthropic；上游协议两种：Chat、Responses；调用模式两种：普通、流式。每个用例通过真实 Router、Gateway、Snapshot、Credential Cipher 和 Connector。

```go
for _, clientProtocol := range []string{"chat", "responses", "anthropic"} {
	for _, upstreamProtocol := range []catalogmodel.UpstreamProtocol{catalogmodel.UpstreamChatCompletions, catalogmodel.UpstreamResponses} {
		for _, stream := range []bool{false, true} {
			clientProtocol, upstreamProtocol, stream := clientProtocol, upstreamProtocol, stream
			t.Run(clientProtocol+"/"+string(upstreamProtocol)+"/"+strconv.FormatBool(stream), func(t *testing.T) {
				runProviderMatrixCase(t, clientProtocol, upstreamProtocol, stream)
			})
		}
	}
}
```

在同文件定义 `runProviderMatrixCase(t *testing.T, clientProtocol string, upstreamProtocol catalogmodel.UpstreamProtocol, stream bool)`：它创建测试上游、编译单个 OpenAI Provider/Platform Credential/Deployment/Alias 的 Snapshot、通过真实 Router 发起对应客户端协议请求，并执行本步骤列出的所有断言；每个子测试独立创建和关闭资源。

断言 ModelAlias、ToolCall ID、Usage、StopReason、首帧 Flush、只有一次上游请求。

- [ ] **Step 3: 写凭据优先级与撤销纵向测试**

依次创建 Platform、Organization、Project 三层凭据，验证 Project 命中；停用 Project 后命中 Organization；停用 Organization 后命中 Platform；停用最后一个 Platform 后等待更高 Revision，路由仍可解析但调用 502，捕获服务器不再收到旧 Key。

- [ ] **Step 4: 写敏感信息和 OTel 否定断言**

把 Virtual Key、上游 Key、私有 BaseURL、UpstreamModel 放入独立 canary 字符串。序列化 Invocation、捕获日志、Span、Metric、错误响应和 Snapshot 可见视图，逐一断言禁止位置不含 canary；上游仅收到上游 Key，不收到 Virtual Key。

- [ ] **Step 5: 写真实 PostgreSQL 迁移与零外键验收**

在设置 `LLM_PROXY_TEST_POSTGRES_DSN` 时运行完整迁移、控制面写入、快照刷新、撤销和恢复链；查询 `information_schema`/`pg_constraint` 确认零外键，查询列类型确认 UUID 与 `timestamp(0) without time zone`。DSN 测试连接必须包含 `TimeZone=Asia/Shanghai`。

- [ ] **Step 6: 运行纵向普通与 Race 测试**

Run: `go test -count=1 ./internal/interfaces/http/handler/gateway ./internal/infrastructure/snapshot ./internal/infrastructure/persistence/migration ./cmd/proxy`

Run: `go test -race -count=1 ./internal/interfaces/http/handler/gateway ./internal/infrastructure/snapshot`

Expected: PASS。配置了真实 PG 时纵向 PG 用例必须实际执行而非 Skip。

- [ ] **Step 7: 提交纵向验收**

```bash
git add internal/interfaces/http/handler/gateway internal/infrastructure/snapshot internal/infrastructure/persistence/migration cmd/proxy
git commit -m "test: verify provider connector vertical paths"
```

---

### Task 17: 全量验证与交付检查

**Files:**
- Verify only; only modify files when a failing check directly belongs to this feature.

- [ ] **Step 1: 格式和静态差异检查**

Run: `gofmt -w $(git diff --name-only origin/feat/unified-protocol-kernel...HEAD -- '*.go')`

Run: `git diff --check origin/feat/unified-protocol-kernel...HEAD`

Expected: 无输出、退出码 0。

- [ ] **Step 2: 运行目标安全与架构检查**

Run: `go test -race -count=1 ./internal/application/gateway/... ./internal/infrastructure/connector/... ./internal/interfaces/http/protocol/... ./internal/interfaces/http/handler/gateway ./internal/infrastructure/observability ./internal/architecture`

Expected: PASS。

- [ ] **Step 3: 运行全量测试、Race 和 Vet**

Run: `go test -count=1 ./...`

Run: `go test -race -count=1 ./...`

Run: `go vet ./...`

Expected: 全部 PASS。

- [ ] **Step 4: 验证 Wire 和构建**

Run: `go generate ./internal/di && git diff --exit-code -- internal/di/wire_gen.go`

Run: `go build -o /tmp/llm-proxy ./cmd/proxy`

Expected: Wire 无未提交差异，Build PASS。

- [ ] **Step 5: 检查安全、迁移和仓库状态**

Run: `if rg -n 'FOREIGN KEY|REFERENCES|constraint:|gorm:".*foreignKey|Preload\(|Association\(' internal/infrastructure/persistence -g '!**/*_test.go'; then exit 1; fi`

Expected: 无生产代码匹配，命令退出码 0；测试中的否定断言文本被排除。

Run: `git status --short --branch`

Expected: `feat/provider-connectors` 工作区干净，没有二进制、日志、真实凭据或无关文件。

- [ ] **Step 6: 如验证失败则回到归属任务修复**

不要在本步骤用通配符暂存。若 Step 1-5 产生代码差异，先回到最早失败的所属任务，重新运行该任务列出的目标测试，并使用该任务 Step “提交”中列出的精确目录暂存；提交消息使用该任务原消息。若 Step 1-5 没有产生差异，不创建空提交。
