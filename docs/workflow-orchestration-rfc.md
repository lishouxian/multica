# Workflow 编排 — 「agent 铺轨,引擎行车」设计方案

> Status: Draft (设计阶段,未动工)
> Owner: TBD
> Last updated: 2026-07-11

## TL;DR

- **问题**:multica 今天组织复杂多 agent 流程的唯一方式是 squad leader + prompt 约定(backlog 停车、手动 promote、child-done comment 唤醒)。流转没有引擎保证、每一跳都要付一次完整 LLM 调用、流程不可声明/复用/审计。
- **方案**:引入 Workflow 编排层,定位介于 Dify(纯静态图)和 Claude Code dynamic workflow(纯动态)之间——**planning 动态(LLM 决定建什么节点),transition 确定(节点间流转由引擎执行)**。
- **架构关键**:不建与 issue 平行的 run log 体系。**每个 workflow 节点 = 一个 issue,节点执行 = 现有 task queue,可观测性全部长在 issue 上**。引擎的角色是"issue 树的编译器 + 状态机监督者"。
- **激活休眠资产**:`issue_dependency` 表(migration 001 就存在,至今无任何 query/service 使用)作为运行时 DAG 边存储。
- **两种流转控制**:`gateway`(CEL 表达式,零 LLM 成本,确定性)对应工程控制;`agent_gateway`(LLM 路由决策,但引擎强制 schema + default 分支 + 超时 fallback)对应 prompt 控制。
- **杀手锏动线**:先让 squad 动态跑一次 → 成功后 [Save as workflow] 固化为模板 → 越跑越稳越便宜。流程资产是"录制"出来的,不是画出来的。
- **改动量**:总计约 13–18 人周,分 M0–M3 四期,M0/M1 之间有止损点。
- **最大风险**:① issue 状态副作用分散在至少三条写路径,引擎挂不全会静默卡死——M0 必须先收敛到 service 单一入口;② 进程内 event bus + 多实例部署,引擎推进必须完全靠 DB 幂等,不能依赖事件必达。

---

## 1. 现状评估

### 1.1 已有的编排原语

读 `service/task.go`、`handler/issue.go`、`builtin_skills/multica-squads`、`multica-working-on-issues`、autopilot 与 migrations 后的结论:**multica 已具备 workflow 引擎所需的几乎全部底层原语,但把"编排逻辑"完全放在了 agent prompt 里,没有引擎层保证。**

| 原语 | 现状 | 对应 workflow 概念 |
|---|---|---|
| Issue 状态机 | `backlog→todo→in_progress→in_review→done/blocked/cancelled`;状态变化有服务端副作用(`backlog` 停车,移出即 enqueue assignee,`handler/issue.go` 状态更新路径) | 节点激活/挂起 |
| 父子 issue + 子完成回调 | 子 issue 到 `done` 时平台在父 issue 发 system comment 并 @父 assignee(MUL-2538,`issue_child_done.go`) | 节点完成 → 回调 orchestrator |
| Squad → leader 路由 | squad 是路由对象非 agent;所有工作路由到 leader,leader 靠 briefing + prompt 分解协调 | dynamic orchestrator 节点 |
| 串行链约定 | 子 issue 建成 `backlog` 停车,leader 逐个 promote 为 `todo`(working-on-issues skill 合同) | 顺序边(靠 prompt 遵守) |
| Autopilot | schedule / webhook / manual 触发 → `create_issue` 或 `run_only` | trigger 节点 |
| Issue metadata KV | `waiting_on`、`blocked_reason`、`decision`、`pr_url` 等高信号键 | 节点间数据传递雏形 |
| Task queue | lease、attempt/max_attempts 重试、session resume vs fresh、trigger comment 上下文 | 节点执行层(已完善) |
| Event bus | 进程内同步 pub/sub(`internal/events/bus.go`) | 引擎驱动源 |
| `issue_dependency` 表 | **migration 001 就建了,无任何 query/service 使用,完全休眠** | 现成的 DAG 边存储 |

### 1.2 差距(相对 Dify / n8n)

今天组织复杂流程的唯一方式:issue 指给 squad → leader 被唤醒 → 靠 prompt 协议建 backlog 子 issue、逐个 promote、被 child-done comment 唤醒后继续。本质是 Claude Code dynamic workflow 式的"agent 即引擎"。问题:

1. **流转没有引擎保证。** leader 忘记 promote、数错并行完成数、被打断,链就断了。没有 join(N 个并行子 issue 全 done 才推进)、没有条件路由的确定性执行。
2. **每一跳都要唤醒 leader 跑一轮完整 LLM。** 纯粹的"检查是否该推进"也要付大模型调用的延迟和成本。
3. **流程不可声明、不可复用、不可版本化。** 流程图只存在于 squad `instructions` 和 leader 的上下文里,不能存模板、不能可视化、不能审计"这次 run 走了哪条路"。
4. **节点间数据传递没有契约。** 靠 comment 自由文本和 metadata 约定,下游 agent 自己翻。
5. **人工卡点不是一等公民。** 现在是 `blocked` + `waiting_on` metadata 约定,没有"审批通过自动放行"。

---

## 2. 设计方案

### 2.1 核心哲学:workflow 建在 issue 图之上,不建平行体系

不照抄 Dify 搞独立的 workflow run/node log 实体。multica 的全部可观测面(issue 树 UI、timeline、comment、inbox、PR 联动)都长在 issue 上——**每个节点就是一个 issue,节点执行就是现有 task queue,人工介入就是现有 comment/status**。否则出现两套真相(run log vs issue 状态),这正是 Dify 用在工程团队里最难受的地方。

### 2.2 定位:Dify 与 Claude Code 之间的 hybrid

- Dify/n8n:图**静态**声明,执行确定 → 表达力受限,开放性任务僵硬。
- Claude Code dynamic workflow:图由 orchestrator **完全动态**生成 → 灵活但不可靠、不可审计。
- **multica 取中间态:planning 动态(LLM 决定建什么节点、怎么连),transition 确定(节点建好后,promote/join/失败路由由引擎执行,不再依赖 prompt 遵守)。**

产品文案一句话:**"Agent 负责画图,引擎负责开车。"**

### 2.3 数据模型

```sql
workflow_definition (id, workspace_id, name, version, graph JSONB, status, created_by, ...)
workflow_run       (id, definition_id NULLABLE,   -- NULL = 纯动态铺出来的 run
                    root_issue_id, status, context JSONB, deadline, ...)
workflow_node_run  (id, run_id, node_key, issue_id, task_id,
                    status, output JSONB, attempt,
                    UNIQUE(run_id, node_key, attempt))   -- 幂等关键
-- 边:静态部分存 definition.graph;运行时实际生效的边落到已休眠的 issue_dependency 表
```

所有新表带 `workspace_id` 并在每条查询过滤(多租户硬规则)。

### 2.4 Graph DSL:节点类型钉死 6 种,不做 BPMN

`agent` / `agent_gateway` / `gateway` / `human` / `trigger` / `subworkflow`。示例:

```yaml
name: feature-delivery
nodes:
  plan:      { type: agent, assignee: "@architect", output_schema: { subtasks: array } }
  implement: { type: agent, assignee: "@coder",
               prompt: "按 {{nodes.plan.output.subtasks}} 实现", retry: {max: 2} }
  gate_ci:   { type: gateway,                       # 工程控制的流转
               when: 'pr.checks_conclusion == "passed"',
               on_true: review, on_false: fix }
  review:    { type: agent_gateway, assignee: "@reviewer",   # prompt 控制的流转
               output_schema: { verdict: enum[approve, revise, escalate] },
               routes: { approve: ship, revise: implement, escalate: human_review },
               default: human_review }              # enum 漂移降级,不崩
  human_review: { type: human, assignee_role: admin }        # inbox 审批卡
  ship:      { type: agent, assignee: "@ops" }
edges:
  - plan -> implement
  - implement -> gate_ci
policies: { loop_max: 3, run_deadline: 48h }
```

两种流转控制方式:

- **工程控制** = `gateway`:CEL/JSONLogic 表达式,输入是上游节点 structured output、issue metadata、PR 状态(`state`/`checks_conclusion` 链路现成)。零 LLM 成本、毫秒级、确定性。UI 上不暴露表达式语言,呈现为"当 review 的 verdict = approve 时"的下拉选择。
- **prompt 控制** = `agent_gateway`:路由决策交给一次 LLM 调用(可指定小模型),但引擎强制 output schema、强制 `default` 分支、强制超时 fallback——**决策可以不确定,流转本身必须确定**(与"enum drift downgrades, not crashes"同一精神)。

### 2.5 引擎实现:事件驱动 reducer,不引入 Temporal

1. **驱动源**:订阅现有 event bus(`issue:status_changed`、`task:completed`、PR webhook 事件)。
2. **推进函数**:纯函数 `advance(graph, runState, event) -> []Action`;Action 为"创建 issue / promote backlog→todo / enqueue task / 完成 run"。纯函数可单测穷举全部转移(复用 `autopilot_test.go` 风格)。
3. **执行**:Action 在事务里落库,`workflow_node_run` 唯一约束保证幂等——事件重放、并发唤醒不会重复推进。
4. **兜底**:`scheduler/manager.go` 已有 tick 框架,加 workflow reconcile tick 处理丢事件、超时节点、run deadline。
5. **触发**:autopilot `execution_mode` 增加第三种 `run_workflow`——workflow 是 autopilot 的自然泛化,schedule/webhook/manual 三种触发器直接继承。

### 2.6 节点间数据流转

- agent 节点完成时写结构化输出:新增 `multica task output set --json '{...}'`(或复用 issue metadata),落 `workflow_node_run.output`,`CompleteTask` 路径校验 schema。
- 下游 prompt 模板插值:`{{nodes.plan.output.subtasks}}`、`{{run.context.branch}}`。
- run 级 `context JSONB` 存全局变量,gateway 表达式可读。

### 2.7 动态铺轨:squad leader 升级为"有护栏的 orchestrator"

新增 CLI(须同步更新 builtin skill,CLAUDE.md 硬规则):

```bash
multica workflow spawn-node <run-id> --key fix_login --assignee @coder \
    --after implement --join all --on-failure human_review
```

leader 运行时动态生成后继节点和边——**但边一旦建立,后续 promote/join/失败路由全部由引擎接管**。leader 不再被每个 child-done 唤醒去"数数",只在 join 失败或 escalate 时被重新唤醒。这砍掉当前最大成本项:状态盯梢型 LLM 调用。

---

## 3. 产品方案与用户动线

### 3.1 对象命名(用户视角只有三个概念)

| 对象 | 用户叫法 | 本质 |
|---|---|---|
| **Workflow** | 剧本 / 流程 | 可复用的图定义,带版本 |
| **Run** | 一次执行 | 一棵带引擎监督的 issue 树 |
| **Step** | 步骤 | 一个 issue(用户不需要学"node") |

路由 `/{slug}/workflows`(session route,单词符合规范),sidebar 与 Autopilots 平级。心智模型教育一句话:**Autopilot 决定"什么时候开始",Workflow 决定"开始之后怎么走",Squad 决定"谁来干活"**——三者正交。

### 3.2 动线 A · 创建剧本:「说出来,agent 画出来」

第一入口是对话式而非画布:

- **① Describe it(主推)**:用户描述流程 → planner agent 生成图草稿 → 右侧实时渲染只读流程图预览 → 结构化表单微调。
- **② From template**:模板库(Bug 分诊、PR 审查流水线、周报、发版…)。
- **③ From scratch**:结构化表单 + 图预览。

**v1 不做 n8n 式自由拖拽画布**:左侧步骤列表(增删排序、缩进表并行),右侧自动布局只读 DAG 预览。每个 Step 只有五个配置项:谁来做 / 做什么(prompt,支持上游变量插值补全)/ 产出什么(输出字段)/ 之后去哪(默认顺连,可按输出值分支)/ 出错怎么办(重试 N 次 / 转人工 / 备用分支,默认转人工)。

### 3.3 动线 B · 触发 Run:四个入口

1. **手动**:workflow 详情页 [Run],弹窗填入口参数。
2. **定时/Webhook**:autopilot `run_workflow` 模式,现有 trigger 配置 UI(`trigger-config.tsx`)、webhook deliveries 面板原样继承。
3. **从 issue 发起(高频)**:issue 详情 `⋯` 菜单 → "Run workflow on this issue" → 该 issue 成为 run 根,步骤作为子 issue 长出。
4. **对话发起**:chat 里说"按发版流程处理 v0.3.2",agent 用 CLI 起 run。

### 3.4 动线 C · 观察运行:一个 Run 两种视角,零新页面

Run 主视图 = 根 issue 详情页 + 顶部 Run Banner:

```
┌─────────────────────────────────────────────────────┐
│ ▶ Weekly Release · 运行中 · 步骤 3/6 · 已运行 42min      │
│ [●回归测试✓]→[●changelog✓]→[◐打tag·等待审批]→[○公告]    │
│                              [查看流程图] [暂停] [Eject] │
└─────────────────────────────────────────────────────┘
```

- 步骤条可点,跳对应子 issue;[查看流程图] 展开 DAG 叠加实时状态(绿=完成、蓝=运行中带 agent 头像、黄=等人、灰=未激活、红=失败),**高亮实际走过的路径**。
- 子 issue 详情页有细 banner:"属于 Weekly Release run #12 · 第 3 步 · 上游输出 ▸"——解决"agent 靠 comment 传话,人看不懂上下文从哪来"的痛点。
- Workflows 列表页显示每个剧本的进行中 runs、近 10 次成功率、平均耗时、平均 token 成本。
- 关键节点(完成、失败、等审批)推 inbox,与现有 issue 通知同一心智,用户不需要盯。

### 3.5 动线 D · 人工卡点:审批就是一张 inbox 卡片

- human step 激活 → 指定人/角色收到 inbox 卡片,**卡片上直接 [批准]/[打回]**,不必进 issue。
- 打回可附理由,理由作为 `{{rejection_reason}}` 流回上游重跑。
- 超时策略在剧本定义:24h 无响应 → 升级 admin / 自动批准 / 挂起。
- 另外**任何边可标"需人工放行"**——不新增步骤,流转前多一道 inbox 确认。轻量/重量卡点分开。

### 3.6 动线 E · 干预与失败:随时能接管

运行中任何 step(=issue)上可以:

- **改 assignee**:agent 干不动就换 agent 或换成人——引擎只等 issue 到 `done`,**人和 agent 在流程里完全同构**。
- **重试 / 跳过**:失败 step 一键重跑(fresh session 或续 session);或标记跳过按"视为完成"推进(确认弹窗写明下游拿到空输出)。
- **暂停 Run**:停止推进,在跑的 task 跑完即止;恢复后从暂停点继续。
- **Eject(逃生舱)**:一键降级为普通 issue 树,引擎放手,人工接管,run 记录保留为"已弹出"。**必须永远存在且可用**——失控时保住信任的按钮。

失败默认策略是**转人工而不是终止**:失败 step 变红 → 生成 assignee 为发起人的处理卡片 → 人处理完标 done → 流程继续。

### 3.7 动线 F · 动态铺轨:看着 agent 现场画图

剧本可含"由 agent 规划"的展开步骤(planner step):run 到达时 leader 现场决定建哪些子步骤(`spawn-node` CLI),**流程图上实时看到新节点"长"出来**(虚线=规划中,实线=进入引擎管辖)。铺完轨 leader 退场,流转由引擎确定性执行。

### 3.8 动线 G · 复盘与固化(杀手锏)

1. Run 结束页:每步耗时、token 成本、重试次数、人工介入点、产出链接(PR、文档)。
2. 对动态铺轨居多的成功 run 提供 **[Save as workflow]**:把实际走过的图固化为剧本草稿,具体值自动参数化。
3. 飞轮:**随手让 squad 动态做 → 做得好固化 → 固化后越跑越稳越便宜**(静态图省掉动态规划的 LLM 调用)。流程资产是"录制"出来的——Dify 给不了,因为它没有"先动态跑一次"的能力。

### 3.9 刻意不做

- 自由拖拽画布(v1)——动线 G 成立后多数图不是人画的。
- 通用表达式语言暴露给用户——UI 是下拉选择,底层才是 CEL。
- 独立 run 日志页——可观测性全部长在 issue 上。
- 跨 workspace 编排(远期)。

---

## 4. 改动量与涉及模块

### 4.1 总量

| 阶段 | 内容 | 后端 | 前端 | 工作量* |
|---|---|---|---|---|
| **M0** | 状态写路径收敛 + dependency 自动 promote | ~400 行 + 收敛重构 | 0 | 2 人周 |
| **M1** | 引擎内核 + Run Banner + 从 issue 发起 | ~2500–3500 行 | ~2000–3000 行 | 4–6 人周 |
| **M2** | 创建器 + 审批 + 干预 + autopilot 集成 | ~2000 行 | ~3000–4000 行 | 5–7 人周 |
| **M3** | 动态铺轨 + 固化 + 统计 | ~1500 行 | ~1500 行 | 3–4 人周 |

\* 含测试(本仓库 Go 侧测试普遍是实现的 1–2 倍行数,已计入)。总计 **13–18 人周**。M0 独立有价值,M0/M1 之间是止损点。

### 4.2 后端模块

| 模块 | 改动 | 量级 |
|---|---|---|
| `server/migrations/` | 3 张新表 + autopilot `execution_mode` CHECK 扩表 + `issue_dependency` 加索引 | 4–5 个迁移,小 |
| `server/pkg/db/queries/` | 新增 `workflow.sql`;`issue.sql` 加 dependency 查询;`make sqlc` | 中 |
| `server/internal/service/` | **新增 `workflow.go`(advance reducer + actions),最大新增面**;`task.go` `CompleteTask` 挂输出采集钩子 | 大 |
| `server/cmd/server/` | 新增 `workflow_listeners.go`(照抄 `autopilot_listeners.go` 模式);scheduler 加 reconcile tick | 中 |
| `server/internal/handler/` | 新增 workflow handler;**`issue.go` 状态变更处插引擎通知点——最高危改动**(3000+ 行,两条更新路径都要插) | 中,风险高 |
| `server/cmd/multica/` | 新增 `cmd_workflow.go`(照 `cmd_squad.go` 骨架) | 中 |
| `builtin_skills/` | 新增 `multica-workflows` skill + source map;改 `multica-working-on-issues`、`multica-squads`(promote 语义变化) | 小但**强制** |
| `reserved_slugs.json` | 加 `workflows` + `pnpm generate:reserved-slugs`,双文件同 commit | 微小但必须,CI 拦 |

### 4.3 前端模块

| 模块 | 改动 |
|---|---|
| `packages/core/` | API 端点 + **zod schema(全部走 `parseWithFallback`)**;TanStack Query hooks(key 带 `wsId`);WS 失效映射 |
| `packages/views/` | 新增 `workflows/` 域(列表、详情、Run Banner、只读 DAG、步骤条);`issues/` 挂 banner + 菜单入口;`inbox/` 审批卡(M2);`locales/` 中英文案(过 conventions glossary) |
| `apps/web/` + `apps/desktop/` | 各自路由/tab 接线,量小但两端都不能漏 |
| `e2e/` | run 生命周期用例 |

只读 DAG 图是前端唯一"新物种"(项目无图渲染库,需引入 dagre 类布局 + 自绘 SVG,进 pnpm catalog),是前端估算方差最大的一块。

---

## 5. 变更风险(按严重度)

### 🔴 R1 状态写路径分散 —— 引擎"漏接",最大架构风险

issue 到 `done` 至少三条路径:`handler/issue.go` 两个更新入口、GitHub webhook merge close-intent 自动推进。`notifyParentOfChildDone` 今天就要多处手动调用——既有债务的症状。**引擎挂不全 → workflow 静默卡死在某个节点,比 crash 更糟。**

对策:动工前先做小型收敛重构——"issue 状态变更 + 副作用"下沉到 service 层单一入口,所有 handler/webhook 路径改调它,引擎只挂这一个点。约 1 人周,有回归风险(backlog promote、取消清任务、child-done 通知都在这条路上,`issue_child_done_test.go` 等覆盖较厚)。**计入 M0,且无论 workflow 做不做都在还债。**

### 🔴 R2 幂等与竞态 —— 多实例下重复推进

realtime 有 Redis relay、scheduler 有并发 claim 测试 → 生产多实例;而 `events.Bus` 是**进程内**总线。两个并行子 issue 在不同实例同时 `done`,两个引擎实例会同时推进同一 join。

对策(设计前提,非可选项):推进完全靠 DB——`workflow_node_run(run_id, node_key, attempt)` 唯一约束 + run 行锁(`SELECT ... FOR UPDATE`)+ reconcile tick 兜底丢事件。task queue 的 lease/attempt 已验证此模式,照搬;测试参考 `concurrent_claim_test.go`、`task_complete_race_test.go`。

### 🟠 R3 双重推进 —— 引擎和 leader 打架

M0 后存量 leader prompt 仍教它手动 promote,引擎也在 promote → 重复推进/重复建子 issue。对策:builtin skills 与引擎行为**同 PR 更新**;引擎 promote 写 system comment 声明来源;promote 幂等(backlog→todo 二次执行无害)。

### 🟠 R4 API 兼容 —— 旧 desktop 客户端

workflow 字段进入 issue 响应;#2143/#2147/#2192 三次事故都在这。对策:新字段全 optional、zod schema + fallback 同 PR、banner 缺字段整体不渲染而非白屏。仓库有现成流程,风险在执行纪律。

### 🟠 R5 流程失控 —— 环、孤儿、成本

- dependency 成环 → 节点永远 backlog。对策:建边时 DB 内 DFS 拒环 + reconcile 检测"run 活着但无可推进节点"报警。
- 根 issue 删除/cancel、agent archive、squad 删除 → run 悬空。每个引用定义级联行为(建议统一降级为 eject)。
- planner 铺轨 + 循环 = token 放大器。loop 上限、run deadline、spawn-node 配额必须在引擎里,不留给 prompt 自律。

### 🟡 R6 其余清单

- autopilot `execution_mode` CHECK 约束迁移:先发容错代码再发迁移,避免旧代码读到新值的窗口。
- 多租户:新表全部 `workspace_id` 过滤,漏一条即越权,review 重点。
- i18n:文案过 conventions.mdx glossary 与中文 voice guide。
- `issue_dependency` 是 001 时代表结构,启用前确认字段够用(`created_by`、边类型),宁加列勿复用语义。
- 性能:issue 列表/详情多一次 run 归属 join,勿退化现有 keyset 分页。

---

## 6. 实施路径

| 阶段 | 交付动线 | 内容 |
|---|---|---|
| **M0** | (无 UI) | ① 状态变更收敛到 service 单一入口(R1);② 激活 `issue_dependency`:依赖全 done 自动 promote backlog→todo。squad leader 立即受益,验证引擎地基 |
| **M1** | B3 + C | "Run workflow on this issue" + 顺序/并行/join + Run Banner + 步骤条;gateway 与图编辑器后置,模板用 3 个内置官方剧本 |
| **M2** | A + D + E | Describe-to-workflow 创建器、human 卡点 + inbox 审批、重试/跳过/Eject、autopilot `run_workflow` |
| **M3** | F + G | planner step 动态铺轨、Save as workflow 固化、成本/成功率统计 |

M1 验收场景用团队自己的真实流程(发版、bug 分诊)dogfood。

**建议的第一个 PR**:R1 的状态变更收敛重构——独立可 review、独立可回滚,且无论 workflow 最终做不做都在偿还已存在的技术债。

---

## 7. 总结

multica 不需要"再造一个 Dify"。它需要把已经存在于 prompt 约定里的编排协议(backlog 停车、promote 推进、child-done 回调、metadata 传值)**下沉为引擎保证**,再把 squad leader 从"全程盯梢的执行者"升级为"只负责规划的铺轨者"。休眠的 `issue_dependency` 表是最好的起点——最初的设计者已经预留了这条路。
