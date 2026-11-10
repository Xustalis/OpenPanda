# Agent 与 Subagent 模型及并行配置

控制台入口为 **设置 → Agents**。可以分别填写每种 agent 的主任务模型、子任务模型与并发上限，并设置全局并发、委派开关、fanout、累计子任务数及派生深度。模型 ID 使用各 CLI 的原生写法，下面的模型名只是占位示例，需要替换为 CLI 实际可用的模型。

```yaml
injection:
  model: never
routing:
  max_concurrent: 6
  agent_concurrency:
    codex: 2
    claude_code: 2
  agent_models:
    codex: [my-root-model]
    claude_code: [my-claude-model]
  subagent_models:
    codex: [my-child-model]
    claude_code: [my-child-claude-model]
delegation:
  enabled: true
  max_concurrent: 3
  fanout: 3
  max_children: 12
  max_depth: 2
```

- `routing.max_concurrent`：当前 worker 的活跃 agent 调用上限，同时更新该 worker 的队列准入。0 继承能力卡容量，不代表无限。
- `routing.agent_concurrency`：每种 agent 的额外并发上限；与全局额度取交集，省略某个 agent 表示不额外限制。键可为能力卡中的名称或适配器文件名。使用适配器名时，同一适配器的别名合并计数；同时设置名称和适配器额度时两者均需满足。
- `routing.subagent_models`：只影响 OpenPanda 派生的子任务。省略某个键时继承该 agent 的主任务选择；显式 `[]` 使用子任务 CLI 的默认模型。界面清空表示恢复继承。
- `delegation.max_concurrent`：所有父任务共享的子 agent 活跃调用上限，0 表示只受全局和 agent 额度约束。
- `fanout`：单个父任务同时派发和等待的子任务数。它不等于生命周期并发上限；等待超时后仍执行的子任务继续占用执行额度。
- `max_children`：单个父任务整个生命周期允许派生的累计子任务数。`max_depth`：最大派生代数，根任务深度为 0。
- 调低并发不会杀死已有调用，后续调用等待空闲额度。父任务等待子任务时不占用 agent 执行额度。
- 限制针对当前 worker 的活跃适配器调用，不是集群全局配额，也不控制厂商 CLI 内部的隐藏子 agent 或空闲持久会话。普通部署沿用单一队列所有者；不要把多个独立 worker 的本地额度理解为跨进程硬信号量。
- 项目任务的多子任务批次在有效 `fanout` 大于 1 时，未声明 `paths`、`isolate` 或 `workdir` 的兄弟任务会默认使用隔离副本，从而具备并行资格；实际并发仍受 worker、agent 和委派额度限制，完成后通过三方检查合并。单个未隔离任务仍锁整棵项目树。显式声明不重叠 `paths` 可避免复制；共享工作树里的重叠路径仍会串行。

这些选项选择 CLI 原生模型，不转发新的 endpoint 或 API key。保持项目原有优先级：注入模型和显式 CLI 模型环境变量优先于原生候选列表。示例使用 `injection.model: never`，明确采用 CLI 自身登录/配置；不要在需要 Panda 提供模型凭据的节点上直接照抄这一项。需要覆盖 endpoint/凭据时继续使用现有 `injection.agents`。单模型 CLI 使用列表第一项；OpenCode 继续按本机可用模型选择候选。ZCode 可写 `provider/model`；只写模型名需要现有 provider 选择。自定义 generic 适配器需在 command 中使用 `{model}` 占位符。

保存后立即更新当前嵌入式 worker；共享配置的其他本地 worker 通常在约 1 秒内刷新，并在执行前再读取。运行中的调用保留自己的模型快照。

```sh
panda config routing set agent_models codex my-root-model
panda config routing set subagent_models codex my-child-model
panda config routing set max_concurrent 6
panda config routing set agent_concurrency codex 2
panda config delegation set max_concurrent 3
panda config delegation set fanout 3
panda config routing get --json
```

API: `GET /api/settings/execution`, `PUT /api/settings/execution` (Bearer token).

## 执行时限与上下文

- 每轮 agent 的候选切换、瞬时错误重试和凭据救援共用执行截止时间（任务覆盖值或全局时限，加收尾宽限），不会逐次重置整段预算。取消后不再启动候选。执行 panic 会结算当前 attempt 并释放执行资源。
- OpenCode 的 provider/session 失败事件会立即结束调用；收到回答后的终态 idle 也会结束仍保持管道打开的 CLI。初始 idle、工具失败及其他会话事件不会被当成本任务完成。
- 托管 Codex 任务保留磁盘会话以供中断和验收续跑；独立诊断调用仍可使用临时会话。支持 `thread.started` 与旧版 session 事件。
- agent 提示按复杂度使用 12,000 / 32,000 / 64,000 字节软预算，复杂度未知用 32,000。用户要求、验收标准、作用域和协议约束始终保留。技能正文及退化索引共用剩余预算；个人记忆索引至多选择六个相关主题并控制在 4,000 字节内，项目/个人隔离不变。命令参考按任务选择，完整命令仍可通过 `panda help` 查询。
- 上下文溢出时重试预算减半且不超过 32,000 字节；运行笔记可压缩至 12,000 字节，省略部分有明确标记并保留 UTF-8 边界。续跑仍携带完整任务要求，以支持冷启动候选。`exec_agent_start` 记录 `prompt_bytes` 与 `context_budget_bytes`，它们是字节数而非 token 数。
- 验收/摘要调用使用相同模型的独立短输出配置，上限 2,048 token、最多一次传输重试，并请求关闭扩展思考（具体行为取决于 provider）。验收总时限为 45 秒，失败仍进入人工复核；常规对话及 agent 的模型配置不变。查询已配置/当前模型使用只读 `model_list` 工具，无需派发 agent。

这些执行逻辑需部署新的二进制与适配器后生效；配置热更新不会替换已运行的程序代码。

## 调查任务与执行证据

- 任务 spec 可携带 `task_kind`：`code_question`、`investigation`、`edit` 或 `operations`。省略时保持旧任务兼容；该字段不是授权。调查/代码问答按只读 `plan` 执行，编辑仍受会话权限约束。深度仓库调查应派发 coding harness；未成功派发时不会把入口目录导览当作完成的审查。
- 调查需给出实际实现、调用者/相关测试的证据、覆盖范围与未验证项；静态调查不强制构建或跑测试。有限/截断搜索不能证明全仓库不存在。编辑需报告相关验证结果，运维需确认目标后置状态。只读任务不会自动运行 acceptance 命令。
- 每次 agent attempt 在 worker 本地记录 `execution_environment`：工作目录、git commit/分支/dirty 状态（包含未跟踪文件）及根 `AGENTS.md`/`CLAUDE.md` 的有界内容指纹。它不是完整规则索引或依赖预检，不读取规则正文到日志，不运行项目命令；git 探测失败时保留 unknown。
- `adapter_execution_profile` 在可信进程边界记录请求侧模型偏好、注入与工具策略、只读/授权状态、时限、恢复请求和注册表声明能力。持久会话后续轮次保留启动配置；这不是原生 CLI 的配置确认，实际模型/推理级别/版本未确认时为 unknown。profile 不含 prompt、凭据、provider URL、MCP 内容或命令模板。
- 这些是 worker 本地任务事件，不新增专用 UI 或跨节点环境同步；不代表已经实现完整结论证据账本或完成度验收。需要部署新的二进制后生效。

## 路由、权限与计划边界

- 概念问答仍可快速回答；具体代码符号/路径问题跳过无工具快答，需读取实际源码。深度仓库调查及有仓库上下文的深入追问（含 Side）需派发 coding harness；未派发、排队、审批中或失败不能被收敛摘要改写为完成。该路由不等于切换原生 CLI 的推理级别，也不是逐条结论的证据验证器。
- 全局工具策略是上限，任务只能进一步收窄；`extended` 不授予额外 OS 权限。Codex 普通执行使用 `workspace-write`，只读执行使用 `read-only`，不因工具策略切换到 `danger-full-access`。typed 调查/代码问答也强制只读，持久化的授权不能覆盖它；无只读沙箱的 native command 不可用于此类任务。
- `plan` 会话中的入口工具必须明确声明只读；本地快捷动作同样受限。`task_submit`/`task_budget` 保留受限委派路径。未知 MCP 工具没有该声明，默认拒绝；尚未提供可信 MCP 只读能力配置。技能读取只观察现有内容，不隐式安装内建技能。
- 多阶段计划保留各阶段 `task_kind`、会话权限及 agent roster，并以源工作区的有界快照作为阶段输入；缺少 artifact pool、打包失败或发生文件省略时在创建任务前失败。跨节点使用接收节点自己的阶段目录，不传 origin 的绝对工作路径。阶段产物继续通过 artifacts 交接，**不会自动写回或合并到原始 checkout**。
- 带工作区目录图或读取工具的分类不复用仅按路径建立的持久缓存，避免未提交修改后返回旧判断。验证证据只认已完成且未标错的命令工具结果及精确匹配的程序/参数，不认 `echo`、子串、未完成或失败命令；复杂 shell 组合不猜执行路径。该记录不代替独立验收通过。
- Side 仍是禁止派发和修改的侧问通道，不会为了深度检查自动启动 agent；无法满足调查要求时明确报告未完成，需回到主线程执行。快答/深度路由和模型自身推理配置是独立层，不能据此保证与原生工具相同的推理行为。
- 远端计划继续携带项目记忆，但不重复附加 project checkout；源快照及前置阶段输入顺序保持不变。
