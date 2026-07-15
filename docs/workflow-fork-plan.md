# Workflow Fork 版 — 内部低成本实现方案

> Status: Draft(内部 fork 专用,待评审后动工)
> Owner: TBD
> Last updated: 2026-07-11
> 关系说明:本文档是 `workflow-orchestration-rfc.md`(上游产品级设计,Frozen for M0/M1)的**内部 fork 裁剪版**。RFC 保留为长期方向参考;本方案面向"企业内部急需、低成本、可随时跟上游同步"的现实约束。命名与字段尽量对齐 RFC,便于未来上游推出官方 workflow 时做数据迁移而非概念翻译。

## TL;DR

- **背景**:multica 是开源产品,上游未来可能自己长出 workflow 功能;我们内部现在就需要,所以在 fork 上用最低成本实现,并把与上游的代码纠缠压到最小。
- **需求**:① 图结构(节点+边),边代表流转条件,节点有输入输出并执行任务;② UI 上能清楚看到每个节点在做什么、流转顺序如何;③ 支持编排,前期弱要求——agent 编排或手写定义即可。
- **核心模型**:`wf_definition`(剧本,图的 JSON)+ `wf_run`(一次执行)两个新实体;**节点在被激活的那一刻才实例化为 issue**(惰性创建)。issue 承担人机协作与执行,workflow 实体承担图结构、条件流转、节点传值与全局进度。
- **三条成本原则**:只增不改(additive-only)、轮询优先于挂钩(poll over hook)、复用而非新建执行层(issue + task queue + metadata)。
- **工期**:约 3–4 人周;上游文件被触碰处预计仅 4 处、每处 1–3 行。

---

## 1. Fork 的三条成本原则

fork 的长期成本不是写代码,是每次同步上游时解冲突。所有取舍由以下三条原则决定:

1. **只增不改(additive-only)**:所有新代码放新文件/新表/新包;对上游文件的修改压到个位数行(路由注册、goroutine 启动等一行级接线)。
2. **轮询优先于挂钩(poll over hook)**:上游 RFC 里最贵的前置工程是"把 issue 状态写路径收敛到单一入口"(副作用分散在 handler 两处 + GitHub webhook 三条路径)。fork 版**直接不做**——引擎不挂进上游写路径,用 5–10 秒的 reconcile 轮询扫描活跃 run、读 issue 当前状态、推进图。内部规模(同时活跃 run 为个位数)下轮询无感,且零上游侵入。
3. **复用而非新建执行层**:节点执行 = 现有 issue + task queue;节点输出 = 现有 issue metadata(`multica issue metadata set`);节点完成判定 = issue 到 `done`。不碰 `CompleteTask`。

**CLI 零修改保证**:multica CLI 一行不改,包括不新增子命令。两个交互点都走现成通道——

- **节点输出**:`multica issue metadata set` 是上游已有命令(`cmd_issue_metadata.go`),我们只是调用,不是修改;
- **agent 调 workflow API**(编排、发起 run):daemon 会向每个 agent 会话注入 task 级凭证 `MULTICA_TOKEN`(`daemon/types.go` AuthToken,MUL-3292),agent 直接 `curl -H "Authorization: Bearer $MULTICA_TOKEN"` 访问新增的 `/api/workflows/*` 端点即可;新 handler 接受 task token 认证是我们自己的新代码。内部部署 server 地址固定,直接写进 skill 文档。
- 保底通道(可选):若某 runtime 里连 CLI 都不可用,引擎可改为从节点 issue 的最后一条 comment 解析 ` ```wf_output ` 围栏 JSON 块作为输出——纯引擎侧约定,同样零 CLI 依赖。v1 不启用,记录备查。

---

## 2. 核心模型:两个实体 + 惰性实例化

```text
wf_definition(剧本)          ← 静态实体:图的 JSON(节点+条件边),可复用
      │  发起一次执行
      ▼
wf_run(一次执行)             ← 动态实体:这次跑到哪、各节点状态、全局上下文
      │  引擎推进到某个节点时
      ▼
issue(该节点的"工作台")       ← 惰性创建:节点被激活的那一刻才建 issue
```

运转循环:

1. 定义里的节点只是 JSON 规格(谁做、prompt 模板、输出字段),**不是 issue**;
2. run 启动 → 引擎找到入口节点 → 此刻为它创建 issue(`status=todo`,指派给节点声明的 agent 或人)→ 上游现有机制自动 enqueue agent 开工;
3. agent/人在 issue 里干活(评论、PR、接管都是现有协作面),结束时把结构化输出写进 issue metadata,issue 标 `done`;
4. 引擎轮询发现 done → 读 output → 逐条求值该节点出边条件 → 激活后继节点(创建新 issue,prompt 中插值上游输出);
5. 循环往复,无可激活节点时 run 结束。

**为什么惰性创建而不是预建整棵 issue 树:**

- 条件分支意味着有些节点永远不会跑,预建会留下僵尸 issue;
- 下游 issue 的描述依赖上游输出插值,不等上游跑完写不出来;
- 图(workflow 页)负责全貌与未来,issue 列表只含真实发生的工作,两者各司其职。

**分工总结:**

- issue 承担它擅长的一切:人机协作面、执行触发(todo 即开工)、完成判定(done 即节点完成)——执行层零新代码;
- workflow 实体承担 issue 不会的:图结构、条件流转、节点间传值、全局进度——全部在新表 JSONB 里,与上游代码零纠缠;
- 人和 agent 天然同构:human 节点就是指派给人的 issue,引擎同样等它 done,不需要单独的审批系统。

---

## 3. 数据模型

两张新表,**迁移文件用 `900_` 起头的独立号段**,永不与上游未来的 `119_`、`120_`… 冲突;表名用 `wf_` 前缀,与上游未来可能的 `workflow_` 命名错开。

```sql
-- 900_workflow_fork.up.sql
CREATE TABLE wf_definition (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    graph JSONB NOT NULL,          -- 节点+边全在这,不拆表
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE wf_run (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    definition_id UUID REFERENCES wf_definition(id),
    status TEXT NOT NULL DEFAULT 'running',   -- running / paused / done / failed / stopped
    context JSONB NOT NULL DEFAULT '{}',      -- run 级变量(入口参数等)
    node_state JSONB NOT NULL DEFAULT '{}',   -- {node_key: {status, issue_id, output}}
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

刻意的简化(相对上游 RFC):

- **node_run 不单独建表**,整个 run 的节点状态收在 `node_state` JSONB;引擎每 tick 对单个 run 读-算-写整行,乐观锁用 `updated_at`。内部单实例部署无多实例竞态,省掉整套唯一约束幂等工程。
- 不激活 `issue_dependency` 表,边只存在 graph JSON 里——少一次上游表结构纠缠。
- 所有新表带 `workspace_id` 并在每条查询过滤(多租户硬规则不豁免)。

---

## 4. 图定义 Schema(agent 可直接生成)

```json
{
  "nodes": [
    { "key": "triage",  "title": "分诊定位",  "executor": "agent",
      "assignee": "bug-triager",
      "prompt": "定位 {{run.context.bug_url}} 的根因,输出 severity 和 module",
      "outputs": ["severity", "module"] },
    { "key": "fix",     "title": "修复",     "executor": "agent", "assignee": "coder",
      "prompt": "修复该 bug。模块:{{nodes.triage.output.module}}" },
    { "key": "confirm", "title": "人工确认", "executor": "human", "assignee": "user:张三" }
  ],
  "edges": [
    { "from": "triage", "to": "fix",
      "when": { "field": "nodes.triage.output.severity", "in": ["high", "urgent"] } },
    { "from": "triage", "to": "confirm", "when": "default" },
    { "from": "fix",    "to": "confirm" }
  ]
}
```

约定:

- **节点**:`key`(图内唯一)、`title`、`executor`(`agent` | `human`,将来加 `program` 是纯增量)、`assignee`、`prompt`(支持 `{{run.context.*}}` 与 `{{nodes.<key>.output.*}}` 插值)、`outputs`(声明输出字段,供下游引用与 UI 展示;**每个字段是标量**,落地为 issue metadata 的 `wf_out.<field>` 键,见 §5)。
- **边即条件**:`when` 是极简 JSON 条件——`eq` / `in` / `exists` / `default` 四种,不引入 CEL;一个 ~50 行的 Go 求值函数覆盖。多条出边按声明顺序求值,`default` 兜底;无边命中且无 default → 该分支自然终止。
- **join(多入边)**:一个节点的全部入边来源节点都到达终态且至少一条边条件满足时激活。v1 语义即 `all_succeeded`(与上游 RFC §2.8.1 对齐);上游 issue 被 cancel 时 run 标 `failed` 并通知发起人,不带病推进。
- **环**:创建/更新 definition 时 DFS 校验拒环。v1 不支持循环节点,打回重做用"新边指回原节点 + 次数上限"留到 v2。

---

## 5. 节点 I/O:全部复用现有机制,零上游改动

- **输入**:引擎创建节点 issue 时,把插值后的 prompt 写进 issue description——agent 打开 issue 即见完整上下文,**输入不占用 metadata**;
- **输出**:上游 metadata 的硬约束是**值只能是标量**(string/number/bool,对象/数组被 handler 400 拒绝)、每 issue ≤50 键、blob ≤8KB(`handler/issue_metadata.go`)。因此输出采用**扁平化前缀键**(key 规则允许点号):

  ```bash
  multica issue metadata set <issue-id> --key wf_out.severity --value high
  multica issue metadata set <issue-id> --key wf_out.module --value auth
  ```

  节点声明的每个 `outputs` 字段对应一个 `wf_out.<field>` 键;引擎轮询时收集 `wf_out.*` 前缀键写入 `node_state`。**约束即设计原则:输出是路由信号,不是数据载荷**——驱动边条件和下游插值的小标量走 metadata;大产物(报告、代码、日志)留在 issue comment 和 PR 里,下游通过 issue 链接查看。确需传结构化大对象时用下述保底通道;
- **教会 agent**:新增一个 builtin skill 文档(`multica-workflow-fork`),内容一页:什么时候写 `wf_output`、schema 是什么、完成后标 done。skill 目录是纯增量文件,不改上游 skill;
- **human 节点**:指派给人的 issue,人看完点 done 即放行;需要"审批意见"时同样写 metadata。

---

## 6. 引擎:一个 reconcile 轮询循环

新增 `server/internal/service/wfengine/` 包(~600 行含条件求值):

```text
每 5–10s(挂在现有 scheduler 的 tick 框架上,一行接线):
  for run in 活跃 wf_run:
    1. 读 run 行(含 node_state)
    2. 对每个 active 节点:查其 issue 当前状态
       - done      → 读 metadata 输出,节点标 succeeded
       - cancelled → run 标 failed,通知发起人
    3. 对每个 pending 节点:入边全满足? → 创建 issue,节点标 active
    4. 无 active 且无可激活 → run 标 done
    5. 条件更新写回(WHERE updated_at = 读取值;冲突则本 tick 跳过,下 tick 重算)
```

特性即约束:引擎**无状态、可重入**——每 tick 从 DB 全量重算,进程重启零恢复逻辑;推进延迟最坏一个 tick(秒级),对"节点粒度是 agent 干活几分钟到几小时"的场景完全无感。

`stop` 操作(fork 版唯一的干预原语):run 标 `stopped`,引擎不再推进;已创建的 issue 原地保留,人工接管。Pause/Eject 的完整语义(上游 RFC §2.8.5)不做。

---

## 7. UI:React Flow 只读图

新增 `packages/views/workflows/` 域 + `/{slug}/workflows` 路由(web/desktop 各一条接线):

- **列表页**:定义列表 + 各自进行中/历史 run;
- **图页**(React Flow,MIT,dagre 自动布局):
  - 节点卡片 = 标题 + executor 图标(🤖/👤)+ assignee 头像 + 一行任务摘要 + 状态色(灰=未激活、蓝=进行中、绿=完成、红=需处置);
  - 边上标注条件文本("severity ∈ [high, urgent]"),已走过的路径加粗高亮;
  - 点节点 → 跳对应 issue 详情——观察细节、评论、接管全部走现有 issue 页,不重造;
  - **不做编辑交互**,图只读;前端轮询 run 接口刷新状态,WS 事件先不接。
- 不做 Run Banner、不做 inbox 卡投影——避免改 issues/inbox 现有组件;fork 期 workflow 页是唯一观察面。
- 依赖:`@xyflow/react` + `dagre`,进 pnpm catalog,在 `packages/views/package.json` 声明。

---

## 8. 编排:三档,先做前两档

1. **手写 JSON**(第一周可用):`POST /api/workflows/definitions` 提交图定义,工程师照 §4 schema 写;
2. **Agent 编排**(成本≈一份 skill 文档):builtin skill `multica-workflow-authoring` 教 agent JSON schema 与提交 API(用 `curl + $MULTICA_TOKEN`,见 §1 CLI 零修改保证,不新增 CLI 子命令)。chat 里对任意 agent 说"帮我编一个 bug 处理流程"→ agent 生成 JSON → 调 API 创建 → 回图链接,人看图确认;
3. **表单/画布编辑器**:fork 版明确不做。

Run 的触发:v1 只做 API/手动(`POST /api/workflows/runs`,带入口 context)。定时/webhook 触发复用 autopilot 的思路留到 v2(或直接让 autopilot `run_only` 任务里的 agent 调 run API,零开发)。

---

## 9. 相对上游 RFC 砍掉的东西

| 上游 RFC 里的 | fork 版处理 | 理由 |
|---|---|---|
| 状态写路径收敛重构(R1) | **不做**,轮询代替挂钩 | 最大成本项;轮询在内部规模下无感 |
| `workflow_node_run` 表 + 唯一约束幂等(R2) | JSONB + 单行乐观锁 | 内部单实例,无多实例竞态 |
| 激活 `issue_dependency` | 不用,边只在 graph JSON | 少一次上游表结构纠缠 |
| program 执行者、毕业机制、信任阶梯 | 不做(executor 先只有 agent/human) | 内部需求没有;将来加是纯增量 |
| Run Banner / inbox 卡投影 | 不做,workflow 页为唯一观察面 | 避免改 issues/inbox 现有组件 |
| Pause/Eject 完整决策表 | 只做 `stop` | 决策表大部分格子内部用不到 |
| CEL 表达式 | `eq/in/exists/default` 四种 JSON 条件 | 覆盖内部场景,50 行实现 |
| 新 CLI 命令 | **永不加**:输出走现有 metadata 命令,API 调用走 `curl + $MULTICA_TOKEN` | CLI 零修改是硬约束(§1) |

---

## 10. 改动量、涉及模块与上游同步策略

### 10.1 改动量(~3–4 人周)

| 部分 | 内容 | 估算 |
|---|---|---|
| 后端 | `900_` 迁移 ×1 + `wf.sql` queries + `service/wfengine/`(~600 行)+ handler `wf.go`(~300 行) | ~1.5 人周 |
| 前端 | `views/workflows/`(React Flow 页 + 列表)+ core API/zod schema/hooks + 两端路由 | ~1.5 人周 |
| 编排 | 2 份 builtin skill 文档 + 2 个内部真实流程的 JSON 模板 | ~2 天 |

### 10.2 上游文件触碰点(预计 4 处,每处 1–3 行)

1. 路由注册(handler 挂载);
2. scheduler 启动 reconcile goroutine;
3. sqlc 配置纳入 `wf.sql`;
4. 前端路由表(web/desktop 各一条)。

其余全部为新增文件。仓库自身的硬规则不豁免:`workflows` 加入 `reserved_slugs.json` 并重新生成、API 响应过 `parseWithFallback` + zod、workspace 过滤、i18n 文案过 glossary。

### 10.3 上游同步策略

- 定期 `git fetch upstream && git rebase`(或 merge),因新增文件为主,冲突预期集中在 4 个接线点;
- 迁移号段 `900_` 保证 schema 永不撞号;`wf_` 表名前缀保证上游未来的官方 `workflow_` 表可并存;
- 若上游发布官方 workflow:冻结 fork 功能 → 写一次性迁移脚本把 `wf_definition/graph`、`wf_run/node_state` 搬到官方 schema(概念对齐使之为数据搬运)→ 删除 fork 代码。

---

## 11. 执行顺序

| 周 | 交付 | 验收 |
|---|---|---|
| W1 | 迁移 + 引擎 + API | curl 提交手写 JSON 流程,跑通"创建 issue → agent 干活 → metadata 输出 → 条件边 → 下一节点 → run done" |
| W2 | React Flow 图页 + 列表页 | 图上实时看到节点状态流转,点节点跳 issue |
| W3 | agent 编排 skill + 模板 | chat 里让 agent 编排一个流程并发起 run;拿一个真实内部流程上线试跑 |
