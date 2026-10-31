# OpenPanda 实时输出与可观测性审查

日期：2026-10-31
范围：模型调用过程的可见性、token 展示、长任务防误判；对照 Claude Code 与 Codex CLI。

## 一、结论

OpenPanda 的流式传输本身没有问题，问题出在展示层的信息密度。

1. 普通问答把思维过程折成一行 60 字的尾巴，默认不展开；运行中不显示任何 token 计数；状态行只有旋转图标、阶段词和总耗时。任务卡（委派给 agent 的任务）反而信息完整，有流水线、阶段耗时、当前工具和 agent 最后三行输出。
2. token 计数在进程内已经实时可读（`entry.Client.Usage()` 是两个原子计数器），只是没有接进任何运行中的 UI，只在回合结束后出现在结果行。Web 面板连结束后的 token 都拿不到，`AskResult` 里没有这个字段。
3. 防误判卡死的手段只有 10fps 的 spinner 和一个从回合开始计时的 clock。没有「最后一次输出距现在多久」，也没有「首字节等待」的单独阶段。模型 prefill 阶段最长可静默 120 秒（`ResponseHeaderTimeout`），这段时间界面与卡死无法区分。
4. Claude Code 和 Codex 都没有做「每次模型调用输出一句总结」。它们的做法是：状态行常驻 token 与耗时，思考默认折叠成一行摘要，工具调用按动作聚合，回合结束给一行「Cooked for Ns」。OpenPanda 的 `Progress` 事件在语义上已经覆盖了大部分，缺的是常驻展示和运行中计数。

## 二、当前实现审查

### 2.1 输出链路与刷新策略（上一轮报告）

三层结构：provider SSE 解析 → 统一 delta 回调 → 各前端自己的渲染节流。

模型接入层在 `internal/entry/stream.go`。`StreamTurnsWithTools` 按 api type 分派到 Anthropic Messages、OpenAI Chat Completions、OpenAI Responses、Google 四种 SSE 实现，对外统一为 `onDelta` 与 `onReasoning` 两个回调。关键不变量是「用户已见的文本不可重放」：`streamWithRetry`（stream.go:134）只在 `deltaGuard` 判定未交付时重试，一旦有字节到达用户，429/5xx/断连只报错不重放。`deltaGuard`（stream.go:203）负责剥除 inline `<think>`、抑制结构化 JSON 与 DSML 工具标记，并在流结束时 flush 被扣住的字节。

消费端有四条链路，刷新策略各不相同：

| 层级 | 节奏 | 机制 |
|---|---|---|
| provider SSE → 回调 | 每个 chunk | 逐行解析，无缓冲 |
| TUI 重绘 | ≤60fps | Bubble Tea renderer 合帧 |
| TUI live markdown | ≤11 次/秒 | 90ms 节流（tui_view.go:886）+ 同源缓存 |
| TUI 已提交块 | 参数变化时失效 | 逐块渲染缓存（tui_model.go:340） |
| 经典 CLI 正文 | 行边界 | 行缓冲，未完行放在状态行预览（ask.go:390） |
| CLI 状态行 | 80ms | ticker 重绘（cliui.FrameInterval） |
| Web 服务端 | 每 delta | 立即 Flush，无合并（sessions.go:631） |
| Web 前端 state | ≤60fps | requestAnimationFrame 合并（sessions.tsx:1447） |
| Web 变更流 | 1s 轮询指纹 | 变了才推（events.go:24、163） |
| 跨进程任务事件 | 5s 兜底 | 进程内事件监听优先（await.go:41） |
| 心跳 / 判死 | 5s、15s / 45s | ask 流、events 流、浏览器看门狗 |

任务与 agent 侧另有一条流。adapter 以 NDJSON 写 stderr，`progressWriter` 按行解析，`core/run.go` 把结构化事件写进 `task_events`（单任务上限 2000 条），多跳时经 `bus.MsgTaskProgress` 逐跳中继。发起端等待结果用双通道：同进程 `store.AddOnEvent` 即时唤醒，跨进程 5 秒轮询兜底。

### 2.2 用户在运行中实际能看到什么

普通问答回合（TUI）：

- 状态行（tui_view.go:945）：spinner、动词、可选模式徽章、引擎 note、`(总耗时 · 中断提示)`。
- 思维过程（tui_view.go:925）：默认一行 `✻ 思考中 · 最后 60 字`。Ctrl+O 展开全部思路线。
- 答案正文：流式渲染，长回答只显示窗口尾部 `height-10` 行。
- 回合结束后（tui_turn.go:372）：`模型 · 耗时 · N tokens · ($x)`。

委派任务回合：任务卡信息完整。pipeline 四段（triage → route → exec → judge）、每个阶段的耗时、当前工具名与持续时长、agent 自己最后三行发言、总时钟（tui_task.go:168）。卡片注释写明保留三行的理由就是防「读起来像卡死」。

Web 线程：思考 `<details open={streaming}>`，流式期间展开、结束折叠（sessions.tsx:3422）；回答流式渲染；运行中有耗时；没有 token。

经典 CLI（`panda ask`）：未完成的行作为状态行预览，完成行直接打印；结束时状态行给 `SetTokens`（ask.go:658）。

### 2.3 折叠发生在哪

| 位置 | 默认状态 | 展开方式 | 代价 |
|---|---|---|---|
| TUI 思维过程 | 一行 60 字 | Ctrl+O | 展开后占满屏幕，且只有这一种粒度 |
| TUI 任务卡 agent 输出 | 最后 3 行 | 无（完整流在 `panda task <id>`） | 长任务中上下文丢失快 |
| TUI 普通回合重启点 | 无阶段历史 | 无 | note 被覆盖，前一步消失 |
| Web 思维过程 | 流式时展开 | 结束时自动折叠 | 结束后想回看要再点 |
| CLI 未完成行 | 状态行预览 | 无 | 长段落看不到更新 |

真正的问题不在「折叠」本身，Claude Code 也折叠。问题在折叠后的那行信息量。OpenPanda 的折叠行只给最后 60 字，没有行数、没有耗时、没有「正在第几轮」。用户无法判断模型是在思考、在等首字节，还是已经挂了。

### 2.4 token 的现状

- 采集：`entry.Client` 在每次成功流结束后 `addUsage`（model.go:748），只统计成功调用，避免重试双计。Anthropic 的 `message_start` 带 input、`message_delta` 带累计 output；OpenAI 需要 `stream_options.include_usage`，通常只在最后一块出现。
- 读取：`Client.Usage()` 返回两个原子计数（model.go:740），运行中随时可读。
- 展示：只在结束后。TUI 结果行、CLI 状态行。Web 完全缺失，`AskResult` 没有 token 字段，服务端也没把 `out.Tokens()` 放进结果 JSON。
- 多轮：`askengine` 主循环最多 6 轮（askengine.go:1976），`ProgressExec` 的 note 会带 `(2/6)`，但运行中看不到累计 token。

## 三、外部调研

### 3.1 Claude Code

运行中的 spinner 行同时显示耗时与 token 计数，格式形如：

```
✽ Reviewing script for issues… (esc to interrupt · ctrl+t to show todos · 1m 25s · ↑ 887 tokens)
```

思考默认折叠成 `∴ Thought for 3s (ctrl+o to show thinking)`，Ctrl+O 展开。展开看到的是模型生成的思考摘要，原始 chain-of-thought 不落地。社区因此提出过「默认常显思考」的需求（issue #8477），也存在通过补丁强制显示的方案。

工具调用按动作聚合：运行中显示现在进行时（如 "Searching for 1 pattern, reading 2 files…"），回合结束后收敛成一行过去式总结（"Read 1 file, ran 1 shell command"），并给 `✻ Cooked for 8s` 的回合总结。这个聚合也带来过争议，issue #46110 抱怨多个工具调用被并成一行，丢掉了逐步过程。

结论：Claude Code 的「一句总结」出现在回合结束和工具聚合两个位置，模型调用本身靠流式文本自己表达，没有逐调用的一行摘要。token 与耗时是常驻状态行，不是结束后才出现。

### 3.2 Codex CLI

TUI 底部有一条可配置状态行，通过 `/statusline` 选择内置项，默认 `model-with-reasoning · context-remaining · current-dir`。可选项目包括：

- `run-state`：Ready / Working / Thinking
- `used-tokens`、`total-input-tokens`、`total-output-tokens`
- `context-remaining`、`context-used`、`context-window-size`
- `five-hour-limit`、`weekly-limit`、`task-progress`（来自 `update_plan`）

思考展示由 `model_reasoning_summary` 控制（auto / concise / detailed / none），当前模型默认多为 none，也就是默认看不到思考。`tui.stream_reasoning_live` 可以开启逐块流式思考，默认关闭，开启后约 60ms 节流。社区实测过 TUI 不渲染 reasoning summary 的 bug（issue #31216），同期有「thinking timer」的改动。

结论：Codex 把可观测性放进常驻状态行，token 与上下文占比随时可见，思考默认不展示，需要显式配置。

### 3.3 两个产品的共同点

1. 运行中的状态行常驻「时间 + token」这两个数字，不需要用户按任何键。
2. 思考默认折叠，但折叠行自带耗时（"Thought for 3s"），让人知道它确实在动。
3. 工具与阶段按动词聚合，一条线代表一类动作，结束后转成过去式。
4. 回合结束给一行总耗时总结。
5. 都没有按「每次模型调用」逐条输出摘要，粒度是回合与工具。

## 四、差距

| 维度 | OpenPanda 现状 | Claude Code / Codex | 影响 |
|---|---|---|---|
| 运行中 token | 无 | 常驻状态行 | 用户不知道烧了多少，也不知道有没有进展 |
| 思考折叠行信息量 | 最后 60 字 | 折叠 + 耗时（Thought for 3s） | 无法区分思考与卡死 |
| 状态行新鲜度 | 总耗时 | 总耗时 + token 增长 | 静默期看起来与挂起一致 |
| 首字节等待 | 并入总耗时 | 单独状态 | prefill 120 秒上限内无反馈 |
| 工具/阶段历史 | note 被覆盖，任务卡才有 trail | 聚合行 + 分层折叠 | 普通回合看不出走了几步 |
| 轮次可见性 | Exec note 带 (2/6) | 类似 | 可用但没有累计成本 |
| Web token | 完全没有 | 状态行有 | 面板用户只能等结果 |

## 五、建议方案

按改动成本与收益排序。前两项是用户这次诉求的核心。**P0 已实现**，实现方式见本节末尾的落地说明。

### P0 运行中 token 计数

数据已经在内存里，不需要改计费逻辑。

1. 在 `entry` 层增加一个只读回调，流中途每收到 usage 就上报累计值。Anthropic 在 `message_start` / `message_delta` 处已经解析了 usage，只是存进了 `anthAccumulator`，流结束才并入 `Client`。可以新增 `StreamCallbacks.OnUsage(entry.Usage)`，由 askengine 转成 `Progress{Kind: ProgressUsage, Input, Output}`。
2. 不要在 `noteUsage` 里直接 `addUsage`，否则重试的尝试会被重复计费。保持「成功才记账」，实时值用独立的累计器承载。
3. OpenAI 方言拿不到中途 usage 时，用已流出的字节数做本地估算，标注为估算值；拿到真实值后覆盖。
4. TUI 状态行追加 `↑ N tokens`，与 elapsed 并列（tui_view.go:945）。
5. Web 服务端在 `statusEventOf` 或新事件里带上累计 token，前端渲染在耗时旁。
6. `AskResult` 增加 `tokens` / `usage` 字段，Web 结果卡补上。

成本：entry 一个回调、askengine 一个 ProgressKind、两个前端各一处渲染。约一天。

### P0 活动心跳与首字节阶段

1. TUI 状态行显示「最后活动 N 秒前」，由最近一次 delta/reasoning/progress 的时间戳算。超过 15 秒变黄，超过 45 秒变红。
2. 在 `entry` 层区分「等待首字节」与「流式输出中」。请求发出后到第一个 SSE 事件之间单独上报 `Progress{Kind: ProgressAwaitModel}`，状态行显示「等待模型响应 (Ns)」。`ResponseHeaderTimeout` 是 120 秒，把这个窗口可视化出来比缩短它更有效。
3. Web 端 `AskStatusEvent` 增加时间戳字段，前端用同样的颜色规则。

成本：一个时间戳字段加渲染分支，半天。

### P1 思考折叠行的信息量

Claude Code 的折叠行带耗时，这一点直接可借鉴。

1. TUI 折叠行改为 `✻ 思考中 · 12 行 · 3.4s · 最后一句`，行数与耗时取代 60 字截断（tui_view.go:925）。
2. 思路结束后，折叠块同样带 `思考 3.4s`。
3. 已确认 provider 是否下发 reasoning。项目当前走 DeepSeek 的 Anthropic 兼容端点，如果该端点不返回 thinking 块，`onReasoning` 永远为空，思维链就无数据可显。这种情况下应显示「模型未提供推理输出」而不是假装在思考。这是方案成立的前提，需要先实测一次。

成本：半天，前提是 provider 有 reasoning。

### P1 阶段与轮次常驻

1. 普通回合也保留阶段链，不覆盖 note。任务卡已经有 pipeline 和 trail（tui_task.go:148-206），把这套结构复用到非任务回合的分类 → 路由 → 执行 → 评审。
2. 每轮模型调用结束给一行短总结（`第 2/6 轮 · 分类完成 · 2.1k tokens`），粒度对齐 Claude Code 的回合总结，而不是每次调用一条。

成本：一到两天。

### P2 工具调用聚合

任务卡已经把工具聚合成 `N ops`（tui_task.go:80-135）。非任务的工具调用（MCP 工具，如 `skill_find`、`task_submit`）目前只有一行 note，可以考虑按动作聚合并保留最近若干条。

### 落地说明（P0，2026-10-31）

- `entry` 层新增 `WithUsageObserver`（stream.go），四种方言在收到 provider usage 时上报，150ms 节流，流结束时 force 上报一次，且在 `addUsage` 之前，避免双计。
- `entry.ClassifyStreamWithTools` 通过 `WithStreamUsage` 把观察器透传给内部流式调用。
- `askengine` 新增 `ProgressUsage` 与 `usageTracker`：provider 快照按增量累计，客户端 fallback 造成的回落只重置基线；按字符估算输出（约 3 字节/token），回合结束时用账单值覆盖估算；fallback 下账单值小于累计值时保留较大的累计，状态行不倒退。
- TUI 状态行（`statusMeta`）显示累计 token、`等待模型响应 Ns` / `已静默 Ns`；思考折叠行显示行数、耗时与最后一句。任务卡持有同一计数器，长时间任务执行期间同样可见。
- 经典 CLI 的 `Status.SetTokens` 在运行中更新；审批恢复等逐行打印路径过滤 usage，不会产生空 bullet。
- Web 新增 `usage` SSE 事件，线程头部常驻 token；运行中的耗时旁显示 `等待模型响应` / `已静默`；`AskResult.tokens` 供结果使用，`sessions.Turn.Tokens` 随轮次持久化，刷新后仍能看到每轮消耗。
- `--json` 等只绑定 OnStatus 的调用者不再收到空的 usage 状态行。
- 测试：`internal/entry/stream_usage_test.go`、`internal/askengine/usage_test.go`、`cmd/panda/tui_status_test.go`。

### 尚未做（P1/P2）

思考默认仍折叠；provider 是否下发 reasoning 需要实测确认；工具调用的聚合未动。

## 六、约束与风险

1. **provider 是否给 reasoning 是前提。** 没有 reasoning 数据时，唯一能展示的是阶段与 token，方案里的「思维链」应降级为「阶段链」。
2. **实时 usage 与计费分离。** 实时计数用于展示，计费仍走现有 `addUsage` 的成功路径，两套数字的语义要在代码注释里写明，避免以后有人把实时值当账单。
3. **状态行宽度。** TUI 状态行已经承载动词、徽章、note、耗时、中断提示，再加 token 与心跳会挤。建议 token 与耗时合并成 `1m 25s · ↑ 887 tok`，心跳只在超过阈值时出现。
4. **Claude Code 的前车之鉴。** 过度聚合会引发「看不到过程」的抱怨（issue #46110、#89318）。OpenPanda 的任务卡折叠了三行 agent 输出，如果再加聚合，应保留展开入口。

## 七、来源

- Claude Code 交互模式文档：https://code.claude.com/docs/en/interactive-mode
- Claude Code 思考默认折叠与 Ctrl+O：https://github.com/anthropics/claude-code/issues/8477
- Claude Code 工具调用聚合争议：https://github.com/anthropics/claude-code/issues/46110
- Claude Code 回合总结与工具聚合复刻说明（pi-claudify）：https://www.npmjs.com/package/@owlburtoe/pi-claudify
- 「Worked for」改「Turn took」的记录：https://cdn.jsdelivr.net/npm/cc-my-pi@1.3.3/CHANGELOG.md
- Codex CLI 状态行配置项：https://github.com/openai/codex（`/statusline`，`[tui] status_line`）
- Codex 状态行实测：https://vladkens.me/coding-agents-statusline/
- Codex reasoning 展示与 `model_reasoning_summary`：https://antiburn.com/guides/codex/model-reasoning-summary/
- Codex 流式 reasoning 的 PR（默认关闭、60ms 节流）：https://github.com/openai/codex/pull/6006
- Codex TUI 不渲染 reasoning 的 issue：https://github.com/openai/codex/issues/31216
- Codex 状态行与用量查看：https://www.jdhodges.com/blog/codex-usage-cli-status-line/
