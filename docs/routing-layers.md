# 任务路由的三层契约

一次任务派发要回答三个互不相同的问题：在哪台机器上跑、用哪个执行器跑、由谁规划和汇总。这三层由不同字段、不同代码路径决定，混用会造成「任务明明派给了 xin，却在本机 opencode 里跑起来」。本文是这三层的行为约定，也是相关代码的注释基准。

## 1. 三层分别决定什么

| 层 | 回答的问题 | 载体 | 决策位置 | 失败时的行为 |
| --- | --- | --- | --- | --- |
| 设备层 | 在哪台机器上执行 | `spec_json.node`（硬 pin）、`spec_json.preferred`（软提示）、两者皆空（自由路由） | `core.routePinned` / `scheduler.RouteP` | 硬 pin 不回退本机；软提示与自由路由可回退，但必须留痕 |
| 执行器层 | 用哪个适配器执行 | `requires`：`agent:<name>`、`hardware:*`、native 能力 ID | 目标设备上的 `commander.Router` | 目标设备不具备该能力时在准入阶段 decline |
| 协调器层 | 谁分类、拆解、汇总结果 | `config.model` 的入口 LLM HTTP endpoint | `internal/entry` + `internal/askengine` | 与设备选择无关，永不作为任务执行器 |

`requires` 只表达「目标设备需要具备什么」，不表达设备。 `agent:opencode` 在每台装了 opencode 的机器上都成立，`Node.Matches` 只检查该节点有没有这个名字的 agent。设备意图只能通过 `node` / `preferred` 表达，这是 `internal/entry/prompt.go` 对模型输出的硬要求。

## 2. 两个都叫 opencode 的东西

- **协调器的模型供应商**：`config.model.provider: opencode-go`、`base_url: https://opencode.ai/zen/go/v1`。入口 LLM 由 `internal/entry/stream.go` 直接发 HTTP 请求，负责分类、生成 TaskSpec、观察子任务结果并收敛回复。它不启动 opencode CLI，也不参与设备选择。
- **执行器的适配器**：`requires: ["agent:opencode"]`。目标设备上的 `commander.Router` 把它解析为 `adapters/opencode.py`，由该 Python 适配器起 `opencode run` 子进程。

两者名字相同但不是同一个组件。协调器只在接收用户请求的那台节点上运行，执行器只在设备层选中的那台节点上运行。任务停在 `dispatched` 时，本机看起来「卡在 opencode」，通常是协调器在等远端结果，而不是本机执行器在跑；判据是 `task_events` 里有没有 `accept` / `progress`，以及 `task_outbox` 里有没有托管行。

## 3. 一次请求的链路

```
用户请求
  → 协调器（入口 LLM）：分类，产出 TaskSpec（设备 / 能力 / 约束）
  → core.Enqueue：落库，queued
  → 队列认领（ClaimLocal）：dispatched，事件 delegate by=queue target=本机
      这一条只是本机持有租约，不是派发决定
  → forwardScheduled：设备层决策
  → sendClaimedDelegate：事件 delegate by=queue-forward target=<对端>
  → 对端 handleLocalDelegate → 本地 commander.Router → 适配器子进程
  → task_result 回传，本机 handleResult 收敛，协调器汇报
```

`delegate by=queue` 的 target 恒为本机，读时间线时不能把它当成「任务被判给本机」。`delegation_hop via=retarget` 之后才是真正的目标。

## 4. 设备层规则

**硬 pin（`spec.node` 非空）**。`routePinned` 不评分：名字解析为本机则本机执行；解析为配置中的 peer 或拓扑邻居则转发；名字歧义、不在目录里、已在委派链中、或该节点不满足能力与资源声明时，任务以终态失败结束，不降级到「谁分数高谁跑」。链路不通时，`sendClaimedDelegate` 把 delegate 托管进 `task_outbox`（`transport_type=pin`），清租约，等该节点的下一个 hello 或 5 秒周期的 sweep 投递；TTL 由任务 deadline 决定，没有 deadline 时用 `defaultDTNTTL`。`runScheduled` 还有一道 fail-closed 守卫：硬 pin 任务若走到本机 `c.run` 之前，直接失败并写明目标设备，绝不启动本机适配器。

**软提示（`spec.preferred` 非空）**。`RouteP` 先按 id 或显示名在「在线且具备能力」的候选里找该设备；命中则转发。设备离线或不具备能力时落到评分，`localBias`（0.15）让空闲本机有优势，因此软提示允许回到本机执行——这是软提示的定义。一旦转发已经指向另一台设备而发送失败，`parkable` 生效，任务托管进 `task_outbox`（`transport_type=prefer`）等链路恢复；回退到本机时写 `route_fallback` 事件并打 WARN 日志，说明原本指向哪台设备。纯自由路由的转发失败仍保留 corrective 本机回退，同样写 `route_fallback`。

**自由路由（两者皆空）**。由 `RouteP` 按能力过滤、硬件适配、负载与项目亲和度评分。`localBias` 有意让空闲本机胜出，避免单任务在空网络上无谓跨机。此处的本机执行是调度结果，不是回退。

**能力卡与设备无关**。`capabilities.yaml` 的 `agents.opencode` 经 `ledger.Register` 写入 `employee_cache`，`Node.Abilities()` 生成 `agent:opencode` 并随 hello 扩散。它让一台机器成为候选，不给它任何优先权，也不把它变成默认目的地。

## 5. 各入口如何写设备字段

| 入口 | 设备字段 | 语义 |
| --- | --- | --- |
| `task_submit` 工具的 `node` 参数 | `TaskSpecDetail.Node` → `spec_json.node` | 硬 pin；用户点名设备时必填 |
| Web 面板的设备选择器 | `createTaskRequest.Node` → `core.TaskInput.TargetNode` | 硬 pin |
| 计划（plan）的 stage `node` | `core.TaskInput.TargetNode` | 硬 pin |
| `panda task add --preferred` | `core.TaskInput.PreferredNode` → `spec_json.preferred` | 软提示；设备离线或能力不满足时允许回退本机并留 `route_fallback` |
| `panda task add --nodes a,b` | 每个副本的 `core.TaskInput.TargetNode` | 硬 pin；`routePinned` 不评分，不通则托管或终态失败 |
| `spec.preferred` / `p.PreferredNode` 沿线透传 | 每一跳保留 | 软提示 |

面板曾经把设备选择器写进 `PreferredNode`。被选设备离线时 `RouteP` 找不到它，评分回到本机并启动本机 agent CLI，而界面仍显示那台设备为目标——这是软/硬语义混用的典型后果。设备选择器必须是 `TargetNode`。

## 6. 链路排障

两端启动日志里的 `secret_fp` 是 `network.shared_secret` 的短指纹，用于比对而不泄露密钥。指纹不同，hello 会被双方同时拒绝：一边是 `peer rejected our hello reason=auth`，另一边是 `rejected hello: bad signature`，而 TCP 连接始终是 ESTABLISHED。此时日志的 `reason` 字段会区分 `shared_secret mismatch`、`clock skew`（超过 ±5 分钟）、`identity signature` 和 `hello bound for a different receiver`。

恢复方式是在对端执行 `panda pair --secret <从本机 config.yaml 抄出的值> --peer <本机地址:7836>`，或用交互式 `panda pair <设备>` + 对端 `panda pair confirm` 重新交换密钥。配置里 `network.shared_secret` 为空时，`cmd/panda/netsetup.go` 会在内存中临时生成一个而不写盘，重启后又换一个，两端就会永久互相拒绝；`ApplyNetworkConfig` 因此拒绝用空值覆盖一个正在使用的密钥。

`network.exposure_mode` 默认 `loopback`：监听压在 `127.0.0.1:7836`，发现与自动拨号都关闭。局域网组网要写 `lan`（或显式 `listen_addr`），否则即使配了 `network.peers`，也只能靠外拨，且对端无法反向接入。

**先确认地址，再怀疑密钥**。`network.peers` 存的是静态 IP，设备换 Wi-Fi、切热点或重拨号后网段会变，旧地址只剩拨号超时。判据是本机 `ipconfig getifaddr en0` 与 peer 地址是否同网段、`arp -n <peer>` 有没有条目、`nc -z -G 3 <host> 7836` 通不通；`panda nodes` 里节点仍显示 `offline` 但 `last_seen` 不再更新，配合日志里成片的 `peer dial failed … i/o timeout`，就是地址失效而非认证失败。此时更新 `network.peers` 为对端新地址即可，配置由 daemon 与 web 控制台各自的文件轮询在一秒内生效，无需重启。

**单进程与双进程**。`panda web` 独立运行时是节点行的持有者，自己监听 7836、跑 keepalive 与 outbox sweep；旁边有 daemon 时它是 borrowed node，`applyNetworkConfig` 的 `OwnsNodeRow` 门禁会阻止它启动 peer keepalive，避免抢走 daemon 唯一的那条边、把任务永久托管在 `task_outbox`。两种形态都跑同一个 `runNetworkSync` 文件轮询，只是前者通过 `EngineHolder.MutateConfig` 落到 engine 的 core，后者直接落到 daemon 的 core。

**托管行与任务状态要一起看**。任务停在 `dispatched` 且 `task_outbox` 有行，说明设备层已选定目标、卡在投递；任务已经 `cancelled`/`failed` 而 outbox 还留着行，是取消后未清的孤儿，会在 TTL 内反复重试投递一个已终态的任务。清理时 `task_outbox` 与 `cancel_outbox` 要成对删除，否则对端恢复后会收到一个没有对应 cancel 的 delegate。
