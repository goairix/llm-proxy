# Phase 1B：统一协议内核与 PostgreSQL 控制面实施总计划

> **供执行代理使用：** 必须使用 `superpowers:subagent-driven-development`（推荐）或 `superpowers:executing-plans`，逐任务执行本计划。所有步骤使用 `- [ ]` 复选框跟踪。

**目标：** 按三个可独立验证的里程碑完成 PostgreSQL 多租户控制面、不可变运行时快照、统一推理语义内核、Fake Connector 和 OpenAI/Anthropic 双兼容入口。

**架构：** 保持 DDD 模块化单体和现有透明代理隔离。控制面通过仓储与事务写 PostgreSQL，后台把 Revision 编译为不可变快照，数据面只读取快照并调用统一 Connector；两个兼容协议各自负责边界解析和编码。

**技术栈：** Go 1.25、标准库 HTTP/SSE、Google Wire、GORM、PostgreSQL、gormigrate、UUIDv7、AES-256-GCM、Viper、OpenTelemetry、Zap。

**设计依据：** `docs/superpowers/specs/2026-08-25-unified-protocol-kernel-postgresql-design.md`

---

## 一、为什么拆成三个计划

Phase 1B 同时包含持久化控制面、运行时配置发布和双协议数据面。把全部工作写成一个不可中断的大任务会让数据库、并发和协议问题互相掩盖。因此按依赖顺序拆成三个计划，每个计划完成后都能独立测试和回滚：

1. [PostgreSQL 控制面计划](2026-08-25-postgresql-control-plane.md)
2. [运行时配置快照计划](2026-08-25-runtime-snapshot.md)
3. [统一协议内核与 Fake Connector 计划](2026-08-25-unified-protocol-fake-connector.md)

三个计划在同一个普通分支 `feat/unified-protocol-kernel` 顺序执行。禁止使用 worktree；不得同时并行修改共享 DI、Router、Config 或领域类型。

## 二、执行顺序

### 里程碑 1：PostgreSQL 控制面

- [ ] **步骤 1：确认分支和基线**

```bash
git status --short
git branch --show-current
go test ./...
go build -o /tmp/llm-proxy ./cmd/proxy
```

预期：工作区干净，分支为 `feat/unified-protocol-kernel`，测试和构建通过。分支不存在时才从已合并 Phase 1A 的开发分支创建；禁止 `git worktree`。

- [ ] **步骤 2：完整执行控制面计划**

逐项执行 `docs/superpowers/plans/2026-08-25-postgresql-control-plane.md`，每个任务按 TDD 顺序完成并使用计划中的提交信息提交。

- [ ] **步骤 3：验证里程碑 1**

```bash
go test ./...
go test -race ./internal/application/controlplane/... ./internal/infrastructure/security/... ./internal/interfaces/http/...
go vet ./...
go build -o /tmp/llm-proxy ./cmd/proxy
go build -o /tmp/llm-proxy-migrate ./cmd/migrate
git diff --check
```

预期：全部通过；控制面能在真实 PostgreSQL 上完成资源创建和密钥安全测试；透明代理回归不变。

### 里程碑 2：运行时配置快照

- [ ] **步骤 1：完整执行快照计划**

逐项执行 `docs/superpowers/plans/2026-08-25-runtime-snapshot.md`。所有控制面提交后唤醒行为必须在事务提交之外测试。

- [ ] **步骤 2：验证里程碑 2**

```bash
go test ./...
go test -race ./internal/application/gateway/snapshot ./internal/infrastructure/snapshot ./internal/application/controlplane/...
go vet ./...
go build -o /tmp/llm-proxy ./cmd/proxy
git diff --check
```

预期：全部通过；数据库中断时最后可用快照不丢失；Gateway 关闭时没有数据库连接或后台 goroutine。

### 里程碑 3：统一语义内核与双协议入口

- [ ] **步骤 1：完整执行协议计划**

逐项执行 `docs/superpowers/plans/2026-08-25-unified-protocol-fake-connector.md`。协议 Golden Test 以计划记录的官方接口基线为准，不能用一种协议的 DTO 复用实现另一种协议。

- [ ] **步骤 2：验证里程碑 3**

```bash
go test ./...
go test -race ./...
go vet ./...
go build -o /tmp/llm-proxy ./cmd/proxy
go build -o /tmp/llm-proxy-migrate ./cmd/migrate
go run github.com/google/wire/cmd/wire@v0.7.0 ./internal/di
git diff --check
git status --short
```

预期：全部通过，Wire 可重复生成，工作区仅包含执行过程明确产生且已提交的源文件。

## 三、设计覆盖矩阵

| 设计要求 | 实施位置 | 验收证据 |
| --- | --- | --- |
| 现有透明代理不变 | 三个计划的 Router/回归步骤 | 透明代理端到端、限流、Token、SSE、OTel 全量测试 |
| DDD 仓储和可替换数据库边界 | 控制面任务 2–5 | 架构依赖测试、真实 PostgreSQL Repository 测试 |
| UUIDv7 + PostgreSQL 原生 UUID | 控制面任务 2–5 | Domain Version=7、迁移字段类型集成测试 |
| 多 Organization/Project/VirtualKey | 控制面任务 2、7、8 | 控制面 Application 与 HTTP 测试 |
| 资源化 `/v1` 控制面 API | 控制面任务 8–9 | Router 和纵向控制面测试 |
| 管理令牌隔离 | 控制面任务 6、8 | 固定时间比较和未授权测试 |
| Virtual Key 单次展示、哈希存储 | 控制面任务 6–9 | 安全、Application、数据库明文扫描测试 |
| Provider 凭据信封加密 | 控制面任务 6–9 | 篡改、AAD、轮换、数据库明文测试 |
| 同事务 ConfigRevision | 控制面任务 5、7 | 回滚和并发递增测试 |
| 可重复读配置加载 | 快照任务 1、4 | PostgreSQL 一致性读取测试 |
| 不可变原子快照 | 快照任务 2–3 | Compiler、Session、race 测试 |
| 轮询、唤醒、退避、LKG | 快照任务 5–6 | 状态机和恢复集成测试 |
| 请求热路径不查数据库 | 快照任务 3、协议任务 3 | Gateway Fake 依赖断言、架构测试 |
| 统一文本/图片/工具/结构化语义 | 协议任务 1、5、6 | 双协议 Decoder/Encoder Golden Test |
| 强类型 SSE 事件和背压 | 协议任务 2、4、7 | Event 顺序、race、首事件 Flush、取消测试 |
| Fake Connector 完整纵向链路 | 协议任务 4、9 | 真实 PostgreSQL + 双协议 HTTP 集成测试 |
| 数据库故障不拖垮透明代理 | 控制面任务 9、快照任务 5–6、协议任务 9 | 数据库离线/恢复与透明代理并行测试 |
| 配置文件不热更新，数据库资源动态刷新 | 控制面任务 1、快照任务 5 | 配置测试与 Revision 刷新测试 |

## 四、每个任务的固定执行纪律

每个实现任务都遵循以下顺序，不允许先写生产实现再补测试：

```text
写最小失败测试
  → 运行目标测试并确认因缺失行为而失败
  → 实现满足该测试的最小代码
  → 运行目标包测试
  → 运行架构/并发/集成测试中与本任务相关的部分
  → gofmt + git diff --check
  → 聚焦提交
```

出现测试失败、竞态、SSE 卡住或数据库恢复异常时，停止计划推进并使用 `superpowers:systematic-debugging` 查明根因。不得通过放宽断言、增加固定长时间 sleep 或跳过测试掩盖问题。

## 五、最终完成条件

- [ ] 三个子计划的完成检查点全部满足。
- [ ] 真实 PostgreSQL 迁移、Repository、快照和双协议纵向测试执行通过。
- [ ] 完整 Virtual Key、管理令牌、Provider 明文凭据、真实 DSN 和主密钥未进入 Git、日志、Trace 或 Metric。
- [ ] 原 `/openai/*`、`/anthropic/*` 的路径、Header、限流、统计、Token、SSE 和 OTel 行为未改变。
- [ ] `/v1/chat/completions` 与 `/v1/messages` 使用同一 Project 配置和 Fake Deployment 返回兼容 JSON/SSE。
- [ ] `go test ./...`、`go test -race ./...`、`go vet ./...`、两个二进制 Build、Wire 重生成和 `git diff --check` 全部通过。
- [ ] 使用 `superpowers:requesting-code-review` 自审实现，再使用 `superpowers:verification-before-completion` 复核所有完成声明。
