# Phase 1B-1：PostgreSQL 控制面实施计划

> **供执行代理使用：** 必须使用 `superpowers:subagent-driven-development`（推荐）或 `superpowers:executing-plans`，逐任务执行本计划。所有步骤使用 `- [ ]` 复选框跟踪。

**目标：** 建立可降级连接的 PostgreSQL 数据层、DDD 仓储、UUIDv7 多租户控制面、Virtual Key 安全存储和 Provider 凭据信封加密，并通过资源化 HTTP API 管理配置。

**架构：** Domain 定义 Tenancy 与 Catalog 聚合及 Repository，Application 用事务端口编排写入与 ConfigRevision，Infrastructure 用 GORM/PostgreSQL、gormigrate 和 AES-256-GCM 实现端口，Interfaces 提供受独立管理令牌保护的 `/v1` 资源 API。数据库临时不可用只使控制面返回 503，不影响现有透明代理启动。

**技术栈：** Go 1.25、标准库 `net/http`、Google UUID、GORM、PostgreSQL Driver、gormigrate、AES-256-GCM、Viper、Google Wire、`httptest`。

**设计依据：** `docs/superpowers/specs/2026-08-25-unified-protocol-kernel-postgresql-design.md`

---

## 一、执行边界与最终文件结构

本计划是 Phase 1B 的第一个计划。完成后控制面可用，但 `/v1/chat/completions` 和 `/v1/messages` 尚未注册；随后依次执行运行时快照计划和双协议数据面计划。

执行前确认当前分支为 `feat/unified-protocol-kernel`。禁止创建或使用 worktree，不得切回长期分支直接实施。

最终新增目录职责：

```text
cmd/migrate/                                      # 严格连接数据库的迁移 CLI
internal/domain/shared/                           # UUIDv7、状态、事务端口
internal/domain/tenancy/{model,repository}/       # Organization、Project、VirtualKey
internal/domain/catalog/{model,repository}/       # Provider、Credential、Deployment、Alias、Target、Revision
internal/application/controlplane/{dto,service}/  # 控制面命令、查询和事务编排
internal/infrastructure/persistence/database/     # 可降级 Runtime 与严格 Open
internal/infrastructure/persistence/transactions/ # GORM 事务上下文
internal/infrastructure/persistence/entity/       # GORM Entity，与领域模型分离
internal/infrastructure/persistence/repository/   # Repository Adapter
internal/infrastructure/persistence/migration/    # gormigrate 固定版本迁移
internal/infrastructure/security/                  # 管理令牌、Virtual Key、凭据信封加密
internal/interfaces/http/handler/controlplane/    # 资源化控制面 HTTP Adapter
internal/interfaces/http/response/                # 控制面公共 JSON 响应
```

## 二、任务

### 任务 1：引入数据库依赖并扩展配置

**文件：**

- 修改：`go.mod`
- 修改：`go.sum`
- 修改：`internal/infrastructure/config/config.go`
- 修改：`internal/infrastructure/config/env.go`
- 修改：`internal/infrastructure/config/config_test.go`
- 修改：`config.yaml`
- 修改：`.env.example`

- [ ] **步骤 1：添加配置失败测试**

在 `internal/infrastructure/config/config_test.go` 增加表驱动测试，至少覆盖：Gateway 默认关闭；开启后读取 PostgreSQL DSN、连接池、控制面令牌、Snapshot 时间参数；非法 Keyring JSON、非法 Base64 和非 32 字节主密钥失败。

测试使用下列输入与断言，不写真实密钥：

```go
func TestLoadGatewayEnvironment(t *testing.T) {
	t.Setenv("LLM_PROXY_GATEWAY_ENABLED", "true")
	t.Setenv("LLM_PROXY_DATABASE_DRIVER", "postgres")
	t.Setenv("LLM_PROXY_DATABASE_DSN", "postgres://user:pass@127.0.0.1:5432/test?sslmode=disable")
	t.Setenv("LLM_PROXY_CONTROL_PLANE_TOKEN", "test-control-token-with-enough-entropy")
	t.Setenv("LLM_PROXY_CREDENTIAL_ENCRYPTION_CURRENT_KEY_VERSION", "v1")
	t.Setenv("LLM_PROXY_CREDENTIAL_ENCRYPTION_KEYS", `{"v1":"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}`)

	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Gateway.Enabled || cfg.Database.Driver != "postgres" || cfg.CredentialEncryption.CurrentKeyVersion != "v1" {
		t.Fatalf("gateway config = %+v", cfg)
	}
}

func TestLoadRejectsInvalidCredentialKeyring(t *testing.T) {
	t.Setenv("LLM_PROXY_CREDENTIAL_ENCRYPTION_KEYS", `{"v1":"c2hvcnQ="}`)
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("expected invalid 256-bit key error")
	}
}
```

- [ ] **步骤 2：运行配置测试并确认失败**

```bash
go test ./internal/infrastructure/config -run 'TestLoadGatewayEnvironment|TestLoadRejectsInvalidCredentialKeyring' -v
```

预期：FAIL，提示新增配置字段不存在。

- [ ] **步骤 3：实现配置结构和校验**

在 `config.go` 增加并挂到根 `Config`：

```go
type DatabaseConfig struct {
	Driver             string        `mapstructure:"driver"`
	DSN                string        `mapstructure:"dsn"`
	MaxIdleConnections int           `mapstructure:"max_idle_connections"`
	MaxOpenConnections int           `mapstructure:"max_open_connections"`
	ConnectionLifetime time.Duration `mapstructure:"connection_lifetime"`
	ConnectTimeout     time.Duration `mapstructure:"connect_timeout"`
}

type ControlPlaneConfig struct {
	Token string `mapstructure:"token"`
}

type CredentialEncryptionConfig struct {
	CurrentKeyVersion string            `mapstructure:"current_key_version"`
	Keys              map[string]string `mapstructure:"keys"`
}

type GatewayConfig struct {
	Enabled          bool          `mapstructure:"enabled"`
	SnapshotInterval time.Duration `mapstructure:"snapshot_interval"`
	SnapshotTimeout  time.Duration `mapstructure:"snapshot_timeout"`
	RetryBackoff     time.Duration `mapstructure:"retry_backoff"`
}
```

根配置增加 `Database`、`ControlPlane`、`CredentialEncryption`、`Gateway`。新增环境变量严格使用 `LLM_PROXY_` 前缀；Keyring 在 `applyComplexEnvironment` 中用 `json.Unmarshal` 解析，并逐项 `base64.StdEncoding.DecodeString` 验证长度等于 32。Gateway 开启时校验 Driver 只能为 `postgres`、DSN 和管理令牌非空、当前密钥版本存在。

- [ ] **步骤 4：更新中文 YAML 与环境变量示例**

`config.yaml` 只写安全默认值：

```yaml
gateway:
  enabled: false
  snapshot_interval: 5s
  snapshot_timeout: 3s
  retry_backoff: 5s

database:
  driver: postgres
  dsn: ""
  max_idle_connections: 5
  max_open_connections: 20
  connection_lifetime: 30m
  connect_timeout: 3s

control_plane:
  token: ""

credential_encryption:
  current_key_version: ""
  keys: {}
```

`.env.example` 增加对应变量，令牌和密钥值留空并用中文注释说明生成方式，禁止放入可工作的共享秘密。

- [ ] **步骤 5：添加依赖、格式化并验证**

```bash
go get gorm.io/gorm@v1.31.0
go get gorm.io/driver/postgres@v1.6.0
go get github.com/go-gormigrate/gormigrate/v2@v2.1.5
gofmt -w internal/infrastructure/config
go mod tidy
go test ./internal/infrastructure/config
```

预期：PASS；`github.com/google/uuid` 从间接依赖变为直接依赖。

- [ ] **步骤 6：提交配置和依赖**

```bash
git add go.mod go.sum internal/infrastructure/config config.yaml .env.example
git commit -m "feat: add unified gateway database configuration"
```

### 任务 2：建立共享领域原语与 Tenancy 聚合

**文件：**

- 新建：`internal/domain/shared/model/entity.go`
- 新建：`internal/domain/shared/model/status.go`
- 新建：`internal/domain/shared/errors/errors.go`
- 新建：`internal/domain/shared/port/transaction.go`
- 新建：`internal/domain/tenancy/model/organization.go`
- 新建：`internal/domain/tenancy/model/project.go`
- 新建：`internal/domain/tenancy/model/virtual_key.go`
- 新建：`internal/domain/tenancy/model/model_test.go`
- 新建：`internal/domain/tenancy/repository/repository.go`

- [ ] **步骤 1：先写 UUIDv7 与聚合约束测试**

```go
func TestNewOrganizationAndProjectUseUUIDv7(t *testing.T) {
	org, err := NewOrganization("Acme")
	if err != nil {
		t.Fatal(err)
	}
	project, err := NewProject(org.ID, "Production")
	if err != nil {
		t.Fatal(err)
	}
	if org.ID.Version() != 7 || project.ID.Version() != 7 {
		t.Fatalf("versions = %d, %d", org.ID.Version(), project.ID.Version())
	}
}

func TestNewProjectRejectsNilOrganization(t *testing.T) {
	if _, err := NewProject(uuid.Nil, "Production"); err == nil {
		t.Fatal("expected organization id error")
	}
}

func TestVirtualKeyExpired(t *testing.T) {
	expires := time.Now().Add(-time.Minute)
	key, err := NewVirtualKey(uuid.Must(uuid.NewV7()), "ci", [32]byte{1}, "llmp_v1_abc", "1234", &expires)
	if err != nil {
		t.Fatal(err)
	}
	if key.ActiveAt(time.Now()) {
		t.Fatal("expired key is active")
	}
}
```

- [ ] **步骤 2：运行测试并确认失败**

```bash
go test ./internal/domain/tenancy/model -v
```

预期：FAIL，包和构造器尚不存在。

- [ ] **步骤 3：实现共享实体和状态**

`entity.go`：

```go
package model

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Entity struct {
	ID        uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
}

func NewEntity() (Entity, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return Entity{}, fmt.Errorf("generate uuidv7: %w", err)
	}
	return Entity{ID: id}, nil
}
```

`status.go` 定义 `StatusActive`、`StatusDisabled` 并提供合法性检查；`transaction.go` 定义：

```go
type TransactionManager interface {
	Transaction(ctx context.Context, fn func(context.Context) error) error
}
```

`errors.go` 定义不携带基础设施细节的稳定 Sentinel：`ErrInvalid`、`ErrConflict`、`ErrDependencyUnavailable`。Repository 查询未命中统一返回 `(nil, nil)`，由 Application 根据用例语义转换为 NotFound。

- [ ] **步骤 4：实现 Tenancy 聚合与仓储契约**

每个构造器先调用 `sharedmodel.NewEntity()`，验证名称、父 ID、哈希和过期时间。仓储接口使用领域模型和 UUID：

```go
type OrganizationRepository interface {
	Save(context.Context, *model.Organization) error
	FindByID(context.Context, uuid.UUID) (*model.Organization, error)
	List(context.Context, int, int) ([]model.Organization, error)
}

type ProjectRepository interface {
	Save(context.Context, *model.Project) error
	FindByID(context.Context, uuid.UUID) (*model.Project, error)
	ListByOrganization(context.Context, uuid.UUID, int, int) ([]model.Project, error)
}

type VirtualKeyRepository interface {
	Save(context.Context, *model.VirtualKey) error
	FindByID(context.Context, uuid.UUID) (*model.VirtualKey, error)
	ListByProject(context.Context, uuid.UUID, int, int) ([]model.VirtualKey, error)
}
```

- [ ] **步骤 5：格式化并验证**

```bash
gofmt -w internal/domain/shared internal/domain/tenancy
go test ./internal/domain/shared/... ./internal/domain/tenancy/...
go test ./internal/architecture -run TestLayerDependencies -v
```

预期：PASS，Domain 不导入 Application、Interfaces、Infrastructure 或 DI。

- [ ] **步骤 6：提交 Tenancy 领域**

```bash
git add internal/domain/shared internal/domain/tenancy
git commit -m "feat: add multitenant control plane domain"
```

### 任务 3：建立 Catalog 聚合、作用域和能力模型

**文件：**

- 新建：`internal/domain/catalog/model/scope.go`
- 新建：`internal/domain/catalog/model/capability.go`
- 新建：`internal/domain/catalog/model/provider.go`
- 新建：`internal/domain/catalog/model/credential.go`
- 新建：`internal/domain/catalog/model/deployment.go`
- 新建：`internal/domain/catalog/model/model_alias.go`
- 新建：`internal/domain/catalog/model/route_target.go`
- 新建：`internal/domain/catalog/model/revision.go`
- 新建：`internal/domain/catalog/model/model_test.go`
- 新建：`internal/domain/catalog/repository/repository.go`

- [ ] **步骤 1：先写作用域与 UUIDv7 测试**

```go
func TestCredentialScopeValidation(t *testing.T) {
	projectID := uuid.Must(uuid.NewV7())
	tests := []struct {
		name  string
		scope Scope
		want  bool
	}{
		{name: "platform", scope: Scope{Kind: ScopePlatform}, want: true},
		{name: "organization", scope: Scope{Kind: ScopeOrganization, OrganizationID: uuid.Must(uuid.NewV7())}, want: true},
		{name: "project", scope: Scope{Kind: ScopeProject, ProjectID: projectID}, want: true},
		{name: "invalid platform target", scope: Scope{Kind: ScopePlatform, ProjectID: projectID}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.scope.Validate() == nil; got != tc.want {
				t.Fatalf("valid = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCatalogConstructorsUseUUIDv7(t *testing.T) {
	provider, _ := NewProvider("Fake", "fake")
	if provider.ID.Version() != 7 {
		t.Fatalf("version = %d", provider.ID.Version())
	}
}
```

- [ ] **步骤 2：运行测试并确认失败**

```bash
go test ./internal/domain/catalog/model -v
```

预期：FAIL，Catalog 类型尚不存在。

- [ ] **步骤 3：实现聚合字段和约束**

使用以下稳定类型，不用 `map[string]any` 代替核心语义：

```go
type ScopeKind string
const (
	ScopePlatform ScopeKind = "platform"
	ScopeOrganization ScopeKind = "organization"
	ScopeProject ScopeKind = "project"
)

type CapabilitySet struct {
	Text             bool `json:"text"`
	ImageInput       bool `json:"image_input"`
	Tools            bool `json:"tools"`
	StructuredOutput bool `json:"structured_output"`
	Streaming        bool `json:"streaming"`
}

type SealedCredential struct {
	KeyVersion       string
	WrappedKeyNonce  []byte
	WrappedDataKey   []byte
	PayloadNonce     []byte
	Ciphertext       []byte
}
```

Provider、ProviderCredential、Deployment、ModelAlias、RouteTarget 和 ConfigRevision 均嵌入共享 `Entity`。ModelAlias 保存 ProjectID 与 Name；RouteTarget 保存 ModelAliasID、DeploymentID、Priority、Weight、Status。构造器拒绝 nil UUID、空名称、非法作用域、非正权重和缺失能力。

- [ ] **步骤 4：定义 Catalog 仓储接口**

`repository.go` 分别声明 Provider、Credential、Deployment、ModelAlias、RouteTarget、ConfigRevision Repository。ConfigRevision 接口必须支持事务内递增：

```go
type ConfigRevisionRepository interface {
	Current(context.Context) (int64, error)
	Next(context.Context) (int64, error)
}
```

其余 Repository 均提供 `Save`、`FindByID` 和所需 List；ModelAlias 增加 `FindByProjectAndName`，RouteTarget 增加 `ListByModelAlias`。

- [ ] **步骤 5：格式化、测试并提交**

```bash
gofmt -w internal/domain/catalog
go test ./internal/domain/catalog/... ./internal/architecture
git add internal/domain/catalog
git commit -m "feat: add provider catalog domain"
```

### 任务 4：实现可降级数据库 Runtime、事务和迁移 CLI

**文件：**

- 新建：`internal/infrastructure/persistence/database/open.go`
- 新建：`internal/infrastructure/persistence/database/runtime.go`
- 新建：`internal/infrastructure/persistence/database/runtime_test.go`
- 新建：`internal/infrastructure/persistence/transactions/manager.go`
- 新建：`internal/infrastructure/persistence/transactions/manager_test.go`
- 新建：`internal/infrastructure/persistence/entity/base.go`
- 新建：`internal/infrastructure/persistence/entity/tenancy.go`
- 新建：`internal/infrastructure/persistence/entity/catalog.go`
- 新建：`internal/infrastructure/persistence/migration/migration.go`
- 新建：`internal/infrastructure/persistence/migration/v2026082501_initial.go`
- 新建：`internal/infrastructure/persistence/migration/migration_test.go`
- 新建：`cmd/migrate/main.go`

- [ ] **步骤 1：先写降级连接测试**

```go
func TestRuntimeStartsUnavailableAndRecovers(t *testing.T) {
	var attempts atomic.Int32
	opener := func(context.Context) (*gorm.DB, error) {
		if attempts.Add(1) == 1 {
			return nil, errors.New("database offline")
		}
		return testDB, nil
	}
	runtime := NewRuntime(opener, 10*time.Millisecond, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runtime.Run(ctx)
	requireEventually(t, time.Second, runtime.Available)
}
```

测试内使用可注入 Opener，不访问真实网络；同时测试 `DB()` 在未连接时返回稳定的 `ErrUnavailable`，`Close()` 可重复调用。

- [ ] **步骤 2：实现严格 Open 和可降级 Runtime**

`Open(ctx, cfg)` 使用 `postgres.New` 与 GORM 打开连接，取得 `sql.DB` 后执行 `PingContext`，再设置连接池。它用于迁移 CLI，失败直接返回错误。

`Runtime` 使用 `atomic.Pointer[gorm.DB]` 保存当前连接，`Run(ctx)` 首次失败后按 RetryBackoff 重试；成功后发布连接。接口固定为：

```go
var ErrUnavailable = errors.New("database unavailable")

type Runtime struct { /* opener、atomic pointer、backoff、logger、closeOnce */ }
func (r *Runtime) Run(context.Context)
func (r *Runtime) DB(context.Context) (*gorm.DB, error)
func (r *Runtime) Available() bool
func (r *Runtime) Close() error
```

不要在连接失败时调用 `log.Fatal`，不要让构造函数发起无限阻塞连接。

- [ ] **步骤 3：实现持久化 Entity**

`base.go`：

```go
type BaseEntity struct {
	ID        uuid.UUID `gorm:"type:uuid;not null;default:uuid_generate_v7();primary_key"`
	CreatedAt time.Time `gorm:"type:timestamp(0) without time zone;index;not null"`
	UpdatedAt time.Time `gorm:"type:timestamp(0) without time zone;not null"`
}
```

Tenancy/Catalog Entity 使用原生 `uuid.UUID` 保存逻辑关联 ID。能力 JSON 保存到 `text`；哈希、nonce、密文和包装数据密钥使用 `bytea`。每个 Entity 显式实现稳定复数表名。正常代码必须显式写入 UUIDv7；数据库 `uuid_generate_v7()` 默认值只服务手工 SQL。所有时间字段使用 `timestamp(0) without time zone`，连接指定 `TimeZone=Asia/Shanghai`。禁止声明 GORM Relationship、Association 或 `constraint`，迁移不得创建任何数据库外键；跨资源关联完整性全部由 Application Service 在事务内校验。

- [ ] **步骤 4：实现事务管理器**

Application 看到的 Adapter 实现领域 `TransactionManager`。Infrastructure Repository 使用内部接口：

```go
type Manager interface {
	Transaction(context.Context, func(context.Context) error) error
	DB(context.Context) (*gorm.DB, error)
}
```

事务中的 `*gorm.DB` 通过私有 Context Key 传播；嵌套事务复用已有事务。Runtime 不可用时返回 `database.ErrUnavailable`。

- [ ] **步骤 5：实现固定版本迁移**

迁移 ID 使用 `2026082501_initial_control_plane`。Up 顺序创建 organizations、projects、virtual_keys、providers、provider_credentials、deployments、model_aliases、route_targets、config_revisions；Down 仅按反向依赖顺序删除这些表。Up 最后创建一条 UUIDv7 的 revision=0 基础记录。

除 `BaseEntity.ID` 的手工插入兜底默认值外，业务代码禁止依赖 `uuid_generate_v7()`；同时禁止使用 `jsonb`、数组、数据库外键或手写 PostgreSQL 专属查询。普通索引至少覆盖父级列表、VirtualKey 哈希唯一索引、Project 内 Alias 名称唯一索引和 RouteTarget 逻辑关联 ID。

- [ ] **步骤 6：实现迁移 CLI**

`cmd/migrate/main.go` 使用标准库 `flag`/`os.Args` 支持：

```text
go run ./cmd/migrate up
go run ./cmd/migrate down
go run ./cmd/migrate status
```

CLI 加载同一份 `config.yaml`，调用严格 `database.Open`；缺失 DSN、连接失败或未知命令返回非零退出码。`status` 读取 gormigrate 版本表并打印当前版本，不输出 DSN 密码。

- [ ] **步骤 7：运行单元测试**

```bash
gofmt -w cmd/migrate internal/infrastructure/persistence
go test ./internal/infrastructure/persistence/database ./internal/infrastructure/persistence/transactions
go test ./internal/architecture
```

预期：PASS。

- [ ] **步骤 8：使用真实 PostgreSQL 验证迁移**

设置测试专用 DSN 后运行：

```bash
LLM_PROXY_TEST_POSTGRES_DSN='postgres://postgres:postgres@127.0.0.1:55432/llm_proxy_test?sslmode=disable' \
  go test ./internal/infrastructure/persistence/migration -v
```

预期：Up、字段类型、UUIDv7、Down 均 PASS；环境变量缺失时该集成测试明确 Skip。

- [ ] **步骤 9：提交数据库基础设施**

```bash
git add cmd/migrate internal/infrastructure/persistence
git commit -m "feat: add postgres persistence runtime and migrations"
```

### 任务 5：实现 GORM Repository Adapter

**文件：**

- 新建：`internal/infrastructure/persistence/repository/tenancy/mapper.go`
- 新建：`internal/infrastructure/persistence/repository/tenancy/repository.go`
- 新建：`internal/infrastructure/persistence/repository/tenancy/repository_test.go`
- 新建：`internal/infrastructure/persistence/repository/catalog/mapper.go`
- 新建：`internal/infrastructure/persistence/repository/catalog/repository.go`
- 新建：`internal/infrastructure/persistence/repository/catalog/repository_test.go`

- [ ] **步骤 1：先写真实 PostgreSQL Repository 测试**

测试必须验证：保存并查询 Organization/Project；同 Project Alias 重名失败；Virtual Key Hash 唯一；Credential 密文映射不丢字节；RouteTarget 引用；事务回滚；ConfigRevision 并发递增不丢失。

事务回滚核心断言：

```go
err := txManager.Transaction(ctx, func(txCtx context.Context) error {
	if err := organizations.Save(txCtx, org); err != nil {
		return err
	}
	if _, err := revisions.Next(txCtx); err != nil {
		return err
	}
	return errors.New("force rollback")
})
if err == nil {
	t.Fatal("expected rollback error")
}
if got, _ := organizations.FindByID(ctx, org.ID); got != nil {
	t.Fatal("organization persisted after rollback")
}
```

- [ ] **步骤 2：运行并确认失败**

```bash
LLM_PROXY_TEST_POSTGRES_DSN="$LLM_PROXY_TEST_POSTGRES_DSN" \
  go test ./internal/infrastructure/persistence/repository/... -v
```

预期：FAIL，Repository 构造器不存在。

- [ ] **步骤 3：实现 Mapper 和 Repository**

Mapper 必须逐字段转换领域模型与 Entity，不允许领域包导入 Entity。Repository 通过事务 Manager 取得 DB；`gorm.ErrRecordNotFound` 映射为 `(nil, nil)`，唯一冲突和数据库不可用映射为稳定领域/应用可识别错误。

分页统一校验 `limit` 为 1–100，`offset` 非负；列表查询必须有稳定的 `created_at, id` 排序。ConfigRevision `Next` 在事务内使用行锁读取唯一基础记录并更新版本。

- [ ] **步骤 4：运行测试和架构边界**

```bash
gofmt -w internal/infrastructure/persistence/repository
LLM_PROXY_TEST_POSTGRES_DSN="$LLM_PROXY_TEST_POSTGRES_DSN" \
  go test ./internal/infrastructure/persistence/repository/... -v
go test ./internal/architecture -run TestLayerDependencies -v
```

预期：PASS。

- [ ] **步骤 5：提交 Repository**

```bash
git add internal/infrastructure/persistence/repository
git commit -m "feat: add control plane repositories"
```

### 任务 6：实现 Virtual Key 与 Provider 凭据信封加密

**文件：**

- 新建：`internal/application/controlplane/port/security.go`
- 新建：`internal/infrastructure/security/virtualkey/generator.go`
- 新建：`internal/infrastructure/security/virtualkey/generator_test.go`
- 新建：`internal/infrastructure/security/credential/cipher.go`
- 新建：`internal/infrastructure/security/credential/cipher_test.go`
- 新建：`internal/infrastructure/security/controltoken/authorizer.go`
- 新建：`internal/infrastructure/security/controltoken/authorizer_test.go`

- [ ] **步骤 1：先写安全测试**

Virtual Key 测试验证 `llmp_v1_` 前缀、至少 256 bit 随机熵、SHA-256 哈希、展示前缀、末四位和两次生成不同。Credential 测试验证往返、数据库封装不含明文、篡改失败、交换记录 AAD 失败、未知 KeyVersion 失败、旧版本可读且新版本写入。

```go
func TestCipherRejectsCiphertextMovedToAnotherCredential(t *testing.T) {
	cipher := newTestCipher(t)
	first := uuid.Must(uuid.NewV7())
	second := uuid.Must(uuid.NewV7())
	sealed, err := cipher.Seal(context.Background(), first, providerID, scope, []byte(`{"api_key":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cipher.Open(context.Background(), second, providerID, scope, sealed); err == nil {
		t.Fatal("expected authenticated-data failure")
	}
}
```

- [ ] **步骤 2：运行并确认失败**

```bash
go test ./internal/infrastructure/security/... -v
```

预期：FAIL，安全实现尚不存在。

- [ ] **步骤 3：定义 Application 安全端口**

```go
type GeneratedVirtualKey struct {
	Plaintext string
	Hash      [32]byte
	Prefix    string
	LastFour  string
}

type VirtualKeyGenerator interface {
	Generate() (GeneratedVirtualKey, error)
}

type CredentialCipher interface {
	Seal(context.Context, uuid.UUID, uuid.UUID, catalogmodel.Scope, []byte) (catalogmodel.SealedCredential, error)
	Open(context.Context, uuid.UUID, uuid.UUID, catalogmodel.Scope, catalogmodel.SealedCredential) ([]byte, error)
}

type ControlPlaneAuthorizer interface {
	Authorize(token string) bool
}
```

- [ ] **步骤 4：实现安全组件**

Virtual Key 主体用 `crypto/rand` 读取 32 字节并 `base64.RawURLEncoding` 编码，哈希完整字符串。控制令牌 Authorizer 在构造时哈希配置令牌，认证时哈希请求令牌后用 `subtle.ConstantTimeCompare`。

Credential Cipher 每条记录生成随机 32 字节 DEK；先用 DEK/AES-GCM 加密经 `json.Compact` 规范化的 JSON，再用当前 KEK/AES-GCM 包装 DEK。AAD 使用固定版本前缀和 NUL 分隔字段：`llm-proxy/credential/v1\x00<credential-id>\x00<provider-id>\x00<scope-kind>\x00<scope-target-id>`；包装 DEK 追加 `\x00dek`，加密 Payload 追加 `\x00payload`。所有 nonce 由对应 GCM 的 `NonceSize()` 决定并独立生成，任何随机源错误都直接失败。

- [ ] **步骤 5：验证、race 并提交**

```bash
gofmt -w internal/application/controlplane/port internal/infrastructure/security
go test -race ./internal/infrastructure/security/...
git add internal/application/controlplane/port internal/infrastructure/security
git commit -m "feat: secure virtual keys and provider credentials"
```

### 任务 7：实现控制面 Application Use Case

**文件：**

- 新建：`internal/application/controlplane/dto/command.go`
- 新建：`internal/application/controlplane/dto/response.go`
- 新建：`internal/application/controlplane/errors/errors.go`
- 新建：`internal/application/controlplane/service/tenancy.go`
- 新建：`internal/application/controlplane/service/catalog.go`
- 新建：`internal/application/controlplane/service/service_test.go`

- [ ] **步骤 1：先写事务与安全边界测试**

使用内存 Fake Repository 测试：创建 Organization；创建 Project 前验证 Organization；创建 Virtual Key 只在结果返回明文且 Repository 收到的领域对象只有 Hash；创建 Credential 只保存 SealedCredential；每个影响数据面的写操作与 Revision.Next 在同一事务；失败时二者一起回滚；Phase 1B 拒绝非 platform 凭据。

```go
func TestCreateVirtualKeyReturnsPlaintextOnceAndPersistsOnlyHash(t *testing.T) {
	svc, fakes := newServiceFixture(t)
	result, err := svc.CreateVirtualKey(context.Background(), dto.CreateVirtualKey{ProjectID: fakes.project.ID, Name: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256([]byte(result.Secret))
	if result.Secret == "" || fakes.savedVirtualKey.Hash != wantHash {
		t.Fatal("persisted hash does not match one-time secret")
	}
	if fakes.revision != 1 {
		t.Fatalf("revision = %d", fakes.revision)
	}
}
```

- [ ] **步骤 2：运行并确认失败**

```bash
go test ./internal/application/controlplane/service -v
```

预期：FAIL，Service 尚不存在。

- [ ] **步骤 3：定义命令、响应与服务接口**

Command 使用 UUID 类型，不让 HTTP 字符串进入领域服务。Virtual Key 结果唯一包含一次性 Secret：

```go
type CreateVirtualKeyResult struct {
	ID       uuid.UUID
	Secret   string
	Prefix   string
	LastFour string
	Revision int64
}
```

ProviderCredential 的响应只包含 ID、ProviderID、Scope、KeyVersion、Status 和时间，禁止包含 SealedCredential 字段。

Application 错误定义稳定 `Code`：InvalidRequest、AuthenticationFailed、PermissionDenied、NotFound、Conflict、DependencyUnavailable、Internal。错误只包含 Code、SafeMessage、Param 和可供 `errors.Is/As` 使用的 Cause；`Error()` 不拼接 Cause。Service 把共享领域冲突、父资源缺失和数据库不可用映射到这里，HTTP 层不导入 Infrastructure 错误。

- [ ] **步骤 4：实现 Tenancy 与 Catalog Service**

所有写方法采用统一模板：读取并验证父资源；构造领域聚合；在 `TransactionManager.Transaction` 中保存；若影响数据面则调用 `ConfigRevisionRepository.Next`；事务成功后返回 Revision。更新只允许白名单字段，停用前检查必要引用。

Catalog Service 创建 ModelAlias/RouteTarget 时验证 Project、Deployment 作用域和 Phase 1B 单一启用 Fake Target 约束。Credential 明文 JSON 只传给 Cipher，Seal 成功后立即用密文聚合替代，不进入日志或响应。

- [ ] **步骤 5：运行测试和架构检查**

```bash
gofmt -w internal/application/controlplane
go test ./internal/application/controlplane/...
go test ./internal/architecture -run TestLayerDependencies -v
```

预期：PASS。

- [ ] **步骤 6：提交 Application 用例**

```bash
git add internal/application/controlplane
git commit -m "feat: add control plane application services"
```

### 任务 8：实现控制面 HTTP Adapter 与资源路由

**文件：**

- 新建：`internal/interfaces/http/response/json.go`
- 新建：`internal/interfaces/http/handler/controlplane/dependencies.go`
- 新建：`internal/interfaces/http/handler/controlplane/tenancy.go`
- 新建：`internal/interfaces/http/handler/controlplane/catalog.go`
- 新建：`internal/interfaces/http/handler/controlplane/handler_test.go`
- 新建：`internal/interfaces/http/middleware/controlplane_auth.go`
- 新建：`internal/interfaces/http/middleware/controlplane_auth_test.go`
- 新建：`internal/interfaces/http/middleware/request_id.go`
- 新建：`internal/interfaces/http/middleware/request_id_test.go`
- 新建：`internal/interfaces/http/middleware/controlplane_logging.go`
- 修改：`internal/interfaces/http/router/router.go`
- 修改：`internal/interfaces/http/router/router_test.go`

- [ ] **步骤 1：先写路由与密钥泄漏测试**

至少覆盖：缺少/错误管理令牌返回 401；`POST /v1/organizations` 返回 201；`POST /v1/projects/{id}/virtual-keys` 只在创建响应返回 Secret；Credential 查询不返回密文；非法 UUID 返回 400；数据库不可用映射 503；原 `/openai/*`、`/anthropic/*` 与 Dashboard fallback 仍通过。

```go
func TestControlPlaneRoutesRequireManagementToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations", strings.NewReader(`{"name":"Acme"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}
```

- [ ] **步骤 2：运行并确认失败**

```bash
go test ./internal/interfaces/http/handler/controlplane ./internal/interfaces/http/middleware ./internal/interfaces/http/router -v
```

预期：FAIL，新路由和 Handler 尚不存在。

- [ ] **步骤 3：实现请求 ID、公共 JSON 响应和认证中间件**

Request ID Middleware 只接受长度不超过 128 且由字母、数字、`-`、`_`、`.` 组成的 `x-request-id`，否则生成 UUIDv7；它把值放入 Context，并设置响应 `x-request-id`。所有控制面错误从 Context 读取同一个 ID。

响应 Writer 固定设置 `Content-Type: application/json`，错误结构为：

```go
type ErrorEnvelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}
```

认证中间件只包裹控制面资源路由，不包裹现有透明代理和未来数据面。不得把 Authorization Header 放入 Context、日志字段或下游 DTO。控制面使用独立 Logging Middleware，只记录请求模式、状态、耗时、字节数和 Request ID，绝不读取或记录管理令牌，包括末四位。

- [ ] **步骤 4：实现资源 Handler**

每个 Handler 只做：使用 `http.MaxBytesReader` 把写请求 Body 上限固定为 1 MiB、JSON Decode、UUID Parse、调用 Application Service、映射状态码和 JSON Encode。禁止导入 GORM/Entity。创建返回 201；查询/列表/更新返回 200；认证失败 401；资源越权 403；不存在 404；冲突 409；数据库不可用 503。

Virtual Key 创建响应字段名固定为 `secret`，仅该 DTO 包含它。ProviderCredential 写请求接受 `credential` JSON 对象，序列化后交给 Application；读响应 DTO 不定义密文字段。

首版路由表固定为：

```text
POST,GET  /v1/organizations
GET,PATCH /v1/organizations/{organization_id}
POST,GET  /v1/organizations/{organization_id}/projects
GET,PATCH /v1/projects/{project_id}
POST,GET  /v1/projects/{project_id}/virtual-keys
GET,PATCH /v1/virtual-keys/{virtual_key_id}
POST,GET  /v1/providers
GET,PATCH /v1/providers/{provider_id}
POST,GET  /v1/provider-credentials
GET,PATCH /v1/provider-credentials/{credential_id}
POST,GET  /v1/deployments
GET,PATCH /v1/deployments/{deployment_id}
POST,GET  /v1/model-aliases
GET,PATCH /v1/model-aliases/{model_alias_id}
POST,GET  /v1/model-aliases/{model_alias_id}/route-targets
GET,PATCH /v1/route-targets/{route_target_id}
```

PATCH 只接受名称、状态、过期时间、能力、权重、优先级等明确白名单字段；Credential Secret 轮换使用 ProviderCredential PATCH 的写专用 `credential` 字段并生成全新密文封装。所有 DELETE 均返回 405。

- [ ] **步骤 5：显式注册资源化路由**

在 `router.Dependencies` 增加可选 `ControlPlane http.Handler`。Gateway 开启时注册控制面资源的精确模式，并按 RequestID → ControlPlaneLogging → ControlPlaneAuth → Handler 的顺序包裹；Gateway 关闭时不注册，继续由 Dashboard 根 Handler 接住未知路径。不要使用 `/admin` 或 `/v1/admin`。

- [ ] **步骤 6：运行 HTTP 和回归测试**

```bash
gofmt -w internal/interfaces/http
go test ./internal/interfaces/http/handler/controlplane ./internal/interfaces/http/middleware ./internal/interfaces/http/router
go test ./internal/architecture
```

预期：PASS。

- [ ] **步骤 7：提交 HTTP 控制面**

```bash
git add internal/interfaces/http
git commit -m "feat: expose control plane resource api"
```

### 任务 9：Wire 组装、生命周期与真实 PostgreSQL 纵向验证

**文件：**

- 新建：`internal/di/provider/controlplane.go`
- 修改：`internal/di/modules/app.go`
- 修改：`internal/di/provider/transparent.go`
- 修改：`internal/di/app.go`
- 修改：`internal/di/wire.go`
- 生成：`internal/di/wire_gen.go`
- 修改：`cmd/proxy/main.go`
- 新建：`internal/interfaces/http/handler/controlplane/integration_test.go`

- [ ] **步骤 1：先写 Gateway 关闭和数据库降级测试**

测试 `Gateway.Enabled=false` 时不创建数据库 Runtime、控制面路由不注册、透明代理特征测试通过；开启且数据库离线时 App 初始化成功、控制面返回 503、透明代理仍可访问。

- [ ] **步骤 2：实现条件化依赖组装**

`controlplane.go` 负责创建数据库 Runtime、事务、Repository、安全组件、Application Service 和 HTTP Handler。使用一个封装对象避免 Wire 为每个 Repository 制造可选依赖歧义：

```go
type ControlPlaneRuntime struct {
	Handler  http.Handler
	Database *database.Runtime
}
```

Gateway 关闭时返回空 Handler 和 no-op 生命周期；开启时校验敏感配置，但数据库连接由 Runtime 后台重试，不让 DI 初始化失败。

- [ ] **步骤 3：接入 App 生命周期**

`di.App` 增加统一网关 Runtime。`main` 在 HTTP Server 启动前启动数据库连接循环；关闭时先停止数据库重连后台循环，再执行 `Server.Shutdown`，随后关闭数据库连接，最后执行 `Telemetry.Shutdown`。每一步失败都记录并继续，HTTP 关闭期间已进入控制面的请求仍可使用现有数据库连接。

现有 `/readyz` 仍只反映 HTTP Server 生命周期，不读取数据库状态。

- [ ] **步骤 4：生成 Wire 并验证可重复生成**

```bash
go run github.com/google/wire/cmd/wire@v0.7.0 ./internal/di
cp internal/di/wire_gen.go /tmp/llm-proxy-wire-gen.go
go run github.com/google/wire/cmd/wire@v0.7.0 ./internal/di
cmp /tmp/llm-proxy-wire-gen.go internal/di/wire_gen.go
```

预期：两次生成一致。

- [ ] **步骤 5：运行真实 PostgreSQL 纵向测试**

```bash
LLM_PROXY_TEST_POSTGRES_DSN="$LLM_PROXY_TEST_POSTGRES_DSN" \
  go test ./internal/interfaces/http/handler/controlplane -run Integration -v
```

测试按顺序迁移空库、创建 Organization/Project/VirtualKey/Fake Provider/Fake Deployment/Alias/Target、查询资源、停用资源，并直接查询数据库断言没有 Virtual Key 或 Credential 明文。

- [ ] **步骤 6：运行阶段验证**

```bash
gofmt -w cmd internal
go test ./...
go test -race ./internal/application/controlplane/... ./internal/infrastructure/security/... ./internal/infrastructure/persistence/database/... ./internal/interfaces/http/...
go vet ./...
go build -o /tmp/llm-proxy ./cmd/proxy
go build -o /tmp/llm-proxy-migrate ./cmd/migrate
git diff --check
```

预期：全部 PASS，构建产物只在 `/tmp`。

- [ ] **步骤 7：提交组装和纵向测试**

```bash
git add cmd/proxy internal/di internal/interfaces/http/handler/controlplane/integration_test.go
git commit -m "feat: assemble postgres control plane runtime"
```

## 三、本计划完成检查点

- 控制面所有业务 ID 由应用生成 UUIDv7，PostgreSQL 使用原生 UUID。
- 迁移 CLI 可执行 up/down/status，服务启动不自动迁移。
- 数据库离线不阻止现有透明代理启动。
- 控制面 API 使用 `/v1` 资源路径且受独立管理令牌保护。
- Virtual Key 只展示一次且只存哈希。
- ProviderCredential 使用按记录 DEK 的 AES-256-GCM 信封加密。
- 所有控制面写入与 ConfigRevision 在同一事务。
- 工作区干净后再进入 `2026-08-25-runtime-snapshot.md`。
