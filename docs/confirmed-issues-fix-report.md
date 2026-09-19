# OpenPanda 已确认问题与修复方案

> 日期：2026-09-09  
> 范围：CLI Bubble Tea TUI、审批与取消、Web 会话并发、节点身份与结果回放  
> 当前状态：问题分析与修复方案已记录；本文档创建时**未实施源码修复**

## 1. 文档目的

本文集中记录实际交互与代码路径中已经确认的问题、根因、影响、建议修复方式、回归测试和完成边界。
后续工作应直接按本文解决已知问题，不再扩大为开放式 code review。

本文严格区分三类状态：

- **已确认，待修复**：现象已通过真实 PTY、代码路径或并发时序确认。
- **部分已有改动**：工作树中已经存在相关未提交改动，但尚未完成整体验证，不能视为已修复。
- **协议迁移待办**：无法用局部字符串或状态补丁正确解决，必须单独设计兼容迁移。

## 2. 调查结论摘要

### 2.1 鼠标是否会自动中断

结论不是“任何鼠标事件都会中断”：

- 纯鼠标移动不会中断生成。
- 普通双击如果不落入错误命中区域，不会中断。
- 当前程序默认启用了 `tea.WithMouseCellMotion()`，终端会把鼠标交给应用，破坏或干扰终端原生的文本选择、双击选词和滚轮体验。（2026-09-16 起默认交还终端，见 §3.1；`ui.mouse: scroll` 可恢复捕获。）
- 左键按下若落在底部错误扩大的 Stop 命中区域，会立即调用取消；双击或拖选从该区域开始时，第一次按下就可能中断。
- 因此用户感知到的“双击自动中断”是真实可复现的问题，但根因是默认鼠标捕获与错误命中区域叠加，并非鼠标移动本身。

### 2.2 总体风险

| 领域 | 主要风险 | 当前状态 |
|---|---|---|
| TUI 鼠标与按键 | 误取消、无法原生选择、退出语义混乱 | 已确认，待修复 |
| 流式事件 | 旧请求增量污染新请求 | 已确认，待修复 |
| 命令执行 | 界面空白、无法取消、全局 stdout/stderr 串扰 | 已确认，待修复 |
| 审批 | 已完成工作被报告失败，确定性失败原样重跑 | 已确认，待修复 |
| 远程取消 | 下游取消后本地 origin 仍停留在运行态 | 已确认，待修复 |
| Web 会话 | 旧请求清理新请求、Stop 取消历史任务 | 已确认，待修复 |
| 节点身份 | 稳定名称被误判为 ephemeral sibling | 已确认，待修复 |
| Outbox | 请求方重启后旧结果无法投递 | 协议迁移待办 |

## 3. TUI 交互与布局问题

### 3.1 默认鼠标捕获破坏终端原生交互

**现象**

`cmd/panda/tui.go` 创建程序时同时启用 Alt Screen 与 `tea.WithMouseCellMotion()`。终端进入应用鼠标协议后，原生双击选词、拖动选择和滚轮行为会被改变。

**根因**

TUI 的主要内容是聊天文本，鼠标选择价值高于少量按钮点击；但当前默认策略反过来优先捕获鼠标。同时 `tui_update.go` 的注释声称默认不捕获，与实际启动代码不一致。

**修复方案**

1. 默认移除 `tea.WithMouseCellMotion()`，保留 Alt Screen。
2. 以键盘作为 Stop、Steer、Thought、审批和 slash 菜单的可靠主路径。
3. 若未来提供鼠标模式，必须通过显式设置开启，并让渲染布局与命中矩形共享同一份计算结果。

**实施记录（2026-09-16）**

按上表第 1 条直接关闭鼠标捕获会踩到另一个坑：备用屏幕没有终端自身的 scrollback，滚轮一旦不交给应用就无处可去，用户会看到"复制修好了，滚动没了"。因此实际实施把第 3 条一并做掉，并补上不依赖鼠标的滚动通路：

| 动作 | 实现 |
|---|---|
| 默认鼠标归属 | `mouseSelect`：终端持有鼠标，拖选、双击选词、终端原生复制快捷键全部恢复（`cmd/panda/tui_mouse.go`） |
| 滚轮仍要能滚 | 启动时请求 DECSET 1007（alternate scroll），备用屏幕内的滚轮/触摸板上下滑转为 ↑/↓，由 `arrowScrollLines` 还原为对话滚动 |
| 键盘兜底 | PageUp/PageDown 在 idle、asking、exec 三种模式下都可用；`pageLines` 统一翻页步长 |
| 不抢编辑键 | 仅在输入框单行且 slash 菜单未打开时 ↑/↓ 才归滚动；多行草稿与菜单导航不受影响 |
| 需要点击时 | `ctrl+t`（或 F2）运行时切换回捕获模式，`ui.mouse: scroll` 可改默认，`PANDA_MOUSE=scroll` 单次覆盖 |
| 可取回性 | 切换时在对话区公告当前模式，状态行常驻 `ctrl+t` 提示；回看历史时提示补上 ↓/PgDn/Esc |

回归验证：`scripts/tui-mouse-pty-check.py` 用真实 PTY 冷启动二进制，断言默认模式只发 1007h、不发 1002h/1003h，↑ 仍可滚动，退出复位 1007l；`PANDA_MOUSE=scroll`、`ctrl+t`、`ctrl+t` 连按两次各自路径同样被断言。单元测试见 `cmd/panda/tui_mouse_test.go`。

**为什么不需要在"滚动"和"复制"之间二选一（2026-09-16 追加）**

上面的取舍建立在一个未经检验的前提上：捕获鼠标就等于失去复制。这个前提是错的。桌面终端都为这种情况留了旁路——拖拽时按住某个修饰键，终端会忽略应用的鼠标上报，改用自己那套选择。

| 终端 | 旁路键 | 说明 |
|---|---|---|
| iTerm2 | 按住 `Option` 拖选 | 松手即自动写入剪贴板，无需 `Cmd+C`。官方文档：*"If mouse reporting is enabled … pressing option will temporarily disable it so you can make a selection."* |
| Apple Terminal.app | 按住 `Fn` 拖选 | 另有 `⌘R`（View > Allow Mouse Reporting）做长期切换。注意此处 `Option` **无效**：鼠标上报开启时它被映射为 Meta 交给应用，绕不过去 |

因此捕获夺走的是"不带修饰键的复制"，而不是复制本身。两个模式的差别也只剩"哪个手势需要多按一个键"：

- `mouseSelect`（默认）：拖选、双击选词、终端复制快捷键直接可用；点按钮要先 `ctrl+t`。
- `mouseScroll`：滚轮与点击原生；选择需按住 `⌥`/`Fn`。

默认仍取 `mouseSelect`，因为对话区以文本为主、复制是高频动作，而所有点击目标都有键盘等价物（y/n、方向键+Enter、Esc）——该模式没有丢失功能，只少了一条快捷方式。

随这次追查修掉的两个暗坑：

| 问题 | 后果 | 修复 |
|---|---|---|
| `ctrl+t` 切回 `mouseSelect` 时未重新请求 1007 | 切换后滚轮失效；Bubble Tea 自身从不发送该序列，于是漏洞只出现在"切换"这一条路径上 | 新增 `altScrollCmd`，用 `tea.Sequence` 让 1007 双向跟随模式 |
| `onMouse` 在 `modeExec` 直接 return | 任务执行期间滚轮无响应；而同一时刻 PgUp/PgDn 与 select 模式的 ↑/↓ 都能滚，两个模式对"能否滚动"给出不同答案 | 滚轮分支提到模式闸门之前，exec 期间照常滚动；点击仍被拦住，避免误触中断任务 |

补测：PTY 断言由三条路径扩到四条（新增"连按两次 `ctrl+t` 后 1007 是否重新武装"）；单元测试新增 `TestAltScrollFollowsTheMode`、`TestWheelScrollsWhileExecRuns`、`TestClicksStayIgnoredWhileExecRuns`。

**回归验证（原始）**

- 冷启动真实二进制，确认输出中未启用鼠标 cell-motion 协议。
- 双击、移动、拖选不取消生成。
- 键盘 Esc/Enter/Ctrl+O 与 y/n 仍可完整操作。

### 3.2 Asking Stop 命中区域覆盖输入框

**现象**

`askingButtonHit` 把终端最后两行及过宽的 X 范围视为 Stop/Inject/Thought。Stop 最少扩到 22 列，即使可见按钮远小于该范围；输入框边框和空白处也可能被当作 Stop。

**根因**

命中测试使用独立的经验常量，没有复用最终 `View()` 的真实行列布局。

**修复方案**

- 删除 `max(22, ...)`、`max(45, ...)`、`max(68, ...)` 式扩大范围。
- 从最终 footer 行计算三个可见标签的精确起止列。
- 只在标签所在的唯一行响应左键 press；release、motion、边框、空白一律忽略。

### 3.3 审批点击 Y 坐标错误

**现象**

审批卡在 `mainChatView` 中上方还有内容、下方还有输入区，但 `approvalHit` 使用 `height-len(card)` 推导 Y。可见 `[y]/[n]` 可能点不到，错误位置反而可能命中。

**影响**

审批属于不可逆操作入口，任何坐标误判都不能按普通 UI 瑕疵处理。

**修复方案**

- 由统一 layout 函数返回审批选项的最终屏幕矩形。
- 任何不在选项矩形内的点击必须是 no-op。
- 默认鼠标关闭后仍保留 y/n、方向键和 Enter 的安全路径，默认焦点保持拒绝。

### 3.4 Esc、Ctrl+C 与草稿语义不一致

**现象**

- Asking 时 Esc 立即取消当前生成。
- Idle 时 Ctrl+C 立即退出，即使输入框有未提交草稿。
- “连续两次中断退出”在普通取消路径中不完整：第一次取消后已回到 idle，第二次走 idle Ctrl+C 的立即退出逻辑，而 Esc 则不一致。

**修复方案**

采用统一、可测试的状态机：

1. 输入框有草稿时，首次 Esc/Ctrl+C 只清空草稿并关闭菜单。
2. Asking 且无草稿时，首次中断明确取消当前生成，保留已经显示的内容或记录停止说明。
3. 在固定时间窗内第二次中断才退出。
4. UI 文案写“取消生成”，不再用“停止等待”混淆模型请求、持久任务和后台 watcher 的所有权。

### 3.5 Asking 期间不能回看长输出

**现象**

PageUp/PageDown 只在 idle 处理；生成长回答时无法回看。普通输入还会把 `scrollOffset` 重置为 0。

**修复方案**

- Asking 模式处理 PageUp/PageDown。
- 若显式开启鼠标模式，再映射滚轮。
- 只有用户位于底部时新 delta 才自动跟随；用户主动回看时不得抢回底部。
- 编辑输入不应无条件清除滚动位置。

### 3.6 Steering 静默丢失

**现象**

`injectSteer` 使用非阻塞 channel send；队列满时静默走 `default`。UI 仍清空输入、写入 pending prompt 和聊天历史，用户会看到“已注入”，但引擎实际未收到。

**修复方案**

- `injectSteer` 返回 `bool` 或 typed result。
- 只有成功入队后才清空输入并写入 UI/历史。
- 队列满时保留草稿并显示明确提示。

### 3.7 极窄窗口溢出

**现象**

resize 时 textarea 宽度使用 `max(20, width-4)`；`textWidth()` 同样强制 20。终端窄于约 24 列时，组件宽度超过窗口。

**修复方案**

- 实际组件宽度 clamp 到 `[1, available]`。
- 极窄终端使用简化 footer/banner，不强制不存在的 20 列空间。
- 测试必须验证每行显示宽度不超过窗口，而不只是验证 View 非空。

### 3.8 长流输出的重复复制与重排

**现象**

`liveAnswer += chunk` 持续复制累计字符串；每帧又格式化完整答案并裁剪尾部。输出越长，累计成本越接近二次复杂度。

**修复方案**

- 使用 chunk buffer 或受控 builder 保存增量。
- 缓存按宽度格式化后的视图，宽度变化时失效。
- 只增量处理新文本，或将 repaint 合并到固定刷新节奏。
- 增加长流正确性、allocation 和耗时上界测试。

### 3.9 Alt Screen 退出后的结果可见性

Alt Screen 退出后会恢复原屏幕，用户可能认为会话消失。应在退出前把最后结果摘要打印回普通终端，或提供明确的持久化/恢复提示；不能让 Alt Screen 成为唯一可见副本。

## 4. 流式事件隔离

### 4.1 旧请求事件可写入新请求

**现象**

`doneMsg` 和 `resumedMsg` 携带 `*askStream`，但 `deltaMsg`、`reasoningMsg`、`progressMsg` 只有 payload。用户取消 A 后快速发起 B，A 的迟到事件没有来源身份，可能被折叠进 B 的 `liveAnswer`、thought 或 task card。

**放大因素**

`onDelta` 和 `onProgress` 收到事件后调用 `waitForActivity(m.stream)`；如果 `m.stream` 已经是 B，A 的消息会替 B 续泵，混淆流和 goroutine 生命周期。

**修复方案**

- 所有事件统一携带 source stream 或单调 generation：
  - delta
  - reasoning
  - progress
  - done
  - resumed
- handler 首先检查 `event.stream == m.stream && !event.stream.detached`。
- 旧流事件直接忽略；需要继续泵时只 re-arm `event.stream`。
- terminal message 同样要求精确等于当前 stream，不能只检查 detached。

**回归测试**

1. A 发出迟到 delta，B 的答案不变化。
2. A 发出迟到 reasoning/progress，B 的 thought/card 不变化。
3. A 的 terminal completion 不清空 B。
4. drop 后等待中的 pump 与阻塞 send 均退出，无 goroutine 泄漏。

### 4.2 drop 的语义需要准确命名

当前 `drop()` 同时标记 detached、取消 engine context、关闭 pump。它不是单纯“停止等待”。对未被 Core 接管的请求，它会取消执行；对已有独立所有权的持久任务，最终行为由 Core 决定。

修复时应把 UI 文案和代码注释按实际所有权分层：

- 取消当前模型/前台请求；
- 取消正在恢复的审批任务；
- 取消持久任务树；
- 仅停止本界面展示。

这些动作不能继续用一个模糊的“Stop”承诺覆盖。

## 5. Slash 与 Shell 命令生命周期

### 5.1 进程级 stdout/stderr 替换

**现象**

`tui_exec.go:captureOutput` 临时把全局 `os.Stdout`、`os.Stderr` 指向 pipe。即使有 mutex，它仍会截获其他 goroutine 在此期间的日志或输出，并污染并发行为。

**修复方案**

- REPL command handler 改为显式接收 `io.Writer`。
- 外部 shell 使用 `exec.CommandContext` 与独立 stdout/stderr pipe。
- 无法立即 writer 化的旧 handler 应在隔离子进程执行，不能继续替换进程全局 fd。

### 5.2 命令界面空白且不可取消

**现象**

- `modeExec` 的 `View()` 返回空字符串。
- `onKey` 在 `modeExec` 直接忽略所有按键。
- `runSlash` 没有 cancellable context。
- `!sleep 999` 或 `/tasks watch` 会让界面像死机一样空白，Esc/Ctrl+C 无效。

**修复方案**

- 为每次命令建立 context、cancel 与 generation。
- modeExec 继续渲染聊天、命令标题、已产生输出和 Esc/Ctrl+C 提示。
- completion 消息携带 generation，旧命令完成不得覆盖新命令状态。
- Esc/Ctrl+C 使用 `CommandContext` 终止子进程并回到 idle。

### 5.3 `/tasks watch` 不应作为一次性命令

`/tasks watch` 是长驻订阅，应使用专门的 tea.Cmd/event pump，逐条推送并支持取消；不能包进等待函数返回的普通 slash dispatch。

## 6. 审批语义与结果状态

### 6.1 接受既有工作默认被报告为失败

**现象**

`askengine.acceptReviewedWork` 反序列化空或旧 `ResultJSON` 时使用零值 `TaskResultPayload`，其中 `OK=false`。随后任务可能已从 review 转成 done，但 CLI/TUI 收到失败结果。

**修复方案**

先初始化：

```go
result := bus.TaskResultPayload{OK: true}
```

再覆盖存在的持久 payload，行为与 Core 的接受路径一致。必须添加空 JSON、旧 JSON、完整 JSON 三类回归测试。

### 6.2 任意失败后的 review 都可能原样重跑

**现象**

`reviewFromFailure` 对旧事件使用 `EvResult` 且包含 `"failed"` 的宽泛判断。上下文窗口超限、输入错误等确定性失败会被视为 `ApprovalResumeExecution`，批准后用相同输入重跑，不可能自行恢复。

**修复方案**

引入结构化 approval disposition/review kind：

- `accept_work`：工作已经执行，只接受现有结果。
- `resume_execution`：仅适用于明确的授权拒绝或“执行从未开始”。
- `needs_changed_input`：上下文超限、scope/input 等确定性错误，需要用户修改输入，不能原样重跑。

旧数据兼容必须保守：只有能明确识别为 authorization refusal 的记录允许 resume，其余不能凭 `"failed"` 子串重跑。

### 6.3 CLI approve 不应为纯存储操作初始化完整引擎

`cmd/panda/panel.go:runApprove` 当前先构造包含模型、card、MCP 的 ask engine。纯 `accept_work` 可能被无关配置错误阻塞。

应先通过轻量 TaskStore/Core approval service 查询 disposition 并接受已有工作；只有 `resume_execution` 才创建执行引擎。

## 7. 远程取消与任务终态

### 7.1 普通远程 Submit 取消后本地任务不终结

**现象**

普通远程分支在 `ctx.Done()` 时只调用 `forwardCancelDownstream` 并返回 `ctx.Err()`。没有 daemon monitor 的前台 ask/repl 进程可能留下本地 origin 行处于 dispatched/running。

**修复方案**

使用不受原取消 context 影响的 cleanup context：

1. `cleanup := context.WithoutCancel(ctx)`。
2. 调用 `CancelTree(cleanup, taskID)`，由统一路径取消下游并级联本地状态。
3. 重新读取最终任务行并返回真实终态。
4. forward、context prepare、lease 部分失败也必须进入统一 terminalize helper。

### 7.2 固定 timeout 与 lease 续期冲突

健康任务可能持续续租，但固定 wall-clock timeout 仍会误杀。超时判断应依据最新持久 lease 状态循环判定，而不是只看首次发送时间。

### 7.3 队列删除必须以终态为前提

批量清队列时，取消失败不能继续删除仍活动记录。只有确认任务已终态或取消成功后，才允许从队列移除。

## 8. Web 审批与长操作

### 8.1 浏览器断线会取消真实审批执行

Approval handler 同步调用 `ResumeApproved(r.Context())`。浏览器主动中止或通用 60 秒 timeout 会取消服务器中的真实任务。

**修复方案**

- `accept_work` 可同步完成。
- `resume_execution` 创建持久 operation/task 后立即返回 `202` 与 operation/task ID。
- 后台执行使用服务生命周期 context，不使用请求 context。
- 只有显式 Cancel API 可以取消任务。

### 8.2 HTTP 200 被错误解释为批准成功

review、failed、cancelled、expired 当前也可能返回 200；前端 action 因此无条件显示 approved。

**修复方案**

API 返回 typed union，例如：

- `done`
- `running`
- `review`
- `failed`
- `cancelled`
- `expired`

前端只对真实 `done` 显示批准成功；running 显示 operation，其他状态显示原因和后续动作。

### 8.3 通用 60 秒请求超时不适用于审批执行

request helper 应支持端点级 timeout 和外部 signal。启动长操作不应在浏览器内等待执行完成，也不应让网络生命周期成为任务所有者。

## 9. Web 会话并发与 Stop

### 9.1 activeAsks 的 stale unregister race

**时序**

1. A 注册 session cancel。
2. B 替换 A，取消 A，并存入 B cancel。
3. A 的 deferred `unregisterSessionAsk(id)` 执行。
4. A 无条件删除同 session key，误删 B。
5. Stop 再也找不到 B。

**修复方案**

map value 改为 `{generation, cancel, operationID}`：

- register 返回 generation；
- unregister 只在当前 generation 相等时 compare-and-delete；
- cancel API 校验当前 operation ID，不能只靠 session ID。

### 9.2 旧请求 finally 清除新请求 busy

前端虽然只在 controller 相等时清理 refs，但 `setBusy(false)` 在 guard 外。A 的 finally 可以在 B 运行时把 busy 设为 false。

所有 busy/error/ref/finally 更新必须在相同 controller 或 generation guard 内完成。

### 9.3 Stop 会取消历史任务

当前 Stop 除了中止当前 session request，还倒序扫描历史消息，找到任意带 task ref 的消息并调用 cancel。这可能取消早已与当前 turn 无关的任务。

**修复方案**

- Stop 只取消当前 operation ID。
- 不从历史消息推断当前运行对象。
- 当前 operation 不存在时，Stop 只中止当前网络流，不触碰历史 task。

### 9.4 共享 Engine project 状态交叉污染

并发 session ask 会临时 `SetProject`，然后 defer 恢复。A/B 交错时，任一请求都可能读取另一个请求的 project/workDir，恢复顺序也不可靠。

项目与工作目录必须成为 request-scoped 参数或派生 runner 状态，不得继续作为共享 engine 的 ambient mutable state。

### 9.5 SSE 重连不能依赖短暂轮询和 Wake Lock

Wake Lock/Web Audio 只能改善体验，不提供正确性。SSE 事件应携带 operation ID 和递增 event ID；客户端断线后从 last event 重连或查询 operation 状态，而不是固定轮询约 12 秒后宣告失败。

## 10. 节点身份与 Outbox

### 10.1 后缀猜测会把稳定名称误判为 ephemeral

`EphemeralBase` 把任何以 `-` 加 8 位十六进制字符结尾的 ID 视为临时实例。例如合法稳定名称 `builder-deadbeef` 会被拆成 `builder`。`SameRuntimeIdentity` 因此可能把两个不同稳定身份视为同 runtime，削弱审批或 resume ownership 检查。

**最小安全修复**

- 配置校验拒绝新的稳定 node name/id 使用歧义后缀，并给出迁移提示。
- ownership/approval 默认要求 exact participant。
- 只有持久记录同时保存、认证并验证 stable ID 时，才允许 restart continuity。
- 添加大小写 hex、真实 ephemeral sibling、稳定名称碰撞和跨节点 ownership 的表驱动测试。

### 10.2 Outbox 跨重启投递不能靠字符串补丁解决

`result_outbox` 当前以 `(peer ephemeral ID, task_id)` 存储和查询。请求方重启后得到新 ephemeral ID，旧结果仍指向旧实例，无法自动领取。

这是**协议迁移待办**，不能声称由后缀校验修复。正确方案需要：

1. 显式 `stable_node_id`。
2. 显式 `instance_id`。
3. 显式 `operation_id/requester_id`。
4. 握手认证把 stable 与 instance 绑定到同一凭证。
5. Outbox 使用 durable destination/operation key。
6. 新实例只能领取同一已认证 stable node 的结果。
7. 版本协商、滚动升级和数据库迁移测试。

## 11. 实施批次

### Batch 0：保护现有工作并建立基线

- 编辑前读取当前 status/diff，保留所有既有未提交修改。
- 不 reset、不覆盖、不自动提交或推送。
- 先用回归测试锁定期望语义，而不是保留错误坐标。

### Batch 1：TUI 安全与流隔离

1. 默认关闭鼠标捕获。
2. 精确 hitbox 或统一 layout。
3. 为全部流事件加 source identity。
4. 修复中断/草稿状态机。
5. Asking 支持回看且不抢滚动。
6. Steering 入队失败可见。
7. 窄窗口严格限制宽度。
8. 降低长输出重复复制与重排。
9. 提供退出后的会话可见性。

### Batch 2：命令执行生命周期

- 移除全局 stdout/stderr 替换。
- 使用 context、generation 和显式 writer。
- modeExec 可见、可取消。
- `/tasks watch` 使用专门事件泵。

### Batch 3：审批与取消正确性

- 统一 typed approval disposition。
- 接受已有工作默认成功。
- 确定性失败要求修改输入。
- 普通远程取消使用 `CancelTree` 终结 origin。
- forward/lease/error 使用统一 terminalize 路径。

### Batch 4：Web 长操作与竞态

- Approval 返回 typed outcome/operation。
- 后台执行脱离 HTTP request context。
- active ask 使用 generation compare-and-delete。
- Stop 只处理当前 operation。
- project/workDir 请求作用域化。
- SSE 支持按 operation/event 重连。

### Batch 5：身份最小加固

- 拒绝歧义稳定 ID。
- 授权默认 exact identity。
- 明确协议迁移边界。

### Batch 6：独立协议迁移

- stable node、instance、operation 三类身份显式化。
- 迁移 outbox schema 与握手认证。
- 做兼容、滚动升级、重启恢复和未授权领取失败测试。
- 此批次必须单独审查与发布；未完成前只能标记“待办”。

## 12. 回归测试矩阵

### TUI 单元与 PTY

- 默认启动不启用鼠标捕获协议。
- 双击、移动、拖选不取消。
- 精确 Stop/审批坐标；边框和空白 no-op。
- 草稿下 Esc/Ctrl+C 不误退出。
- Asking PageUp/PageDown 与新 delta 锚定行为。
- A 取消后 B 启动，A 的所有迟到事件均不能修改 B。
- resize 到 1、5、10、40 列，无 panic 且每行不越界。
- 长输出保持正确且资源增长受控。
- 长 shell 与 `/tasks watch` 可见、可取消、无串线。

### Core 与 AskEngine

- 空/旧 `ResultJSON` 接受后 `OK=true`。
- authorization refusal 可 resume；context overflow 不原样 resume。
- 远程 submit 取消后 downstream 与 origin 都为终态。
- 续租中的健康任务不被固定超时误杀。
- 取消失败时队列记录不被错误删除。

### Web

- A/B 并发：A 的 unregister/finally 不影响 B。
- Stop 只取消 B 的 operation，不取消历史 task。
- 浏览器断线或 60 秒后后台审批仍继续。
- done/review/failed/cancelled/running 显示准确。
- SSE 能按 operation/event 恢复。

### Identity 与协议

- `builder-deadbeef` 等歧义名称被明确拒绝。
- stable 与 ephemeral sibling 判断不再用于宽泛授权。
- 协议迁移后：请求方重启可领取自己的旧结果，其他节点不能领取。

## 13. 验证命令

每批先运行最窄测试；全部实现后运行：

```bash
go test ./cmd/panda ./internal/askengine ./internal/core ./internal/scheduler ./webui/panel
go test -race ./cmd/panda ./internal/core ./webui/panel
make race-focused
make web-test
make web
make fmt-check
make vet
make build
git diff --check
make gate-all
```

真实 TUI 必须冷启动实际二进制并通过 PTY 驱动，不能用单元测试代替交互验证。若环境限制导致某项未运行，报告必须写明原因和替代验证，不能记录为通过。

## 14. 当前工作树与实施纪律

调查时仓库已经存在大量未提交修改，覆盖 TUI、AskEngine、Core、Scheduler 与 Web panel；另有未跟踪文档。这些内容不应被 reset、覆盖或误认为本次文档创建产生的改动。

后续实施必须遵守：

- 不再启动开放式 code review。
- 只解决本文已确认问题及测试直接暴露的阻断性回归。
- 每个文件编辑前读取当前版本并做合并式修改。
- 不自动 commit 或 push。
- 测试失败必须修复或如实记录，不能放宽断言以保留原错误行为。
- 既有未提交改动只有在完整验证后才能标记“已修复”。
- 跨重启 outbox 在协议迁移完成前始终标记“待办”。

## 15. 状态清单

本文档创建时的真实状态如下：

| 项目 | 状态 |
|---|---|
| 真实 PTY 复现鼠标/双击/选择问题 | 已完成 |
| 根因与修复方案整理 | 已完成 |
| 仓库内集中报告 | 已完成（本文） |
| 默认关闭鼠标捕获 | 已实施（2026-09-16，见 §3.1：默认交还终端，并补 1007/↑↓/PgUp-PgDn 滚动通路与 ctrl+t 切换） |
| 终端旁路键（`⌥`/`Fn`）与"非二选一"结论成文 | 已实施（2026-09-16，见 §3.1 追加段） |
| 1007 跟随 ctrl+t 双向切换 | 已实施（2026-09-16，`altScrollCmd`） |
| exec 期间滚轮可用 | 已实施（2026-09-16，滚轮分支提到模式闸门之前） |
| 精确 TUI hitbox | 未实施 |
| 全流事件 source identity | 未实施 |
| 命令可取消与 writer 化 | 未实施 |
| AskEngine 接受结果默认成功 | 未实施 |
| 确定性失败审批分类 | 未实施 |
| 普通远程取消终结 origin | 未实施 |
| Web generation-safe registry | 未实施 |
| Stop 不再取消历史任务 | 未实施 |
| 节点 identity 最小加固 | 未实施 |
| Outbox 跨重启协议迁移 | 待独立设计与实施 |

## 16. 此前验证记录

在源码修复开始前，已有以下基线结果：

- `make build`：通过。
- `go test ./cmd/panda ./internal/askengine ./internal/core ./internal/scheduler ./webui/panel`：通过。
- `make web-test`：通过，TypeScript 检查通过，61 个 Node 测试通过。
- `git diff --check`：通过。

这些结果只表示原工作树通过现有门禁，不代表本文问题不存在。现有测试缺少 PTY 鼠标协议、旧流串线、HTTP 生命周期和并发 generation 等关键场景，部分测试还固定了错误 hitbox 假设。
