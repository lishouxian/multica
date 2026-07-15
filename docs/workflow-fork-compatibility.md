# Workflow Fork — CLI 零修改与上游兼容性契约

> Status: Draft(与 `workflow-fork-plan.md` 配套;plan 讲怎么做,本文讲**边界在哪、上游变了怎么办**)
> Owner: TBD
> Last updated: 2026-07-11

## 结论(先说答案)

在以下两条硬约束下,workflow 功能**可以完整实现**,且本文档给出可检查的执行契约:

1. **multica CLI 零修改**:不新增子命令、不改动任何现有命令的行为与参数;
2. **兼容后续上游变更**:所有改动 additive-only,上游触碰点封顶 5 处;每次同步上游的预期冲突为零到个位数行;上游任何单点变更都有已设计好的降级通道;上游推出官方 workflow 时有明确退出路径。

可行的根本原因:workflow 引擎需要的所有能力,都落在 multica **最稳定的产品语义层**上(issue 生命周期、metadata、任务派发),而不是易变的实现层(handler 内部结构、CLI 代码、前端组件)。引擎用"轮询观察"而非"挂钩侵入"的方式消费这些语义,所以上游怎么重构内部实现,只要产品语义不变,fork 不用动。

---

## 1. 两条约束的精确定义

### 1.1 CLI 零修改

- `server/cmd/multica/` 目录下**不新增、不修改任何文件**;
- agent 与 workflow 系统的全部交互只用两种通道:
  - **已有 CLI 命令的纯调用**(不是修改):`multica issue metadata set`、`multica issue get` 等;
  - **`curl + $MULTICA_TOKEN`**:daemon 为每个 agent 会话注入 task 级凭证(`daemon/types.go` AuthToken,MUL-3292),agent 用它直接访问 fork 新增的 `/api/workflows/*` 端点;端点接受 task token 认证是 fork 自己的新 handler 代码,与 CLI 无关;
- 教学成本走 builtin skill 文档(纯新增文件),不走 CLI help。

### 1.2 上游兼容

- **additive-only**:新迁移(`900_` 号段)、新表(`wf_` 前缀)、新 Go 包(`service/wfengine/`)、新 handler 文件、新前端域(`views/workflows/`);
- **上游文件触碰点封顶 5 处、每处 ≤3 行**(见 §4 清单,超出即 review 打回);
- **不挂上游写路径**:引擎靠 reconcile 轮询读取状态,上游 handler/service 的任何内部重构都不影响 fork;
- 同步节奏:定期 `git fetch upstream && git merge`(或 rebase),冲突预期集中在 5 个接线点。

---

## 2. 依赖的上游接口契约表

fork 依赖的每一个上游接口,按稳定性分级,并预设"上游变了怎么办":

| # | 依赖的上游能力 | 层级 | 稳定性 | 上游变更时的应对 |
|---|---|---|---|---|
| 1 | **Issue 生命周期语义**:创建时 `status=todo` + agent assignee → 自动 enqueue;`done`/`cancelled` 是终态 | 产品语义 | 极高(产品核心合同,文档与 builtin skill 双重承诺) | 引擎只消费"todo 触发、done 完成"两条;若上游加新状态,不影响(引擎只判终态);若改名,适配点集中在 `wfengine` 的 2–3 条查询 |
| 2 | **Issue metadata**:`wf_out.*` 标量键写入/读取(键规则允许点号、值标量、50 键、8KB) | 产品语义 | 高(V1 表面显式冻结,MUL-2017;DB CHECK 双层承诺) | 若收紧/移除:降级到**comment 围栏块通道**(引擎解析节点 issue 最后一条 comment 的 ` ```wf_output ` JSON 块)——已设计备查,纯引擎侧改动 |
| 3 | **`MULTICA_TOKEN` task 凭证注入**(MUL-3292) | daemon 机制 | 中高(安全设计的核心,移除概率低) | 只影响"agent 自助编排/发起 run";节点执行完全不依赖它(靠 assignee 触发)。降级:编排改为 agent 产出 JSON、人经 UI/curl 提交 |
| 4 | **Issue 表结构**:`status`、`metadata`、`assignee_type/assignee_id`、`description` 列(引擎经 sqlc 直读) | DB schema | 高(核心表,上游迁移必带兼容处理) | 依赖收敛在 fork 自己的 `wf.sql` 查询文件里,上游列变更时单文件适配 + `make sqlc` |
| 5 | **Issue 创建/更新的 service 层入口**(引擎创建节点 issue 时调用) | Go API | 中(内部 API 可能重构) | 引擎经由与 handler 相同的 service 函数创建 issue;若签名变化,适配点只在 `wfengine` 一处。保底:引擎改调自身进程内的 HTTP 端点(语义级接口) |
| 6 | **scheduler tick 框架**(reconcile goroutine 挂载点) | 接线点 | 中 | 1 行接线;若框架重构,fork 自起一个 `time.Ticker` goroutine,零依赖 |
| 7 | **Chi router 注册**(新 handler 挂载) | 接线点 | 高 | 1–2 行接线;路由框架变更概率极低 |
| 8 | **workspace 鉴权中间件**(新端点复用) | 中间件 | 高 | 若签名变化,fork handler 单点适配 |

**刻意不依赖的上游能力**(常见误判点):event bus 事件(不订阅——轮询代替,免疫上游事件重构)、`CompleteTask` 钩子(不挂)、`issue_dependency` 表(不用)、autopilot 内部(不改,如需定时触发让 autopilot `run_only` 的 agent 调 run API)、前端 issues/inbox 组件(不改,workflow 页是独立观察面)。

---

## 3. 上游变更情景推演

| 情景 | 对 fork 的影响 | 处置 |
|---|---|---|
| 上游重构 issue handler / service 内部 | 无(引擎不挂写路径,轮询只看结果状态) | 无需动作 |
| 上游修改/重写 CLI | 无修改可冲突;`metadata set` 若变 | 切换 comment 围栏块通道(§2-2) |
| 上游收紧 metadata 规则 | `wf_out.*` 写入受限 | 同上,切围栏块通道 |
| 上游改 issue 状态机枚举 | 引擎判定条件失效 | `wfengine` 内 2–3 处常量适配,半天级 |
| 上游变更 MULTICA_TOKEN 机制 | agent 自助编排受影响,执行不受影响 | 编排降级为人提交;或适配新凭证机制 |
| 上游未来占用 `900_` 迁移号段 | 撞号 | 内部部署可控:重命名 fork 迁移并手工对齐 schema_migrations 记录 |
| 上游新增表名 `workflow_*` | 无(fork 用 `wf_` 前缀) | 无需动作 |
| **上游推出官方 workflow 功能** | fork 功能与之并存或冲突 | 退出路径:冻结 fork 功能 → 一次性脚本把 `wf_definition.graph`、`wf_run.node_state` 迁到官方 schema(fork 的命名/概念已刻意对齐上游 RFC,迁移是数据搬运而非概念翻译)→ 删除 fork 代码。fork 期间积累的流程定义是资产,不是沉没成本 |
| 上游大版本升级(Go/React/依赖) | 与普通 fork 同步一致 | fork 新代码遵守上游的 catalog/工具链,跟随升级 |

---

## 4. 上游文件触碰点封顶清单

允许修改的上游文件**只有以下 5 处**,每处 ≤3 行,全部是注册/接线性质。任何超出此清单的上游文件改动,code review 必须打回:

| # | 文件 | 改动 | 行数 |
|---|---|---|---|
| 1 | server 路由注册处 | 挂载 `/api/workflows/*` handler | 1–2 |
| 2 | scheduler 启动处 | 起 reconcile goroutine | 1 |
| 3 | sqlc 配置 | 纳入 `wf.sql` | 1–2 |
| 4 | `apps/web` 路由表 | 加 `/{slug}/workflows` 页面路由 | 1–3 |
| 5 | `apps/desktop` 路由表 | 同上 | 1–3 |

其余全部为**新增文件**:`900_workflow_fork.up/down.sql`、`server/pkg/db/queries/wf.sql`、`server/internal/service/wfengine/`、`server/internal/handler/wf.go`、`packages/views/workflows/`、`packages/core` 内新增的 API/schema/hooks 文件、builtin skill 目录。

注:`reserved_slugs.json` + 生成的 `reserved-slugs.ts` 需加 `workflows`——这两个文件是数据文件,上游同 key 冲突时取并集即可,不计入触碰点风险。

## 5. 同步后的验证清单

每次合并上游后跑:

1. `pnpm typecheck && pnpm lint`(fork 前端代码随上游类型变化的第一道网);
2. `make test` + fork 专属的 `wfengine` 单测(条件求值、推进逻辑纯函数,穷举覆盖);
3. **一条 e2e 冒烟**:提交内置模板 → 发起 run → mock agent 写 `wf_out.*` → 标 done → 断言条件边选路正确、下游 issue 创建、run 终态正确。这条 e2e 就是"上游语义没变"的活体检测——它红了,说明 §2 契约表里某一项被上游动了,按表内预案处置。

---

## 6. 结论重申

- **CLI 零修改**成立:节点 I/O 走已有 metadata 命令(纯调用)+ description 注入;agent 侧 API 访问走 daemon 已注入的 `MULTICA_TOKEN` + curl;保底还有 comment 围栏块通道,连"依赖已有 CLI 命令"都可去除。
- **上游兼容**成立:依赖全部落在产品语义层(§2 契约表),实现层零挂钩;触碰点封顶 5 处;每种上游变更有预案(§3);官方 workflow 出现时有资产可迁的退出路径。
- 两条约束不是对功能的阉割,而是逼出了更稳的架构:轮询引擎、语义级依赖、信号式 I/O——这些选择即使没有约束也值得做。
