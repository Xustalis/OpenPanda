# 项目现状

> 记录时间：2026-08-26（`main`，v0.0.5 收尾 + v0.0.6 跨设备算力调度落地后）。
> 这份文档回答一个问题：**现在做到哪一步了，哪些能用、哪些还不能。**

## 一句话结论

单机与可信内网（当前三台：Orange Pi / MacBook / Windows）**可以按设计使用**：
一句话进来会被分成「本地回答 / 派一个任务 / 拆成跨机器的多阶段计划」，
不可逆操作停在待审批队列等人批。

但**旗舰场景还没在真机上端到端跑过一次**，并且信任模型有一处已知缺口
（见「已知限制」P2-8）。所以：可用，但不能称为已验证。

## 能力对账

| 设计目标 | 状态 | 落地位置 |
|---|---|---|
| 语音发布任务（桌宠入口） | 🟡 已接通，真机未验 | `cmd/panda/voice.go`（`panda voice`）+ `extensions/voice/*.py` |
| 简单任务本地答、复杂任务外派 | ✅ | `internal/entry` 四分类：`answer` / `tool_call` / `task` / `plan` |
| 一次多任务负载均衡到多台并行 | ✅ | `internal/scheduler/score.go`（空闲率 0.4 + 优先级 0.3 + 节点等级 0.2 + 排队深度 0.1，再乘心跳新鲜度衰减，半衰期 30 分钟） |
| 多适配器各自独立调度 | ✅ | `internal/agents/registry.go` 注册 7 个 adapter；`requires: ["agent:codex"]` 是硬过滤条件 |
| 按硬件路由（显存任务不落到派上） | ✅ | `resource_profile` → `ledger.Fits`；未声明硬件的旧卡片按「沉默 ≠ 0」放行（`Declared()`） |
| 跨机器多阶段流水线 | ✅ | `internal/plan` + `internal/core/plan.go`；阶段产物经 `internal/artifact` 分块搬运 |
| 完成后上报、队列告知完成 | ✅ | `panda queue` / `panda task <id>` / `panda plan show <id>` |
| 不可逆操作进待审批 | ✅ | tier 判定 `internal/defense/permission.go`（含 Windows 动词/解释器表）→ `review` 状态 → `panda approve` / `reject` |
| 统领再往下分给 claude code / codex | ✅ | `internal/commander` 三层：`native` / `agent` / `manual` |
| 去中心化、每个节点都能是根 | ✅ | 逐边路由 + `AppendChain` / `Predecessor` 防环，无主根 |
| 引脚驱动舵机（香橙派） | ❌ 未实现 | `gpio` 目前只是能力卡上的字符串：能把阶段路由到派，但没有执行通路 |
| 最短路径多跳中继 | 🟡 只有贪心 | 逐跳打分 + 跳数惩罚（`internal/scheduler/route.go`）；**无链路时延度量、无拓扑传播、无图最短路** |

## 两条入口都可达

跨机器流水线有两个入口，都通向同一个 `core.StartPlan`：

```bash
# 一、写成文件的流水线（每周要跑的那种，可读可 diff）
panda plan example > train.yaml
panda plan run train.yaml --dry-run     # 先看路由结果
panda plan run train.yaml
panda plan show <plan-id>

# 二、一句话（桌宠场景，不需要先写文件）
panda ask "写个图像分类的训练脚本，在有显卡的机器上跑，最后把结论发回来"
panda voice                              # 同一条路径，改成说出来
```

模型什么时候输出 `plan` 而不是 `task`，判断标准只有一条：**是否必须换机器**。
同一台机器上的连续几步仍然是一个 task。

## 阶段的审批语义

`core.StartPlan` **不给任何阶段 tier-2 同意**，`panda voice` 也没有 `--authorize`。
后果是刻意的：流水线里出现不可逆动作时，那个阶段停在 `review` 等人批，
而不是继承「你对整句话的一次授权」。对一条 shell 命令的同意，不等于对一条
三机器流水线的同意；麦克风听到的更不等于。

## 验证到什么程度

| 检查 | 结果 |
|---|---|
| `make gate`（fmt-check + build + vet + test + race） | exit 0 |
| 三设备真机跑通旗舰流水线（派 → mac → win → 派） | ❌ 没跑过 |
| 语音链路真机跑通（唤醒 → 转写 → 派任务 → 朗读） | ❌ 只验了降级路径（缺 driver 时正确报错并退出） |

语音 sidecar 默认后端不需要密钥，但需要本地依赖：
`pip install openwakeword faster-whisper pyaudio numpy`。缺依赖时
`panda voice` 会打印 sidecar 自己的原因并退出 1，不会静默重启 python。

## 已知限制

**P2-8 全网单一共享密钥（最严重）。** 网络共享密钥是唯一凭据，持有它的节点可以
伪造 `Authorized`，也就是自己给自己批准 tier-2。「不可逆操作必须我审批」这条约束
在单机和可信内网成立，一旦网络里有你不完全信任的节点就不成立。修法是 per-node
密钥 / 可验签授权——属于改信任模型，不是打补丁。

**P2-9 审计链可篡改。** `task_events` 的哈希链是无密钥 SHA-256：能写库就能重算整条
链。需要换成 HMAC 或签名，和 P2-8 是同一个密钥分发问题。

**i18n 只做了一半。** 约 32 个文件里仍有硬编码中文（CLI 提示、计划/语音输出）。
`internal/i18n` 支持 5 语言，但这些路径没走它。

**GPIO / 舵机没有执行通路。** 要在派上驱动舵机，目前得自己提供脚本或 adapter。

**多跳只是贪心。** 每一跳取局部最优 + 跳数惩罚，够用于当前三台机器；深空集群设想里
的「按链路时延求全局最短路」需要链路度量与拓扑传播，都还不在。

## 下一步

1. 在三设备真机 lab 上把一句话触发的流水线跑一遍——这一次会同时验证语音入口、
   逐边路由、阶段产物搬运和审批停泊，是目前信息量最大的一次验证。
2. 定 P2-8 的方向（per-node 密钥还是签名授权），P2-9 随之落地。
3. i18n 收尾、GPIO 执行通路、链路时延最短路，按需排期。
