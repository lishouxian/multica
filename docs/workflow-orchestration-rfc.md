# Workflow 编排 — 「agent 铺轨,引擎行车」设计方案

> Status: **Frozen for M0/M1**(§0–§2、§6 的 M0/M1 部分冻结,进入 M0 实现评审;§3.10 产品增强与 M2+ 内容仍为 Draft,待 dogfood 数据检验)
> Owner: TBD
> Last updated: 2026-07-11
> Rev 3: 吸收外部 review 意见——四层账本模型、执行者×控制结构正交拆分、program v1 受控能力、依赖完成语义显式化、指标质量护栏、M1 改为三执行者纵切。
> Rev 4(规格收口): M0 依赖流转决策表(仅 `all_succeeded`)、依赖边 provenance 与 Eject 降级、最小 node-run 状态机(task failure ≠ issue status)、program at-least-once + action 级幂等键 + 系统身份、Pause/Eject 决策表、账本纪律措辞修正、M1 拆分为 M1a/M1b。见 §2.8。

## TL;DR

- **动机(第一性原理)**:LLM agent 是概率性的,多步流程的端到端成功率随步数指数衰减;人对 agent 的委托上限受限于监督带宽,而不是 agent 能力。核心目标:**把过程可靠性从 LLM 的概率行为中剥离出来交给确定性结构,把监督成本从 O(每一步) 降到 O(例外),把委托单位从"任务"升级为"流程"。**
- **三元执行者模型**:执行者有三种——**人**(判断力最强、最贵)、**agent**(判断力可及、概率性、中等成本)、**程序**(零判断力、零边际成本、100% 可靠)。系统的本质是让工作沿"人 → agent → 程序"的阶梯持续下沉:例外沿阶梯向上找更贵的判断力,确定性沿阶梯向下沉淀为更便宜的执行。
- **问题**:multica 今天组织复杂多 agent 流程的唯一方式是 squad leader + prompt 约定(backlog 停车、手动 promote、child-done comment 唤醒)。流转没有引擎保证、每一跳都付一次完整 LLM 调用、流程不可声明/复用/审计。
- **方案**:引入 Workflow 编排层,定位介于 Dify(纯静态图)和 Claude Code dynamic workflow(纯动态)之间——**planning 动态(LLM 决定建什么节点),transition 确定(节点间流转由引擎执行)**。
- **一句话定性**:**Issue-native 的判断力编译系统,而不是 Issue-only 的 Agent 工作流系统。**
- **架构关键**:不建第二套**用户可见**的工作系统,但保留最小、不可绕过的引擎账本。Issue 是唯一的用户协作界面,`workflow_run` / `workflow_node_run` 是唯一的引擎执行账本,issue 状态只是运行状态的**产品投影**。引擎的角色是"issue 树的编译器 + 状态机监督者"。
- **激活休眠资产**:`issue_dependency` 表(migration 001 就存在,至今无任何 query/service 使用)作为运行时 DAG 边存储。
- **两种流转控制**:`gateway`(CEL 表达式,零 LLM 成本,确定性)对应工程控制;`agent_gateway`(LLM 路由决策,但引擎强制 schema + default 分支 + 超时 fallback)对应 prompt 控制。
- **北极星指标**:**单位合格产出的判断力成本**——不是裸的"介入次数"(单看它会鼓励系统隐藏异常),而是配套质量护栏(验收成功率、返工率、Eject 率)的成本指标。
- **出场顺序修正(见 §7)**:先以"规则自动化"形态发布最小能力(依赖全 done 自动 promote 等单条规则),可选用"智能看板列"做第一层产品皮,显式 DAG 图后置——让图从规则的组合使用中长出来,而不是先建画布。
- **改动量**:总计约 13–18 人周,分 M0–M3 四期,M0/M1 之间有止损点。
- **最大风险**:① issue 状态副作用分散在至少三条写路径,引擎挂不全会静默卡死——M0 必须先收敛到 service 单一入口;② 进程内 event bus + 多实例部署,引擎推进必须完全靠 DB 幂等,不能依赖事件必达。

---

## 0. 动机:第一性原理

### 0.1 三个底层事实

1. **LLM agent 是概率性的。** 单步可靠性再高也不是 1。每步 90% 可靠的 10 步任务,端到端成功率只有 35%。链条越长,失败是数学必然,不是 prompt 没写好。
2. **LLM 的判断力很贵,但协调工作大部分不需要判断力。** "检查三个子任务是否都完成"、"CI 过了就走下一步"——今天由 squad leader 用一次完整 LLM 调用来做,既贵又有概率打偏。
3. **人对 agent 的委托量,上限不在 agent 能力,而在人的监督带宽。** 敢交给 agent 多少活,取决于有多少注意力盯着它。这是所有 agent 产品的真正瓶颈:不是"agent 干不了",是"不敢不看着它干"。

人类劳动史解决过同样的问题:单个工匠不可靠、不可扩展,解法不是找更强的工匠,而是**流水线**——判断力留给人,过程可靠性交给结构。

> **核心原因:把"过程的可靠性"从 LLM 的概率行为中剥离出来,交给确定性结构承担,从而把人对 agent 的监督成本从 O(每一步) 降到 O(例外),把委托的单位从"一个任务"升级为"一整个流程"。**

### 0.2 三元执行者模型

执行者有三种,构成一个以成本和适应性为轴的阶梯:

| 执行者 | 单次成本 | 可靠性 | 适应性(处理没见过的情况) |
|---|---|---|---|
| **程序** | ≈ 0 | 100%(预设范围内) | 零——遇到新情况直接挂 |
| **Agent** | 中 | 90–99% | 高 |
| **人** | 最高 | 最高(且可负责) | 最高 |

阶梯上有两条方向相反的链:

- **链一:运行时向上升级(处理例外)。** 程序节点挂了 → 先交给 agent 兜底(把报错和上下文给它,诊断、修复、重试)→ agent 也搞不定才升级到人。程序的脆弱性被 agent 的适应性包住,人只接最后的漏勺。
- **链二:演化时向下沉淀(毕业机制)。** 一项工作的生命周期:人做(探索)→ 教给 agent(半结构化)→ 固化为程序(完全结构化)。一个 agent 节点连续 N 次产出模式完全一致时,**让这个 agent 自己把脚本写出来**,人 review 一次,此后该节点降级为程序节点——零成本、确定性运行。LLM 在此的角色是"从模糊过程到确定性代码的编译器"。

Agent 在这个图景里不是终点,是**中间体**:既是人的替身(向上),也是程序的孵化器(向下)。引擎的职责是把每一步路由到能胜任的最便宜一层。

### 0.3 北极星指标:单位合格产出的判断力成本

核心指标是**单位合格产出的判断力成本**。注意"合格"二字:单看"每次 run 人工介入次数"有经典的 Goodhart 风险——它会鼓励系统隐藏异常、把该升级的事静默吞掉。介入次数必须与结果质量护栏配套观测:

- **无人工介入且验收成功**的 run 比例(不是裸的无介入率);
- 每个 accepted outcome 的总成本(token + 人时);
- 完成后返工率;
- Eject / 强制接管率;
- 异常发现到恢复的时长;
- agent 节点向 program 节点的迁移比例(阶梯下沉速度)。

全部从 M1 起埋点。成熟 workflow 的健康趋势:介入次数 → 0 **且**验收成功率不降、LLM 调用占比持续下降、token 支出迁移为 CPU 支出。剧本详情页直接画这条曲线("三个月里每次合格 run 的成本从 $2.1 降到 $0.3"),这是流程资产复利的可视化证据。

### 0.4 三条设计推论

- **判断归 LLM,协调归引擎**——由成本和可靠性两个维度共同决定的最优分工,不是工程偏好;
- **人从监工变成"设计流程 + 处理例外"的值班员**——这是 "AI-native 团队" 的真实含义,例外队列是人的主工位;
- **流程一旦可执行,就从 prompt 里的口头约定变成组织资产**——可复用、可改进、可复利;每次人工介入都是流程改进的训练信号。

反面警戒:**不要把一切流程化。** 用 agent 而不用脚本,本来就是为了应对不可预先穷举的工作;一次性、探索性任务套 workflow 只有开销没有收益。产品姿态是默认动态(squad 随手干),只固化重复发生的东西。

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
6. **缺少"程序"这一执行层。** 确定性的步骤(跑固定脚本、调 API、等 PR merge)今天也只能由 agent 或人执行——最便宜、最可靠的执行者不在执行层里。

---

## 2. 设计方案

### 2.1 核心哲学:Issue-native,而非 Issue-only

一句话:**不建第二套用户可见的工作系统,但保留最小、不可绕过的引擎账本。**

精确的四层模型:

| 层 | 载体 | 职责 |
|---|---|---|
| 用户协作界面 | **Issue**(唯一) | 评论、负责人、inbox、PR 联动、人工接手——全部复用现有协作面 |
| 引擎执行账本 | `workflow_run` / `workflow_node_run`(唯一) | 暂停、重试、跳过、等待、循环计数、失败转人工、Eject——完整运行状态机 |
| 执行尝试 | `agent_task_queue` | 某个节点的某一次执行 |
| 产品投影 | Issue 状态 / banner | 运行状态的**投影**,不是运行状态机本身 |

两条对称的纪律:

1. **运行状态永不塞进 issue。** 暂停/循环次数/跳过标记等如果没有账本,几年后一定会被硬塞进 issue status 和 metadata,长出一套隐形 run 状态机——这正是要避免的腐化路径。
2. **账本不成为独立协作界面,只投影到 Issue / Inbox / Run Banner。** 禁止的是需要用户"住进去"的独立界面,不是投影本身——Run Banner 就是账本喂出来的投影(Dify 的 run log 之所以难受,不是因为它存在,而是因为用户必须住在里面)。

multica 的全部可观测面(issue 树 UI、timeline、comment、inbox、PR 联动)都长在 issue 上——**每个 Work Step 就是一个 issue,执行就是现有 task queue,人工介入就是现有 comment/status**。

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

### 2.4 Graph 模型:执行者 × 控制结构正交,不做 BPMN

图上只有两类元素,分别承载两个独立的维度:

```text
Work Step(干活的)
  executor = human | agent | program     # 可替换,图结构不变

Control Node(决定流转的)
  kind = gateway | join | wait | trigger | subworkflow
```

**不把执行者和控制结构混成一堆 node type**,原因有二:① "毕业"(agent 步骤沉淀为 program 步骤,见 §3.10)只需替换 `executor` 字段,不动图结构;② 人/agent/程序的同构(§3.6 随时换执行者)在模型层真正成立,而不是靠 UI 假装。`agent_gateway` 保留为**面向用户的 DSL 语法糖**(用户心智里"审查并路由"是一个动作),底层展开为 `gateway + 一个 executor=agent 的 Work Step`。

示例:

```yaml
name: release
steps:                                   # Work Step:executor 可替换
  wait_ci: { executor: program, action: wait_for_event, event: "pr.checks_completed" }
  analyze: { executor: agent, assignee: "@architect",
             prompt: "分析 CI 结果,起草发布说明",
             output_schema: { verdict: enum[ready, fix_needed], notes: string },
             retry: { max: 2 } }
  approve: { executor: human, assignee_role: admin }        # inbox 审批卡
  ship:    { executor: program, action: builtin, do: create_tag,
             on_failure: { fallback_agent: "@fixer" } }     # 兜底 agent
controls:                                # Control Node:只决定流转
  gate:    { kind: gateway, on: analyze.output.verdict,
             routes: { ready: approve, fix_needed: analyze },
             default: approve }                             # enum 漂移降级,不崩
edges:
  - wait_ci -> analyze -> gate
  - approve -> ship
policies: { loop_max: 3, run_deadline: 48h }
```

**流转的两种控制方式:**

- **工程控制** = `gateway`(CEL/JSONLogic 表达式路由,输入是上游 structured output、issue metadata、PR 状态,零 LLM 成本、毫秒级、确定性;UI 不暴露表达式,呈现为"当 analyze 的 verdict = ready 时"的下拉选择)。
- **prompt 控制** = `agent_gateway` 语法糖(路由决策交给一次 LLM 调用,可指定小模型,但引擎强制 output schema、强制 `default` 分支、强制超时 fallback)——**决策可以不确定,流转本身必须确定**。

**依赖完成语义必须显式声明**(join/edge 的属性,默认 `all_succeeded`):

- `all_succeeded` — 全部上游成功(默认);
- `all_terminal` — 全部上游到达终态(含失败/取消);
- `any_succeeded` — 任一上游成功;
- `on_failure` / `on_cancel` — 显式的失败/取消分支。

关键规则:**上游取消或失败,默认不视为满足**——下游进入"需处置"状态或升级给 agent/人,而不是拿着缺失的输出继续跑。比起"避免静默卡住",更危险的是"静默带病推进"。这条语义是三元阶梯在引擎层的第一次真实落地。**M0 只实现 `all_succeeded`**,其余语义在此定义、M1+ 按需启用;完整的 M0 决策表见 §2.8.1。

**`program` 执行者,v1 只支持受控能力**(不支持任意脚本):

1. **注册过的内置动作**:建 issue、改状态、写 metadata、发 Lark 通知、打 tag——已有 API 的封装;
2. **带 schema 的工具/API 调用**:声明输入输出结构、权限与 secret scope;
3. **等待事件**:等 PR merge、等 CI 结束、等定时——workflow 里大量"步骤"只是确定性等待,今天要么人盯、要么浪费一次 agent 唤醒。

所有 program 步骤强制:结构化输入输出、超时、幂等键、明确的权限/secret scope。"agent 写脚本"的任意执行能力只通过毕业管线(§3.10)引入,不在 v1 的直接配置面里。

程序步骤标配 **兜底 agent 开关**(`on_failure.fallback_agent`,默认开):失败时先让 agent 拿着报错和上下文试一次,agent 也搞不定才升级到人——运行时升级链(§0.2 链一)的落地。

**executor 可迁移**:同一个 step 的执行者可在 人/agent/程序 之间切换而图结构不变。step 详情页展示它当前在阶梯的哪一层,以及升/降级信号(见 §3.10 毕业机制)。

### 2.5 引擎实现:事件驱动 reducer,不引入 Temporal

1. **驱动源**:订阅现有 event bus(`issue:status_changed`、`task:completed`、PR webhook 事件)。
2. **推进函数**:纯函数 `advance(graph, runState, event) -> []Action`;Action 为"创建 issue / promote backlog→todo / enqueue task / 执行程序动作 / 完成 run"。纯函数可单测穷举全部转移(复用 `autopilot_test.go` 风格)。
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

### 2.8 M0/M1 规格收口(冻结)

本节是 M0 实现评审的基准,与实现不一致以本节为准。

#### 2.8.1 M0 依赖流转决策表

M0 只实现 `all_succeeded`。规则**无状态**:每次上游终态事件或 reconcile tick 触发时,按所有上游的**当前状态**重算,不看历史路径。

适用前提:下游 issue 处于 `backlog`、assignee 为 agent/squad、存在至少一条指向它的依赖边。

| # | 触发 | 上游集合当前状态 | 下游动作 |
|---|---|---|---|
| 1 | 某上游 → `done` | 全部 `done` | promote 为 `todo`,enqueue assignee,写 system comment(来源=引擎) |
| 2 | 某上游 → `done` | 存在非 `done` 且非终态 | 无动作,继续等 |
| 3 | 某上游 → `cancelled` 或重试预算耗尽的失败 | — | 下游保持 `backlog`,标记"需处置"并通知 run 发起人 / 父 assignee;**不视为满足** |
| 4 | 上游 `cancelled` 后被手动改为 `done` | 全部 `done` | **视为满足**,promote——只看当前状态,不看路径 |
| 5 | 下游在依赖未满足时被手动 promote | 任意 | **人的操作永远赢**:不阻止、不回滚;写 system comment 记录"依赖未满足时被手动启动";引擎停止对该下游的 promote 监督 |
| 6 | 下游已不在 `backlog`(任何原因) | 任意 | 引擎不再 promote(幂等:backlog→todo 至多发生一次) |
| 7 | 建边成环 | — | 建边时事务内 DFS 拒绝;存量环由 reconcile 检测并告警 |

#### 2.8.2 依赖边的 provenance 与 Eject 行为

`issue_dependency` 增加 `source` 列:`user`(人工创建)或 `workflow_run_id`(引擎创建的 runtime edge)。

- 自动 promote 规则对两种边**都生效**——它是通用规则(方案 D 的第一条),人工手建的依赖同样受益;
- runtime edge 的生命周期归属其 run:run 内的重试/跳过/失败路由只作用于 runtime edge;
- **Eject 时 runtime edge 降级为 `source=user` 的普通依赖并保留**——图结构不消失,只是引擎不再以 run 视角监督;人接管时仍能看到原有结构,通用 promote 规则继续生效;
- 删除 workflow 定义或 run 记录,不删除已降级的边。

#### 2.8.3 最小 node-run 状态机;task failure ≠ issue status

```text
pending ──► ready ──► running ──► succeeded
                        │ ├──► failed(重试预算内)──► running(引擎重试)
                        │ └──► cancelled
                        └──► failed(预算耗尽)──► needs_attention ──► succeeded / skipped(人工处置)
            ready ──► skipped(人工跳过)
```

- `workflow_node_run` 是唯一的运行状态机,issue 状态只是投影(§2.1)。
- **一次 task 尝试失败 ≠ issue 状态变化**:重试预算内的失败只推进 node_run 内部的 attempt 计数,issue 面无感;预算耗尽进入 `needs_attention` 时才投影(issue 标记 + inbox 需处置卡)。
- 投影单向:node_run → issue。引擎绝不从 issue 状态反推 node_run;人工改 issue 状态由 §2.8.1 的 #4/#5 规则单独处理。

#### 2.8.4 Program 执行保证与权限主体

- **交付语义:at-least-once。** 事件驱动 + reconcile 兜底的架构不承诺 exactly-once,不假装承诺。每个注册 action 必须声明 **action 级幂等键**(如 `create_tag` 以 tag 名幂等,通知类以 `(run_id, node_key, attempt)` 幂等),重复投递可安全重放。
- **权限主体**:program 步骤以 **workspace 级 workflow 系统身份**运行,不借用任何个人身份。每个 action 声明所需 scope,run 创建时由创建者授予;活动日志以 "workflow X run N" 记录 actor;secret 按 scope 注入,不落 issue/comment。
- **超时与取消**:每个 action 声明超时;超时按失败处理,进入重试/兜底 agent 链。

#### 2.8.5 Pause / Eject 决策表

| 维度 | Pause | Eject |
|---|---|---|
| 在跑的 task | 跑完为止,不派新 task | 跑完为止,结果只写 issue,不再推进 run |
| 未激活节点 | 保持 `pending` | node_run 全部封存(终态 `ejected`) |
| 等待中的事件 | 继续接收并记账,恢复后生效 | 停止消费 |
| 定时器 / SLA | 挂起,恢复后重算 | 取消 |
| 待审批卡 | 仍可回答,但不放行下游 | 转为普通 inbox 通知,无放行语义 |
| runtime edge | 不变 | 降级为 `user` 依赖(§2.8.2) |
| parked 的 backlog issue | 不变 | 原地保留,人工接管 |
| 可逆性 | 可恢复,从暂停点继续 | **不可逆**,run 记录保留为"已弹出" |

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

**v1 不做 n8n 式自由拖拽画布**:左侧步骤列表(增删排序、缩进表并行),右侧自动布局只读 DAG 预览。每个 Step 六个配置项:谁来做(人/agent/程序)/ 做什么(prompt 或命令,支持上游变量插值补全)/ 产出什么(输出字段)/ 怎么算做完(验收标准,见 §3.10)/ 之后去哪(默认顺连,可按输出值分支)/ 出错怎么办(重试 N 次 / agent 兜底 / 转人工 / 备用分支,默认转人工)。

### 3.3 动线 B · 触发 Run:四个入口

1. **手动**:workflow 详情页 [Run],弹窗填入口参数,显示基于历史的预估成本。
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

- **改 assignee / 换执行层**:agent 干不动就换 agent、换成人、或(对确定性步骤)换成程序——引擎只等 issue 到 `done`,**人、agent、程序在流程里完全同构**。
- **重试 / 跳过**:失败 step 一键重跑(fresh session 或续 session);或标记跳过按"视为完成"推进(确认弹窗写明下游拿到空输出)。
- **暂停 Run**:停止推进,在跑的 task 跑完即止;恢复后从暂停点继续。
- **Eject(逃生舱)**:一键降级为普通 issue 树,引擎放手,人工接管,run 记录保留为"已弹出"。**必须永远存在且可用**——失控时保住信任的按钮。

失败默认策略是**先 agent 兜底、再转人工,而不是终止**:程序/agent 步骤失败 → 兜底 agent 试一次 → 仍失败则生成 assignee 为发起人的处理卡片 → 人处理完标 done → 流程继续。

### 3.7 动线 F · 动态铺轨:看着 agent 现场画图

剧本可含"由 agent 规划"的展开步骤(planner step):run 到达时 leader 现场决定建哪些子步骤(`spawn-node` CLI),**流程图上实时看到新节点"长"出来**(虚线=规划中,实线=进入引擎管辖)。铺完轨 leader 退场,流转由引擎确定性执行。

### 3.8 动线 G · 复盘与固化(杀手锏)

1. Run 结束页:每步耗时、token 成本、重试次数、人工介入点、产出链接(PR、文档)。
2. 对动态铺轨居多的成功 run 提供 **[Save as workflow]**:把实际走过的图固化为剧本草稿,具体值自动参数化。
3. 飞轮:**随手让 squad 动态做 → 做得好固化 → 固化后越跑越稳越便宜**(静态图省掉动态规划的 LLM 调用;稳定的 agent 步骤进一步毕业为程序步骤,见 §3.10)。流程资产是"录制"出来的——Dify 给不了,因为它没有"先动态跑一次"的能力。

### 3.9 刻意不做

- 自由拖拽画布(v1)——动线 G 成立后多数图不是人画的。
- 通用表达式语言暴露给用户——UI 是下拉选择,底层才是 CEL。
- 独立 run 日志页——可观测性全部长在 issue 上。
- 跨 workspace 编排(远期)。
- 模板市场(早期)——用户不缺画图工具,缺"敢让它自己跑完"的理由。

### 3.10 产品增强:围绕"敢委托、少介入、数据回流"三支柱

**支柱一:敢委托——信任要有刻度**

1. **自治等级(信任阶梯)⭐**:每个 workflow/step 可调档——观察模式(每步推进前人点头,新剧本默认)→ 卡点模式(只在标记的边上停)→ 全自动(只有失败找人)。**档位是"挣"出来的**:连续 N 次零介入后系统主动提示"要取消这步的确认卡点吗?"。信任建立的过程被产品显式承载。
2. **每步验收标准(Definition of Done)**:机器可查的(测试通过、PR 已开、输出字段非空)或交给廉价 verifier agent 核对的检查清单,引擎推进前先验收。把信任来源从"相信 agent 说做完了"换成"验证产出符合标准"——敢开全自动的前提。
3. **预算护栏**:run 发起显示预估成本(基于历史),剧本可设 token/时长预算,超了挂起找人。数据(task_usage)现成。

**支柱二:少介入——例外队列是人的主工位**

4. **「需要你」值班台 ⭐**:inbox 聚合所有等人的事(审批卡、失败卡、超时告警)为带优先级的队列,每张卡带等待时长("changelog 审批已等 3 小时,整条 run 因它停着")。
5. **接手包(Handoff Bundle)**:失败/审批卡点开即见:run 要干什么、走到哪、这步试了几次、每次怎么失败、上游给了什么输入——一页读完即可决策。O(例外) 的成本还取决于单次例外的处理成本。
6. **静默超时(每步 SLA)**:step 可配"超过 X 小时无进展就升级"。最糟的失败模式不是报错,是无声卡死。reconcile tick 顺手实现。

**支柱三:数据回流——每次介入都是流程改进信号**

7. **介入原因一键采集 + 流程健康热力图**:每次打回/接管/重试弹一秒钟能完成的选择(产出质量/理解偏差/环境问题/其他+一句话);剧本详情页聚合成节点热力图——哪步最烧人、原因分布、趋势。改进有靶子。
8. **毕业机制(阶梯下沉的产品化)⭐**:agent 步骤连续 N 次操作序列/产出模式稳定,只触发**毕业候选**——连续成功不能直接证明程序可替代 agent。候选进入毕业管线:① 从历史成功 run 生成 replay fixture;② 自动生成 contract test;③ shadow mode 与 agent 结果对比;④ 人 review;⑤ 上线后保留 agent fallback 与一键回滚。管线按节点风险分级:有外部副作用的节点(部署、发通知)走全管线;纯读取/转换节点可缩短为 fixture + contract test + review(shadow mode 意味着双份执行,按风险付费)。产品文案:"可固化为脚本(预计每次省 ~40k token / 3 分钟),**让这个 agent 自己把脚本写出来?**" 反向的固化建议同理:相似 issue 链条第 N 次出现 → 提示存成剧本。**不指望用户主动设计流程,让产品收割重复。**
9. **委托度量周报**:本周 agent 完成多少 issue、无人值守完成率、人均被打断次数、环比。让"省注意力"可见(留存根基),倒逼指标从第一天采集。

**优先级**:P0 = ①④⑥(没有它们,"敢委托""少介入"不成立);P1 = ②⑤⑦(决定全自动能不能真开、例外贵不贵);P2 = ③⑧⑨(飞轮与留存,依赖数据积累)。

---

## 4. 改动量与涉及模块

### 4.1 总量

| 阶段 | 内容 | 后端 | 前端 | 工作量* |
|---|---|---|---|---|
| **M0** | 状态写路径收敛 + dependency 自动 promote(§2.8.1 决策表) | ~400 行 + 收敛重构 | 0 | 2 人周 |
| **M1a** | 引擎账本 + node-run 状态机 + 三执行者纵切(无 UI) | ~2000–2500 行 | 0 | 2–3 人周 |
| **M1b** | Run Banner + inbox 卡 + 从 issue 发起 + dogfood | ~500–1000 行 | ~2000–3000 行 | 2–3 人周 |
| **M2** | 创建器 + 审批 + 干预 + autopilot 集成 | ~2000 行 | ~3000–4000 行 | 5–7 人周 |
| **M3** | 动态铺轨 + 固化 + 统计 | ~1500 行 | ~1500 行 | 3–4 人周 |

\* 含测试(本仓库 Go 侧测试普遍是实现的 1–2 倍行数,已计入)。总计 **13–18 人周**。M0 独立有价值;M0/M1a、M1a/M1b 之间都是止损点。M1 纵切所需的最小程序执行者(两个内置动作)计入 M1a;毕业管线等 §3.10 增强未计入,按 P0–P2 优先级另行排期。

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
- 程序步骤权限边界:v1 不支持任意脚本,只允许注册过的内置动作 / 带 schema 的工具调用 / 事件等待,强制 secret scope、超时与幂等键;任意执行能力只经毕业管线(fixture → contract test → shadow → review → fallback+回滚)引入。

### 🟡 R6 其余清单

- autopilot `execution_mode` CHECK 约束迁移:先发容错代码再发迁移,避免旧代码读到新值的窗口。
- 多租户:新表全部 `workspace_id` 过滤,漏一条即越权,review 重点。
- i18n:文案过 conventions.mdx glossary 与中文 voice guide。
- `issue_dependency` 是 001 时代表结构,启用前确认字段够用(`created_by`、边类型),宁加列勿复用语义。
- 性能:issue 列表/详情多一次 run 归属 join,勿退化现有 keyset 分页。

---

## 6. 实施路径

### 6.1 推荐路径:先内核、后皮肤,让图从使用中长出来

结合 §7 的备选方案分析,推荐的出场顺序不是"先建完整 workflow 产品",而是:

1. **M0(现在就可动工,约 2 人周)**:① 状态变更收敛到 service 单一入口(R1);② 激活 `issue_dependency`,并**把成功/失败/取消后的传播语义钉死**(§2.4:默认 `all_succeeded`;上游失败/取消 → 下游进"需处置"状态,不视为满足);③ 用数据库条件更新 + reconcile 保证幂等(R2)。**不新增任何用户可见概念**,但"工程控制流转"的最小形态已经存在:串行链和 join 由服务端保证,不再靠 prompt 自觉。这也是方案 D(规则自动化)的第一条规则。
2. **M1(拆为 M1a / M1b 两个可独立验收的半程)**:不做"多个 agent issue 串起来"的宽切面——那只能验证 DAG 引擎,验证不了本方案真正的赌注(三元阶梯)。改做**一条包含全部三种执行者的真实纵切**,直接 dogfood 发版流程:

   ```text
   程序:等待 CI 事件 → agent:分析失败/起草发布说明 → 人:inbox 审批 → 程序:打 tag/发通知
   ```

   - **M1a · 后端纵切(约 2–3 人周)**:引擎账本 + node-run 状态机(§2.8.3)+ 三执行者纵切跑通;program 执行者只做两个内置动作(等待 CI 事件、打 tag/通知,按 §2.8.4 的幂等与身份规格);剧本手写 JSON 硬编码,CLI 触发,**无产品面**,以集成测试验收。M1a 结束即可验证阶梯,不等 UI。
   - **M1b · 产品投影 + dogfood(约 2–3 人周)**:Run Banner(步骤条)、inbox 审批卡与"需处置"卡、静默超时、从 issue 发起 run。然后发版流程 dogfood 一个月,盯 §0.3 的指标组。

   **不做创建器、不做 DSL 编辑、不做画布。** 纵切同时验证:三种执行者能否真正互换、join/retry/escalate 是否可靠、issue 投影与运行账本是否清晰、介入是否真的减少、程序步骤是否确实降本。
3. **M2 起由 dogfood 数据决定投资方向**:介入多因"不知道出事/处理例外太累" → 优先值班台+接手包+自治等级(§3.10 支柱二);介入少但发现大量 agent 步骤在重复做确定性的事 → 优先扩充程序动作+毕业机制(三元阶梯)。

### 6.2 分期表

| 阶段 | 交付动线 | 内容 |
|---|---|---|
| **M0** | (无 UI) | 状态收敛 + dependency 自动 promote(§2.8.1 决策表,仅 `all_succeeded`)+ DB 幂等,验证引擎地基,squad leader 立即受益 |
| **M1a** | (无 UI) | 引擎账本 + node-run 状态机 + 三执行者纵切(发版流程:程序等 CI → agent 分析 → 人审批 → 程序执行),集成测试验收 |
| **M1b** | B3 + C | Run Banner + inbox 审批/需处置卡 + 静默超时 + 从 issue 发起;发版流程 dogfood 一个月;内置模板,无创建器 |
| **M2** | A + D + E(+ §3.10 P0/P1 按数据取舍) | Describe-to-workflow 创建器、human 卡点 + inbox 审批、重试/跳过/Eject、autopilot `run_workflow`、自治等级、值班台 |
| **M3** | F + G(+ §3.10 P2) | planner step 动态铺轨、Save as workflow 固化、程序节点毕业机制、成本/成功率统计、委托周报 |

**建议的第一个 PR**:R1 的状态变更收敛重构——独立可 review、独立可回滚,且无论 workflow 最终做不做都在偿还已存在的技术债。

---

## 7. 被考虑过的替代方案

同一诉求(多 agent 复杂流程、节点间流转状态、工程/prompt 双轨控制)在设计空间里的其他落点,以及取舍结论。

### 方案 B:独立 Workflow 引擎(Dify 本体形态)

Workflow 与 issue 平行,自己的画布、自己的 run 记录页;节点调用 agent 但 run 不落成 issue。
**优**:不碰 issue 语义零回归;能编排与 issue 无关的链;对标 Dify 心智零成本。**劣**:两套真相,可观测/评论/通知全部重建;人机协同二等公民;与任务管理叙事脱节。**结论:排除**(叙事和成本双输),除非未来把 workflow 做成独立产品线。

### 方案 C:Workflow-as-Code(Temporal 路线)

不做可视化,流程用代码写(Go/TS SDK + durable execution),multica 提供 activity 库(`assignIssue` / `waitForDone` / `askAgent` / `waitForPRMerge`)。
**优**:重试/幂等/持久化引擎白送(R1/R2 防御大幅简化);表达力无上限;版本化走 git;用户本就是工程师。**劣**:非开发者无法参与;流程不可见;重型依赖或自研成本;产品内体验割裂。**结论:不作为产品形态**,但若只想最快验证"编排有没有价值",它是最短工程路径。

### 方案 D:规则自动化(Zapier / Linear Automations 路线)

不建图,只有扁平规则列表:"当 issue 完成 → promote 依赖它的 backlog issue"、"当 PR 合并 → 创建部署 issue 指派 @ops"。复杂流程从规则组合中涌现。
**优**:增量最小,每条规则独立有用,第一周可上线;理解成本≈0;与 Linear 气质最贴。**劣**:流程隐式,看不到全貌;涌现行为难调试;没有 run 概念。**结论:采纳为 A 的出场方式**——M0 的依赖自动提升本身就是第一条规则;规则积累到用户开始组合复杂链条时,升级为显式图(规则可自动迁移为边)。

### 方案 E:纯 Agent 路线(不建引擎,把 orchestrator 做可靠)

赌模型进步,只给 leader 三样:持久化计划清单(checklist 存 metadata,唤醒先读)、看门狗心跳(cron 用小模型定期唤醒核对全局)、批量状态查询 CLI。
**优**:工程量最小(1–2 人周);灵活性无上限;模型每变强一代免费变好。**劣**:流转仍是概率性的;心跳烧 token;永远给不出保证。**结论:不作为主路线,但看门狗心跳无论选哪条都值得做**——它是所有方案的兜底,也是"引擎会不会是上一代模型时期遗产"这一质疑的对冲。

### 方案 F:配置即代码(GitHub Actions 路线)

流程定义是团队仓库里的 YAML(`.multica/workflows/release.yml`),git 版本化、PR review;multica 读取执行,产品内渲染只读图。
**优**:版本化/审计/review 白嫖 git;dev 心智对齐;编辑器一行不用做;流程随代码库走。**劣**:绑定 repo,不适合非代码流程;改流程走 PR 迭代重。**结论:作为 A 的存储层变体保留**——graph 存 DB 与存 git 引擎同构,可对硬核团队并行支持。

### 方案 G:看板列驱动(零新概念路线)

流程 = 看板本身:每列配置处理者(人/agent/程序)与准入规则,issue 进列即触发、完成即流到下一列。**issue 在看板上的物理移动就是 workflow 的执行**。
**优**:不引入任何新概念;可视化天然免费;人拖卡=干预,交互零学习成本。**劣**:只能表达线性/准线性流程;流程绑死 project,不可复用为模板。**结论:采纳为 A 的可选产品皮**——引擎同一个,v1 可只暴露"智能看板列"而不暴露 DAG;对小团队"流水线"隐喻可能比 DAG 好卖。

### 对比总表

| | 工程量 | 可靠性保证 | 灵活性 | 非 dev 可用 | 叙事契合 | 结论 |
|---|---|---|---|---|---|---|
| A 议题树+引擎 | 大 | 强 | 高 | 中 | 最高 | **主方案** |
| B 独立引擎 | 最大 | 强 | 中 | 高 | 低 | 排除 |
| C 代码引擎 | 中 | 最强 | 最高 | 无 | 中 | 排除(验证期备选) |
| D 规则自动化 | **最小** | 强(单规则) | 低(涌现) | 高 | 高 | **A 的出场方式** |
| E 纯 agent | 最小 | 弱 | 最高 | 高 | 高 | 心跳兜底采纳 |
| F YAML in git | 中 | 强 | 高 | 低 | 高 | A 的存储变体 |
| G 智能看板 | 小 | 强 | 低 | **最高** | 高 | A 的可选产品皮 |

---

## 8. 总结

一句话定性:**Issue-native 的判断力编译系统,而不是 Issue-only 的 Agent 工作流系统。**

multica 不需要"再造一个 Dify"。它需要:

1. 把已经存在于 prompt 约定里的编排协议(backlog 停车、promote 推进、child-done 回调、metadata 传值)**下沉为引擎保证**——判断归 LLM,协调归引擎;
2. 把 squad leader 从"全程盯梢的执行者"升级为"只负责规划的铺轨者";
3. 补齐执行层的第三种执行者(程序),让工作沿"人 → agent → 程序"的阶梯持续下沉:**例外向上找判断力,确定性向下沉淀为代码**。

最应坚持的六条边界:动态规划、确定执行;Issue 作为唯一协作面;最小但严格的运行账本;人/agent/程序作为可替换执行者;异常向上升级、确定性向下毕业;永远可 Eject。

它的卖点是"敢委托",指标是"单位合格产出的判断力成本",护城河是介入数据和沉淀下来的流程资产——multica 卖的不是"agent 干活",是这台把判断力逐步编译成组织能力的机器。休眠的 `issue_dependency` 表是最好的起点:最初的设计者已经预留了这条路。
