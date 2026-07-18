# Workflow Runner v0 — 业务交付版详细方案

> Status: 设计定稿候选（供评审）
> 定位: 官方 workflow layer 落地前的过渡方案。零 fork 改动、零新表、零迁移。
> Last updated: 2026-07-18

## 0. TL;DR

- **形态**：一个独立的小型 Go 程序（`wfrun`，约 1.5–2.5k 行含测试），不进 multica 源码树。它做两件事：把 YAML 流程定义**编译**成带 stage 的 issue 树（懒实例化），并在 **stage 边界**做确定性路由（条件分支、驳回回边、失败升级）。
- **引擎分工**：平台负责 join（stage barrier）与唤醒（平台级 child-done comment）；runner 负责流转决策；agent/人负责干活。全部状态长在 issue 上（子 issue 状态 + metadata），runner 无状态、可随时重启。
- **三个已被代码证实的关键约束**决定了核心设计（见 §2）：
  1. `done → todo` 重开**不会**重新给 agent 派任务 → 驳回必须用**新建 rework issue**实现；
  2. stage barrier 是**动态重求值**的 → rework issue 复用原步骤的 stage 序号即可，barrier 会重新打开；
  3. promote 下一 stage 是父 assignee 的职责而非平台 → 这正是 runner 接管的位置。
- **迁移承诺**：YAML 定义与官方 roadmap 上的 lightweight workflow layer（#1943 / #4325 团队表态）同构；官方落地后 YAML 平移、runner 退役。

---

## 1. 范围

### v0 做

- YAML 定义：串行 / 并行（同 stage）/ 条件分支（枚举相等 + default）/ 驳回回边（仅回到指定的前置步骤，带 max_loops）/ 人工审批步骤 / 变量与上游输出插值。
- 入口：手动 CLI 发起、cron 定时发起、`--root` 挂到已有 issue。
- 干预：pause / resume / cancel / eject / retry-step。
- 观测：父 issue description 里的实时进度表 + 每次流转的 system 风格 comment + 现有 issue 树 / stage UI。

### v0 明确不做

- 不改 multica 源码（server / packages / apps 一行都不动），不依赖任何未合并 PR（#5505/#5507/#5479）。
- 不做表达式语言（只有 `field == "枚举值"`），不做跨级回退（只允许回到 YAML 里声明的那一个前置步骤），不做嵌套子 workflow，不做可视化编辑器。
- 不做事件订阅 / webhook 推送驱动（纯轮询，见 D8）。

---

## 2. 已核实的平台机制（设计的地基，全部有代码出处）

| # | 机制 | 出处 | 对设计的影响 |
|---|---|---|---|
| M1 | 任务派发只在两种情形发生：**创建/改派 assignee**（`RunSourceAssign`，backlog 除外）与 **`backlog → 非终态` promote**（`RunSourceStatus`） | `server/internal/service/issue_trigger.go:89` `WillEnqueueRun` | **`done → todo` 重开不派单**。驳回/重试一律新建 issue，不重开旧 issue |
| M2 | stage barrier：最低未完成 stage 内所有子 issue 到达终态（`done`/`cancelled`）时，服务端发一条唤醒 comment 给父 assignee；不关闭 barrier 的完成是静默的 | `server/internal/handler/issue_child_done.go`（MUL-3508） | join 免费；`cancelled` 也算终态（审批驳回可用 cancel 表达，见 D4） |
| M3 | barrier 按当前子集合**动态重求值**；reopen+done 按新事件处理 | 同上 + #2120 团队确认 | 往已关闭的 stage 里加新子 issue 会重新打开该 stage，rework 复用原 stage 序号 |
| M4 | promote 下一 stage 是父 assignee 的职责，平台只唤醒 | `builtin_skills/multica-working-on-issues/SKILL.md:225` | runner 接管这个决策点；不依赖唤醒 comment（轮询兜底） |
| M5 | CLI 能力：`issue create --title/--description-file/--status/--assignee/--parent/--stage/--allow-duplicate --output json`；`issue children`；`issue comment`；`issue metadata list/get/set/delete`；`issue list --metadata k=v --status ...` | `server/cmd/multica/cmd_issue.go:437-476`、`cmd_issue_metadata.go` | runner 全部操作 shell out 到 CLI 即可，无需直连 REST |
| M6 | CLI 鉴权：`MULTICA_TOKEN` 环境变量优先，否则读 `multica login` 存储的配置 token；agent 任务态用 `mat_` 任务级 token | `server/cmd/multica/cmd_auth.go:74` | runner 用专用 bot 账号的用户 token 运行 |
| M7 | 创建 issue 有活跃重复检测，需 `--allow-duplicate` 绕过 | `cmd_issue.go:474` | rework/重试同名 issue 必须带 `--allow-duplicate`，且标题带 attempt 后缀 |
| M8 | member assignee 不触发任何派单（`WillEnqueueRun` 只处理 agent/squad） | `issue_trigger.go` | 人工步骤 = assignee 为 member 的子 issue，安全 |
| M9 | agent 任务内 CLI 以 `mat_` token 执行，可写 issue metadata | `cmd_agent.go:267` | agent 步骤的结构化输出通过 `issue metadata set` 落盘 |

---

## 3. 总体架构

```
YAML 定义 (git 仓库, PR review)
        │  wfrun run release.yaml --var version=1.2
        ▼
┌─ Run = 一棵 issue 树 ─────────────────────────────┐
│ parent issue (assignee=发起人; metadata: wf_state) │
│  ├─ [stage 1] 回归测试   → @qa-agent    (todo)     │
│  ├─ [stage 1] changelog → @doc-agent   (todo)     │
│  ├─ [stage 2] 发布审批   → member:xian  (懒创建)    │
│  └─ [stage 3] 上线      → @ops-agent   (懒创建)    │
└───────────────────────────────────────────────────┘
        ▲                    ▲
        │ 平台: barrier+唤醒   │ agent: 干活, metadata set 写输出
        │                    │ 人: 在 UI 里 Done/Cancel
        └────── wfrun tick (轮询 60s): 读状态 → 确定性路由 →
                建下一批子 issue / 建 rework / 升级人工 / 收尾
```

三方分工一句话：**平台管 join 和叫醒，runner 管往哪走，agent 和人管干活**。

---

## 4. 关键设计决策（D1–D13）

每条给出决策、理由、被否的备选。

### D1 · Runner 身份与鉴权

**决策**：创建一个专用 bot member 账号（如 `workflow-bot@内部域名`），管理员邀入 workspace，`multica login` 一次拿到 token，以 `MULTICA_TOKEN` 注入 runner 进程。

理由：写操作有清晰的 actor 归属（issue 树里所有编排动作显示为 workflow-bot 所为，与人和 agent 的操作可区分）；token 泄露只需注销一个账号。
备选否决：用真人 token（审计混淆、人员变动即断）；注册成 agent（会被平台当可派单对象，且需要 runtime）。

### D2 · 父 issue 的 assignee = 发起人（人）

**决策**：run 的父 issue assignee 是发起这次 run 的人，不是 bot、不是 agent。

理由：M2 的唤醒 comment 会 @父 assignee 并进 inbox——这样"每个 stage 完成"天然变成发起人的 inbox 通知，观测面免费；M8 保证 member assignee 无派单副作用。runner 不依赖唤醒（轮询），被 @ 只是给人看的。
备选否决：assignee=bot（通知喂给不看 inbox 的程序，浪费唯一的推送面）；assignee=squad leader（会真的派 LLM 任务，双重推进）。

### D3 · 驳回与重试 = 新建 issue，永不重开旧 issue（由 M1 强制）

**决策**：任何"回到前面的步骤"（审批驳回、步骤失败重试）都实现为：按原步骤模板**新建**一个子 issue，复用原步骤的 **stage 序号**（M3 保证 barrier 重新打开），标题带 attempt 后缀（`回归测试 (attempt 2)`），创建时带 `--allow-duplicate`（M7），description 里注入驳回理由 `{{steps.<gate>.reject_reason}}`。

理由：这是 M1 的直接推论——重开旧 issue agent 根本不会醒。附带的好处是每次尝试留下独立 issue，审计轨迹天然完整（RFC 里 `workflow_node_run.attempt` 的意思，用 issue 本身表达了）。
旧 issue 处理：保持 `done`/`cancelled` 不动，runner 在其 comment 区补一条"↩ 已被 attempt 2 取代"。

### D4 · 人工审批的操作映射：Done = 通过，Cancel = 驳回

**决策**：`type: approval` 步骤是一个 assignee 为指定 member 的子 issue。审批人在**现有 UI**里操作：通过 → 状态改 `done`；驳回 → 状态改 `cancelled`，并在 comment 里写原因（runner 抓取该 issue 上审批人的最后一条 comment 作为 `reject_reason`）。

理由：两者都是终态（M2），barrier 都会关闭，runner 在边界读终态即得结论；业务方**零学习成本、零新 UI**——这是所有备选里唯一不需要教用户任何新概念的。
备选否决：metadata 写结论（UI 没有 metadata 编辑入口，得让业务方用 CLI，不现实）；comment 关键词（自由文本解析脆弱）；双子 issue 二选一关闭（怪异）。
兜底：审批 issue 也接受 CLI 写 `metadata set decision=...`（高级用法/将来 agent 审批用），metadata 优先于状态映射。

### D5 · Agent 输出契约：metadata 键 + 枚举 + default 分支

**决策**：需要给下游做路由/插值的 agent 步骤,在 YAML 里声明 `outputs`（键名 + 枚举域）。runner 把输出要求**注入到该步骤 issue 的 description 尾部**（模板固定段落，含精确的 `multica issue metadata set <id> <key> --value ...` 命令示例）。stage 边界 runner 读 metadata：值不在枚举域或缺失 → 走 YAML 强制要求的 `default` 分支，并在父 issue comment 标注"输出漂移，已走默认分支"。

理由：M9 保证 agent 任务内可写 metadata；"决策可以不确定，流转必须确定"——LLM 忘写/写错不会卡死流程，只会走保守分支（通常是转人工）。这是 RFC 里 `agent_gateway` 的退化实现。

### D6 · 懒实例化 + run 状态存储

**决策**：发起时只创建父 issue + **stage 1** 的子 issue。后续每个 stage 在被路由选中时才创建。run 的全部编排状态存在**父 issue 的一个 metadata 键 `wf_state`** 里（JSON 字符串）：

```json
{
  "wf": "release-flow", "def_sha": "a1b2c3…", "vars": {"version": "1.2"},
  "status": "running",              // running | paused | needs_attention | ejected | done | cancelled
  "steps": {
    "regression": {"attempts": [{"issue": "MUL-xxx", "n": 1}], "loops_used": 1},
    "gate":       {"attempts": [{"issue": "MUL-yyy", "n": 1}]}
  },
  "deadline": "2026-07-25T00:00:00Z"
}
```

理由：懒实例化是条件分支的前提（未选中的分支不该存在，见 D9），也让上游输出在创建下游 issue 时已经就绪，插值可以在**创建时**完成（description 是静态渲染结果，agent 看到的就是最终 prompt，无运行时取值）。状态放父 issue metadata 而非本地文件 → runner 换机器/重装零迁移；步骤→issue 映射也免去靠标题猜。
`def_sha`：发起时把 YAML 内容的 SHA 连同**完整 YAML 原文**作为附言 comment 贴在父 issue 上——运行中的 run 永远按发起时的定义走，改 YAML 不影响在途 run，且事后审计能看到当时的定义。

### D7 · 幂等与单实例

**决策**：三层防线——
1. **单实例锁**：runner 由 cron/launchd 每分钟拉起，进程内 `flock` 文件锁,已有实例在跑则直接退出；
2. **写前校验**：每次要创建子 issue 前，重读 `wf_state.steps` + `issue children`，该 step 该 attempt 已存在则跳过（崩溃在"建了 issue、还没写回 state"之间的补救：children 里按标题 attempt 后缀识别孤儿并回填 state）；
3. **动作后写**：每个 advance 动作完成后立即写回 `wf_state`，一次 tick 内一个 run 最多推进一个 stage 边界（保守，宁可慢一拍）。

理由：业务部署是单点 runner，不需要 RFC 里 R2 级别的多实例 DB 行锁；但 cron 重入和崩溃恢复必须处理。

### D8 · 轮询驱动，60 秒 tick

**决策**：`wfrun tick` 用 `issue list --metadata wf_root=true --status in_progress` 找到所有在途 run，逐个 advance。不订阅事件、不架 webhook 接收端。

理由：一周工期内，轮询是唯一同时满足"零 fork 改动 + 丢事件天然免疫 + 重启即恢复"的驱动方式;60s 延迟对以 agent 任务（分钟级）为节点的流程完全无感。事件触发的 autopilot（#3216）上游还没有，等官方 layer 落地这层整个退役。

### D9 · 条件分支：枚举相等 + 强制 default

**决策**：YAML 的 `next` 支持三种形态——省略（顺连下一步骤）、字符串（无条件跳转）、路由表：

```yaml
next:
  when: "steps.review.outputs.verdict"
  routes: { approve: ship, revise: implement }
  default: manual_review          # 必填，缺了 wfrun validate 直接拒绝
```

匹配是字面量枚举相等，不做表达式求值。`routes` 的目标步骤在被选中时才实例化（D6）。

### D10 · 失败、超时与升级

**决策**：
- **步骤超时**：每步可配 `timeout: 4h`（默认 24h）。子 issue 创建后超时仍未终态 → runner 在父 issue comment @发起人，`wf_state.status = needs_attention`，**停止该 run 的自动推进**（其他 run 不受影响）。
- **agent 步骤"失败"的判据**：v0 只认 issue 终态与超时，不解析 task 失败事件（那需要碰事件面）。agent 把 issue 标 `cancelled` 视同该步失败。
- **失败路由**：步骤可配 `on_fail: {retry: 2}`（按 D3 新建 attempt）或 `on_fail: {goto: <step>}` 或缺省 → `needs_attention` 转人工。
- **run 级 deadline**：`policies.run_deadline: 72h`，超过 → `needs_attention`。
- **回边熔断**：`max_loops` 用尽 → `needs_attention`，绝不无限循环。

理由：所有失败路径的终点都是"叫人"，不是"终止"也不是"硬重试到死"——和 RFC"失败默认转人工"一致。`needs_attention` 后人处理完，`wfrun resume <run>` 继续。

### D11 · 暂停 / 取消 / Eject

- `wfrun pause <run>`：`status=paused`，tick 跳过；在途子 issue 自然跑完但不推进下一 stage。`resume` 恢复。
- `wfrun cancel <run>`：显式逐个 cancel 未终态子 issue，最后 cancel 父 issue（遵守仓库"无级联删除，应用层显式清理"的规矩）。
- `wfrun eject <run>`：`status=ejected`，runner 从此不碰这棵树,issue 树原样留给人工接管。**永远可用的逃生舱**。

### D12 · 观测面

- **父 issue description**：runner 每次流转后重写 description 中的进度块（发起时的模板含一个 `<!-- wf-progress -->` 锚区）：

  ```
  ▶ release-flow · 运行中 · Stage 2/3 · 已运行 1h42m
  ✅ 回归测试 (@qa-agent, 38m)   ✅ changelog (@doc-agent, 12m)
  ◐ 发布审批 (@xian, 等待中)     ○ 上线 (@ops-agent)
  ```

  每行链接到对应子 issue。这就是 RFC 里 Run Banner 的 Markdown 版，零前端改动。
- **流转 comment**：每个 stage 边界 runner 发一条简短 comment（"Stage 1 全部完成，verdict=approve，已创建 Stage 2：发布审批"），构成完整流转日志。
- **inbox**：stage 完成的平台唤醒 comment 直达发起人（D2）；`needs_attention` 的 @ 也走 comment→inbox。
- **stage 字段**：issue 列表/树 UI 本来就展示 stage 分组（上游已合入 #5312/#4425），免费。

### D13 · Runner 实现形态

- **语言/依赖**：Go 单二进制（和团队栈一致，方便你自己维护），或 TypeScript + tsx——**推荐 Go**，理由：可直接 vendor 复用 `server/cmd/multica` 的 client 包吗？不——**不 import multica 内部包**，只 shell out CLI（M5），保持对 multica 版本的松耦合（CLI 是稳定面，内部包不是）。
- **代码位置**：独立目录 `tools/wfrun/`（放你 fork 里但不碰上游文件，永无合并冲突），或独立小仓库。二选一皆可，推荐独立仓库（业务交付物和 fork 解耦）。
- **命令面**：

  ```
  wfrun validate <yaml>                    # schema 校验 + 图检查(可达性、default 齐全、回边合法、无环)
  wfrun run <yaml> --var k=v [--root ID]   # 发起
  wfrun tick                               # cron 入口: 推进所有在途 run
  wfrun status [run]                       # 列出 run / 单 run 详情
  wfrun pause|resume|cancel|eject <run>
  wfrun retry <run> --step <key>           # 手动重试某步(新建 attempt)
  ```

- **advance 纯函数**：`advance(def, state, children) -> []action`，与 CLI 执行层分离，动作类型只有 6 种（create_step_issue / create_rework / post_comment / update_state / update_progress / escalate）。纯函数单测穷举全部转移（照搬 RFC §2.5 的思路，也为将来迁移官方引擎留下语义测试集）。

---

## 5. YAML 规范（v0 完整形态）

```yaml
name: release-flow                # 必填, run 标题前缀
vars: [version]                   # 发起时必须提供的参数
policies:
  run_deadline: 72h               # 可选, 默认无
steps:                            # 有序列表; stage 相同 = 并行
  - key: regression               # 必填, 唯一, 蛇形
    stage: 1                      # 必填, >=1, 非降序
    title: "回归测试 {{vars.version}}"
    assignee: "qa-agent"          # agent 名 / member 名, CLI fuzzy match 语义
    prompt: |                     # 渲染进 issue description
      对版本 {{vars.version}} 执行回归测试。
    outputs:                      # 可选; 声明即注入"写 metadata"指令段
      result: [pass, fail]
    timeout: 4h
    on_fail: { retry: 1 }         # retry:N | goto:<key> | 缺省=转人工
    next:                         # 缺省=顺连下一步骤
      when: "steps.regression.outputs.result"
      routes: { pass: gate }
      default: needs_attention    # 内置终点: 转人工
  - key: changelog
    stage: 1
    assignee: "doc-agent"
    prompt: "为 {{vars.version}} 整理 changelog。"
  - key: gate
    stage: 2
    type: approval                # done=通过, cancelled=驳回(理由取审批人最后一条 comment)
    assignee: "member:xian"
    title: "发布审批 {{vars.version}}"
    on_reject: { back_to: regression, max_loops: 2 }
  - key: ship
    stage: 3
    assignee: "ops-agent"
    prompt: |
      发布 {{vars.version}}。
      回归结论: {{steps.regression.outputs.result}}
      审批: {{steps.gate.outcome}}
```

插值作用域：`vars.*`、`steps.<key>.outputs.<name>`、`steps.<key>.outcome`（approved/rejected）、`steps.<key>.reject_reason`、`run.root_issue`。插值在**创建下游 issue 时**静态渲染（D6），引用尚未完成步骤的输出是 validate 期错误。

`validate` 强制检查：key 唯一；stage 非降序且分支目标 stage > 当前 stage（除 `back_to`）；每个 `routes` 有 `default`；`back_to` 只能指向更早 stage 的步骤且目标步骤可重复执行；所有步骤从 stage 1 可达。

---

## 6. 交付排期（7 个自然日）

| 日 | 交付 | 验收 |
|---|---|---|
| D1 | 手工验证：用 CLI 手搓一遍业务真实流程的 staged issue 树，确认 barrier/唤醒/审批映射行为与 §2 一致（**把 M1–M9 全部实测一遍**，尤其 M3 的 barrier 重开） | 手工 run 走通一次含驳回的完整循环 |
| D2 | `wfrun` 骨架：YAML 解析 + validate + `run`（实例化 stage 1）+ `advance` 纯函数 + 单测 | validate 拒绝非法图；顺序流程能实例化 |
| D3 | `tick` 完整推进：stage 边界路由、懒实例化、`wf_state` 读写、幂等三防线（D7） | 串行+并行流程无人值守跑通 |
| D4 | 审批步骤 + 驳回回边（D3/D4 决策）+ 失败/超时/熔断（D10） | 含驳回、含超时升级的流程跑通 |
| D5 | 干预命令（pause/resume/cancel/eject/retry）+ 进度块与流转 comment（D12） | 演练一次人工接管 |
| D6 | 用业务方的 1–2 个真实流程写 YAML，bot 账号/cron 部署到业务实例，dogfood 一整天 | 真实流程端到端至少 2 个成功 run |
| D7 | 业务方实操演练 + 使用文档（一页：怎么发起、怎么审批、出事怎么办）+ 缓冲 | 业务方自己发起并完成一个 run |

D1 放在最前面是止损点：如果实测发现 §2 的任何机制与预期不符（比如 barrier 重开语义有出入），当天调整设计，代价最小。

## 7. 风险清单

| 风险 | 等级 | 对策 |
|---|---|---|
| M3 的 barrier 重开语义未经实测（rework 复用 stage 序号是设计的承重墙） | 🔴 | D1 首日实测；若不成立，退路是 rework 使用**新的更高 stage 序号**（语义等价，进度展示由 runner 的 step 视图承担，不依赖 stage 序号连续） |
| 审批人误操作（想驳回却点了 done） | 🟠 | 审批 issue description 里写明操作含义；gate 之后的第一步 comment 会显示"审批通过"，发现误点可 `wfrun retry --step gate` 重开审批 |
| agent 不写/写错 outputs | 🟠 | D5 的 default 分支兜底 + 父 issue comment 显式标注；prompt 模板里给出可直接复制的 metadata set 命令 |
| runner 单点挂掉 | 🟡 | 无状态 + cron 拉起；挂掉的表现只是"不推进"，issue 树完好，最坏人工 promote（回到今天的现状） |
| bot token 过期/吊销 | 🟡 | tick 启动先做一次鉴权自检，失败则本地告警（stderr + 退出码，交给 cron 邮件/监控） |
| 上游 #5505 合入改变状态模型 | 🟡 | runner 只依赖 CLI 稳定面与终态语义;若内置状态语义变化，改 runner 的状态映射表即可，不涉及数据迁移 |

## 8. 待拍板（不阻塞 D1–D3 开工，默认值可先行）

1. **runner 代码放哪**：独立仓库（默认）还是 fork 的 `tools/wfrun/`。
2. **bot 账号**：业务实例上创建 `workflow-bot` 的邮箱/邀请流程谁来走（需要 admin）。
3. **业务流程盘点**：下周要跑的到底是哪一两个流程、涉及哪些 agent/runtime、审批人是谁——D6 之前必须有答案，越早越好。
4. **tick 宿主**：runner 跑在哪台常开机器上（业务服务器 / 你的工作机）。
