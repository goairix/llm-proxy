# Phase 1B-2：运行时配置快照实施计划

> **供执行代理使用：** 必须使用 `superpowers:subagent-driven-development`（推荐）或 `superpowers:executing-plans`，逐任务执行本计划。所有步骤使用 `- [ ]` 复选框跟踪。

**目标：** 将 PostgreSQL 中的多租户控制面配置编译为不可变 RuntimeSnapshot，通过版本轮询、进程内唤醒和原子指针安全发布，使 Virtual Key 认证与模型解析的请求热路径完全不访问数据库。

**架构：** Infrastructure 以可重复读事务加载领域读模型，Application 编译并验证快照，SnapshotStore 使用 `atomic.Pointer` 发布，Refresher 保留最后一份可用快照并在数据库恢复后自动追赶 Revision。数据面认证只接收入口 Adapter 提取出的 Virtual Key，不接触 HTTP Header。

**技术栈：** Go 1.25、`atomic.Pointer`、Context、GORM/PostgreSQL、Zap、Google Wire、标准库并发测试。

**前置计划：** `docs/superpowers/plans/2026-08-25-postgresql-control-plane.md`

**设计依据：** `docs/superpowers/specs/2026-08-25-unified-protocol-kernel-postgresql-design.md`

---

## 一、执行边界与最终文件结构

执行前必须位于 `feat/unified-protocol-kernel`，且前置控制面计划的测试全部通过。禁止使用 worktree。

```text
internal/application/gateway/snapshot/source.go            # 跨聚合一致性源配置
internal/application/gateway/port/runtime.go               # Loader、Revision、Wake 端口
internal/application/gateway/snapshot/model.go             # 只读运行时结构
internal/application/gateway/snapshot/compiler.go          # 领域配置 → 快照
internal/application/gateway/snapshot/store.go             # atomic.Pointer 发布
internal/application/gateway/snapshot/authenticator.go     # Virtual Key → 租户上下文
internal/infrastructure/persistence/repository/runtime/     # PostgreSQL 一致性 Loader
internal/infrastructure/snapshot/refresher.go               # 轮询、唤醒、退避、LKG
internal/infrastructure/snapshot/runtime.go                 # 后台生命周期
internal/di/provider/gateway_runtime.go                     # 条件组装
```

## 二、任务

### 任务 1：定义一致性读模型和 RuntimeConfigReader

**文件：**

- 新建：`internal/application/gateway/snapshot/source.go`
- 新建：`internal/application/gateway/port/runtime.go`
- 修改：`internal/domain/shared/port/transaction.go`
- 修改：`internal/infrastructure/persistence/transactions/manager.go`
- 修改：`internal/infrastructure/persistence/transactions/manager_test.go`

- [ ] **步骤 1：先写可重复读事务测试**

测试 `ReadOnlySnapshot` 在同一回调的多次查询中复用同一个 GORM Transaction，并以只读、可重复读选项开始；嵌套调用复用已有事务；数据库不可用返回 `database.ErrUnavailable`。

```go
func TestReadOnlySnapshotReusesTransactionContext(t *testing.T) {
	err := manager.ReadOnlySnapshot(context.Background(), func(ctx context.Context) error {
		first, err := manager.DB(ctx)
		if err != nil {
			return err
		}
		second, err := manager.DB(ctx)
		if err != nil {
			return err
		}
		if first != second {
			t.Fatal("transaction handle was not reused")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **步骤 2：运行并确认失败**

```bash
go test ./internal/infrastructure/persistence/transactions -run ReadOnlySnapshot -v
```

预期：FAIL，方法尚不存在。

- [ ] **步骤 3：扩展事务端口**

领域端口增加不暴露 SQL 隔离枚举的方法：

```go
type TransactionManager interface {
	Transaction(context.Context, func(context.Context) error) error
	ReadOnlySnapshot(context.Context, func(context.Context) error) error
}
```

GORM Adapter 使用：

```go
&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
```

PostgreSQL 实现必须保证整个配置加载处于同一事务快照。以后数据库方言可在 Adapter 中调整等价实现。

- [ ] **步骤 4：定义运行时配置读模型**

运行时配置是跨 Tenancy/Catalog 的查询模型，不属于单一聚合 Repository，因此放在 Application 的 Snapshot 源模型；各聚合写 Repository 仍由 Domain 定义。`source.go`：

```go
type SourceConfig struct {
	Revision      int64
	Organizations []tenancymodel.Organization
	Projects      []tenancymodel.Project
	VirtualKeys   []tenancymodel.VirtualKey
	Providers     []catalogmodel.Provider
	Credentials   []catalogmodel.ProviderCredential
	Deployments   []catalogmodel.Deployment
	ModelAliases  []catalogmodel.ModelAlias
	RouteTargets  []catalogmodel.RouteTarget
}
```

`port/runtime.go` 定义：

```go
type RuntimeConfigReader interface {
	CurrentRevision(context.Context) (int64, error)
	Load(context.Context) (snapshot.SourceConfig, error)
}
```

注意为 Tenancy 与 Catalog 使用明确别名，不能出现两个含义不明的 `model` Import。`Load` 返回领域对象，不返回 Entity。

- [ ] **步骤 5：格式化、测试并提交**

```bash
gofmt -w internal/application/gateway/port internal/application/gateway/snapshot internal/domain/shared/port internal/infrastructure/persistence/transactions
go test ./internal/infrastructure/persistence/transactions ./internal/application/gateway/port ./internal/application/gateway/snapshot
go test ./internal/architecture
git add internal/application/gateway/port internal/domain/shared/port internal/infrastructure/persistence/transactions
git commit -m "feat: add consistent runtime config reader contract"
```

### 任务 2：实现不可变 Snapshot 模型与编译器

**文件：**

- 新建：`internal/application/gateway/snapshot/model.go`
- 新建：`internal/application/gateway/snapshot/compiler.go`
- 新建：`internal/application/gateway/snapshot/compiler_test.go`

- [ ] **步骤 1：先写编译器约束测试**

至少覆盖：正常编译；停用 Organization/Project/Key 被排除；Project 内 Alias 索引；Target 引用不存在失败；Credential 作用域不允许失败；Alias 没有启用 Target 失败；Phase 1B 多个启用 Target 失败；能力声明保留；快照不含明文凭据。

```go
func TestCompilerIndexesVirtualKeyAndModelAlias(t *testing.T) {
	config := validSourceConfig(t)
	snapshot, err := NewCompiler().Compile(config, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	access, ok := snapshot.virtualKeys[config.VirtualKeys[0].Hash]
	if !ok || access.ProjectID != config.Projects[0].ID {
		t.Fatalf("access = %+v, ok = %v", access, ok)
	}
	plan, ok := snapshot.routes[RouteKey{ProjectID: access.ProjectID, Model: "fake-model"}]
	if !ok || plan.Deployment.ConnectorType != "fake" {
		t.Fatalf("plan = %+v, ok = %v", plan, ok)
	}
}
```

- [ ] **步骤 2：运行并确认失败**

```bash
go test ./internal/application/gateway/snapshot -run Compiler -v
```

预期：FAIL，Snapshot 类型尚不存在。

- [ ] **步骤 3：实现只读快照类型**

核心类型固定为：

```go
type AccessContext struct {
	OrganizationID uuid.UUID
	ProjectID      uuid.UUID
	VirtualKeyID   uuid.UUID
	ExpiresAt      time.Time
	HasExpiry      bool
}

type RouteKey struct {
	ProjectID uuid.UUID
	Model     string
}

type CredentialEnvelope struct {
	CredentialID uuid.UUID
	ProviderID   uuid.UUID
	Scope        catalogmodel.Scope
	Sealed       catalogmodel.SealedCredential
}

type Deployment struct {
	ID            uuid.UUID
	ProviderID    uuid.UUID
	ConnectorType string
	UpstreamModel string
	Capabilities  catalogmodel.CapabilitySet
	Credential    *CredentialEnvelope
}

type RoutePlan struct {
	AliasID    uuid.UUID
	Alias      string
	Deployment Deployment
}

type RuntimeSnapshot struct {
	revision    int64
	builtAt     time.Time
	virtualKeys map[[32]byte]AccessContext
	routes      map[RouteKey]RoutePlan
}

func (s *RuntimeSnapshot) Revision() int64
func (s *RuntimeSnapshot) BuiltAt() time.Time
```

`CredentialEnvelope` 只复制 SealedCredential 与安全 UUID，不提供明文或 Cipher 实例。Map 保持非导出；`Session.Resolve` 返回 RoutePlan 的深拷贝，CredentialEnvelope 内的所有字节切片都必须复制，调用者无法修改已发布快照。

- [ ] **步骤 4：实现 Compiler**

编译分两轮：第一轮建立有效 Organization、Project、Provider、Credential、Deployment 索引；第二轮编译 VirtualKey 与 ModelAlias/RouteTarget。所有 Map/Slice 在返回前完成，返回后不暴露修改方法。

编译错误包含 Revision、资源类型和安全 UUID，但不能包含密文、Key Hash 或完整模型请求。模型别名统一按原始大小写精确匹配，不在 Phase 1B 擅自 lowercase。

- [ ] **步骤 5：运行测试、race 和架构检查**

```bash
gofmt -w internal/application/gateway/snapshot
go test -race ./internal/application/gateway/snapshot
go test ./internal/architecture -run TestLayerDependencies -v
```

预期：PASS，Application 不导入 Infrastructure、Interfaces 或 DI。

- [ ] **步骤 6：提交快照编译器**

```bash
git add internal/application/gateway/snapshot
git commit -m "feat: compile immutable gateway snapshots"
```

### 任务 3：实现原子 SnapshotStore 与 Virtual Key Authenticator

**文件：**

- 新建：`internal/application/gateway/snapshot/store.go`
- 新建：`internal/application/gateway/snapshot/store_test.go`
- 新建：`internal/application/gateway/snapshot/authenticator.go`
- 新建：`internal/application/gateway/snapshot/authenticator_test.go`

- [ ] **步骤 1：先写并发发布和认证测试**

```go
func TestStorePublishesWholeSnapshotsConcurrently(t *testing.T) {
	store := NewStore()
	one := newSnapshotForTest(1)
	two := newSnapshotForTest(2)
	store.Publish(one)

	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, ok := store.Current()
			if !ok || (got.Revision() != 1 && got.Revision() != 2) {
				t.Errorf("revision = %d", got.Revision())
			}
		}()
	}
	store.Publish(two)
	wg.Wait()
}
```

认证测试覆盖：正确 Token；错误 Token；空 Token；过期；停用资源已在编译时排除；认证结果不可修改 Snapshot；错误中不出现 Token。

- [ ] **步骤 2：运行并确认失败**

```bash
go test ./internal/application/gateway/snapshot -run 'Store|Authenticator' -v
```

预期：FAIL，Store/Authenticator 尚不存在。

- [ ] **步骤 3：实现 Store**

```go
type Store struct {
	current atomic.Pointer[RuntimeSnapshot]
}

func (s *Store) Current() (*RuntimeSnapshot, bool) {
	current := s.current.Load()
	return current, current != nil
}

func (s *Store) Publish(next *RuntimeSnapshot) {
	if next == nil {
		panic("publish nil runtime snapshot")
	}
	s.current.Store(next)
}
```

只允许 Compiler 创建的快照被发布。RuntimeSnapshot 的索引字段不导出；测试使用包内构造辅助函数，生产调用者不能持有或修改底层 Map。

- [ ] **步骤 4：实现 Authenticator 与 Route Resolver**

`Authenticate(token string, now time.Time)` 对完整 Token 执行 SHA-256，查找快照 Hash Map，检查过期时间，返回值复制的 AccessContext。`Resolve(projectID, model string)` 从同一份 Snapshot 解析 RoutePlan；一次 Gateway 请求必须先获取一次 Snapshot 指针并贯穿认证与路由，禁止认证后重新读取另一个 Revision。

为此提供 Session：

```go
type Session struct { snapshot *RuntimeSnapshot }
func (s *Store) Begin() (Session, error)
func (s Session) Authenticate(string, time.Time) (AccessContext, error)
func (s Session) Resolve(uuid.UUID, string) (RoutePlan, error)
func (s Session) Revision() int64
```

- [ ] **步骤 5：运行 race 并提交**

```bash
gofmt -w internal/application/gateway/snapshot
go test -race ./internal/application/gateway/snapshot
git add internal/application/gateway/snapshot
git commit -m "feat: publish and query gateway snapshots atomically"
```

### 任务 4：实现 PostgreSQL RuntimeConfigReader

**文件：**

- 新建：`internal/infrastructure/persistence/repository/runtime/reader.go`
- 新建：`internal/infrastructure/persistence/repository/runtime/reader_test.go`

- [ ] **步骤 1：先写真实 PostgreSQL 一致性读取测试**

测试准备两个完整 Revision，验证 Reader 只返回同一事务视图中的 Revision 与全部关联配置；数据库不可用返回 `database.ErrUnavailable`；Entity 到领域模型转换与普通 Repository 一致；停用记录仍被加载给 Compiler 决定排除。

```go
func TestReaderLoadsOneConsistentRevision(t *testing.T) {
	got, err := reader.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != wantRevision || len(got.Organizations) != 1 || len(got.RouteTargets) != 1 {
		t.Fatalf("runtime config = %+v", got)
	}
}
```

- [ ] **步骤 2：运行并确认失败**

```bash
LLM_PROXY_TEST_POSTGRES_DSN="$LLM_PROXY_TEST_POSTGRES_DSN" \
  go test ./internal/infrastructure/persistence/repository/runtime -v
```

预期：FAIL，Reader 尚不存在。

- [ ] **步骤 3：实现 CurrentRevision 与 Load**

`CurrentRevision` 是低成本单行查询。`Load` 必须调用 `TransactionManager.ReadOnlySnapshot`，在同一回调中读取 Revision 与全部表，然后逐类映射为领域对象；禁止在循环中执行 N+1 查询，禁止解密 Credential。

加载顺序不表达依赖正确性，所有引用校验交给 Compiler。查询按 ID 稳定排序，便于测试和诊断。

- [ ] **步骤 4：验证并提交**

```bash
gofmt -w internal/infrastructure/persistence/repository/runtime
LLM_PROXY_TEST_POSTGRES_DSN="$LLM_PROXY_TEST_POSTGRES_DSN" \
  go test ./internal/infrastructure/persistence/repository/runtime -v
go test ./internal/architecture
git add internal/infrastructure/persistence/repository/runtime
git commit -m "feat: load consistent gateway runtime configuration"
```

### 任务 5：实现版本轮询、唤醒、退避和最后可用快照

**文件：**

- 修改：`internal/application/gateway/port/runtime.go`
- 新建：`internal/infrastructure/snapshot/refresher.go`
- 新建：`internal/infrastructure/snapshot/refresher_test.go`
- 新建：`internal/infrastructure/snapshot/runtime.go`
- 新建：`internal/infrastructure/snapshot/runtime_test.go`

- [ ] **步骤 1：先写 Refresher 状态机测试**

使用 Fake Reader/Compiler 和可控 Timer 测试：启动立即加载；版本未变不重编译；Wake 触发立即检查；Reader 失败保留旧快照；Compile 失败保留旧快照；后续成功发布新 Revision；Wake Channel 满时调用不阻塞；Context 取消及时退出且无 goroutine 泄漏。

```go
func TestRefresherKeepsLastKnownGoodSnapshot(t *testing.T) {
	store := snapshot.NewStore()
	store.Publish(validSnapshot(1))
	reader := &fakeReader{revision: 2, loadErr: errors.New("database offline")}
	r := NewRefresher(reader, compiler, store, Options{PollInterval: time.Hour, LoadTimeout: time.Second}, zap.NewNop())

	r.Refresh(context.Background())
	got, _ := store.Current()
	if got.Revision() != 1 {
		t.Fatalf("revision = %d, want last-known-good 1", got.Revision())
	}
}
```

- [ ] **步骤 2：运行并确认失败**

```bash
go test ./internal/infrastructure/snapshot -v
```

预期：FAIL，Refresher 尚不存在。

- [ ] **步骤 3：定义运行时端口并实现 Refresher**

```go
type RevisionReader interface { CurrentRevision(context.Context) (int64, error) }
type ConfigLoader interface { Load(context.Context) (snapshot.SourceConfig, error) }
type SnapshotCompiler interface { Compile(snapshot.SourceConfig, time.Time) (*snapshot.RuntimeSnapshot, error) }
type RefreshNotifier interface { NotifyRefresh() }
```

Refresher 的 `NotifyRefresh` 只向容量 1 的 Channel 非阻塞发送。`Run` 启动后立即尝试刷新，然后等待 Ticker、Wake 或 Context。每次 Load 使用 `context.WithTimeout`；失败按 RetryBackoff 控制错误日志频率，但正常轮询间隔不被永久改变。

- [ ] **步骤 4：实现 Runtime 生命周期**

Runtime 组合数据库 Runtime 与 Snapshot Refresher，公开：

```go
func (r *Runtime) Start(context.Context)
func (r *Runtime) NotifyRefresh()
func (r *Runtime) Stop(context.Context) error
func (r *Runtime) CloseDatabase() error
func (r *Runtime) Store() *snapshot.Store
```

Start 只能调用一次；Stop 取消内部 Context并等待数据库重连与刷新 goroutine 退出，但不关闭已经发布的数据库连接；HTTP Server 完成 Shutdown 后再调用 CloseDatabase。Gateway 关闭时使用 no-op Runtime。

- [ ] **步骤 5：运行 race 并提交**

```bash
gofmt -w internal/application/gateway/port internal/infrastructure/snapshot
go test -race ./internal/infrastructure/snapshot
git add internal/application/gateway/port internal/infrastructure/snapshot
git commit -m "feat: refresh gateway snapshots with last known good state"
```

### 任务 6：控制面提交后唤醒刷新器并接入 Wire 生命周期

**文件：**

- 修改：`internal/application/controlplane/service/tenancy.go`
- 修改：`internal/application/controlplane/service/catalog.go`
- 修改：`internal/application/controlplane/service/service_test.go`
- 新建：`internal/di/provider/gateway_runtime.go`
- 修改：`internal/di/provider/controlplane.go`
- 修改：`internal/di/modules/app.go`
- 修改：`internal/di/app.go`
- 修改：`internal/di/wire.go`
- 生成：`internal/di/wire_gen.go`
- 修改：`cmd/proxy/main.go`

- [ ] **步骤 1：先写“提交后才唤醒”测试**

```go
func TestCreateProjectNotifiesOnlyAfterCommit(t *testing.T) {
	notifier := &fakeNotifier{}
	svc := newServiceWithNotifier(notifier)
	if _, err := svc.CreateProject(context.Background(), validCommand); err != nil {
		t.Fatal(err)
	}
	if notifier.calls != 1 {
		t.Fatalf("notify calls = %d", notifier.calls)
	}
}

func TestFailedTransactionDoesNotNotify(t *testing.T) {
	notifier := &fakeNotifier{}
	svc := newFailingServiceWithNotifier(notifier)
	_, _ = svc.CreateProject(context.Background(), validCommand)
	if notifier.calls != 0 {
		t.Fatalf("notify calls = %d", notifier.calls)
	}
}
```

- [ ] **步骤 2：实现提交后通知**

Control Plane Service 在 `Transaction` 返回 nil 后调用 `RefreshNotifier.NotifyRefresh()`。通知不能放在事务回调中，不能因 Channel 满而阻塞 HTTP 响应；通知失败不回滚已经提交的配置。

- [ ] **步骤 3：接入 Gateway Runtime**

Wire 使用同一个 Runtime 实例向控制面提供 Notifier、向未来数据面提供 Store。`di.App` 保存 Runtime；`main` 在启动 HTTP Server 前调用 Runtime.Start。关闭顺序固定为 Runtime.Stop → Server.Shutdown → Runtime.CloseDatabase → Telemetry.Shutdown，各步骤失败都继续后续清理。

数据库 Runtime 启动失败由后台重试处理，DI 和 HTTP Server 仍成功启动。Gateway 关闭时不启动任何连接或 goroutine。

- [ ] **步骤 4：重新生成 Wire 并验证**

```bash
go run github.com/google/wire/cmd/wire@v0.7.0 ./internal/di
cp internal/di/wire_gen.go /tmp/llm-proxy-wire-gen.go
go run github.com/google/wire/cmd/wire@v0.7.0 ./internal/di
cmp /tmp/llm-proxy-wire-gen.go internal/di/wire_gen.go
```

预期：生成成功且两次输出一致。

- [ ] **步骤 5：运行快照纵向测试**

使用真实 PostgreSQL：写入 Revision 1 并等待 Store 发布；断开数据库后确认 Store 仍返回 Revision 1；恢复数据库并写入 Revision 2，确认自动发布 Revision 2。等待必须使用带截止时间的轮询辅助函数，不使用固定长时间 `sleep`。

```bash
LLM_PROXY_TEST_POSTGRES_DSN="$LLM_PROXY_TEST_POSTGRES_DSN" \
  go test ./internal/infrastructure/snapshot -run Integration -v
```

- [ ] **步骤 6：全量验证并提交**

```bash
gofmt -w cmd internal
go test ./...
go test -race ./internal/application/gateway/snapshot ./internal/infrastructure/snapshot ./internal/application/controlplane/...
go vet ./...
go build -o /tmp/llm-proxy ./cmd/proxy
git diff --check
git add cmd/proxy internal
git commit -m "feat: integrate gateway snapshot lifecycle"
```

## 三、本计划完成检查点

- RuntimeConfigReader 在可重复读只读事务中加载一个一致 Revision。
- Compiler 拒绝悬空引用、越权作用域和 Phase 1B 多目标路由。
- Snapshot 发布后不可修改，请求 Session 贯穿同一个 Revision。
- Virtual Key 认证和 Alias 解析不查询数据库。
- 轮询、进程内唤醒、失败退避、LKG 与数据库恢复测试通过。
- Gateway 关闭时不创建连接或后台 goroutine。
- 控制面成功响应只保证持久化 Revision；其他实例按轮询窗口最终生效。
- 工作区干净后进入 `2026-08-25-unified-protocol-fake-connector.md`。
