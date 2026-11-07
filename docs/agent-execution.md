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
