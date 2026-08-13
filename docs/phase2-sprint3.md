# Phase 2 · Sprint 2.3/2.4 完成报告 · PANDA

> 阶段：Phase 2 · 防御链 / 权限模型 / GPIO 权限 / 调度评分
> 日期：2026-08-13
> 状态：✅ 完成 · Phase 2 全部 Sprint 收尾

## 1. 交付物概览

本阶段补齐 Phase 2 的最后两块 MVP 基线（防御链 + 权限），并实现调度评分的后续增强基础（优先级评分 + 容量并行）。至此 Phase 2 的计划全部落地。

| 交付物 | 产出 | 状态 |
|---|---|---|
| 权限判定引擎 | `internal/defense/permission.go`：Tier 1/2 分类 + Authorize + 危险命令推断 | ✅ |
| 能力卡 tier 声明 | `NativeAbility.Tier` 字段，commander 执行前判定 | ✅ |
| 授权传递链路 | `TaskInput.Authorized` → wire payload → execute/run → Router.Execute；`ask --authorize` | ✅ |
| GPIO 权限接入 | udev 规则（gpiochip 组可读）+ 能力卡去 sudo（`gpioinfo` tier 1）| ✅ |
| 优先级评分 | `internal/scheduler/priority.go`：加权评分 + 防饥饿 | ✅ |
| 容量并行 | `internal/scheduler/capacity.go`：容量账户 + TryAcquire/Release | ✅ |

## 2. 权限模型（设计 §16）

实现「可挽回自动批，不可挽回必须问人」的确定性基础（不做对抗性双模型自审，那是后续增强）：

- **Tier 1**（可挽回）：默认，自动执行。
- **Tier 2**（不可挽回）：执行前必须携带授权标记，否则拒绝（`ErrNotAuthorized`）。

能力卡 native 命令可用 `tier: 2` 显式声明；未声明时按命令首词推断——`sudo/su/rm/dd/mkfs/shutdown/reboot/systemctl` 等危险/提权动词默认 Tier 2（防御性兜底）。

授权沿委派链传递：本地提交走 `TaskInput.Authorized`，跨节点走 `task_delegate.authorized`，上下文拉取恢复路径也保留该标记。`panda ask --authorize` 显式授权 tier-2 命令。

## 3. GPIO 权限（Sprint 2.4）

此前 `gpio:read` 用 `sudo gpioinfo`（因为 `/dev/gpiochip*` 需 root）。本次从根源解决：

- 香橙派加 udev 规则 `KERNEL=="gpiochip*", SUBSYSTEM=="gpio", MODE="0664", GROUP="gpio"`，并把 `xenith` 加入 `gpio` 组。
- 能力卡 `gpio:read` 改回纯 `gpioinfo`（tier 1，无提权）。

真实硬件验证：`panda ask "读取香橙派的 GPIO 引脚状态"` → 跨设备执行 `gpioinfo` 并回传 6 个 gpiochip 状态，全程无 sudo。

## 4. 优先级评分（P2-06，设计 §6.3）

`Priority` 纯函数实现加权评分（越高越优先）：

```
0.3*user_priority + 0.2*scheduler_tier + 0.1*wait_time + 0.4*resource_efficiency
```

- `user_priority`：0-10，缺省 5。
- `scheduler_tier`：根=10 / 子调度器=5 / worker=1（与 `main.go` 映射一致）。
- `wait_time`：正贡献实现防饥饿。
- `resource_efficiency`：调用方传入（0-1，任务对节点资源的匹配度）。

**一处偏离设计文档**：文档伪代码写 `w3 = -wait_time`，但注释是「等待越久越优先」——负号会饿死长等待任务而非救助。实现按语义取正，代码注释已说明。

## 5. 容量并行（P2-05，设计 §6.3）

`Account` 实现容量驱动的并行接受判定：任务在剩余容量（CPU/RAM/GPUVRAM/并发数）覆盖其需求时才接受，否则返回 false 由调用方排队。零值字段表示该维度不设限。这是「强节点同时跑多个任务」的确定性核心，替代 idle/busy 二进制开关。

## 6. 明确不做（设计「后续增强」）

- **P2-04 任务 transfer**：设计 §6.3 明示「MVP 不自动转移，只记录 queued 状态」。本阶段提供排队所需的基础（容量拒绝 + 评分），自动转移留后续。
- 对抗性双模型自审（§16.2）、模型档位调度（§13）、合并门禁（§15.3）：均属后续增强。

## 7. 测试统计

| 包 | 覆盖率 | 关键新增测试 |
|---|---|---|
| `internal/defense` | 新增 | `TestAuthorize`（tier 门控）/ `TestTierFromCommand`（危险命令推断）|
| `internal/commander` | ↑ | `TestExecuteTier2RequiresAuth`（未授权拒绝，命令不执行）/ `TestRouteTierFromCommand` |
| `internal/scheduler` | ↑ | `TestPriority`（权重 + 防饥饿 + 缺省）/ `TestAccountAcquireRelease` / `TestAccountMaxConcurrent` / `TestAccountUnboundedDimension` |
| `go vet` / `gofmt -l` | ✅ 无告警 / 干净 | |

## 8. 改动文件清单

新增：
- `internal/defense/permission.go` + `permission_test.go`
- `internal/scheduler/priority.go` + `priority_test.go`
- `internal/scheduler/capacity.go` + `capacity_test.go`

修改：
- `internal/ledger/capability.go`：`NativeAbility.Tier`
- `internal/commander/commander.go`：`Plan.Tier` + `Route` 填 tier + `Execute` 授权判定
- `internal/bus/payloads.go`：`TaskDelegatePayload.Authorized`
- `internal/core/submit.go`：`TaskInput.Authorized` + forward 携带
- `internal/core/handlers.go`：`execute`/`run` 传递 authorized
- `internal/core/context.go`：`pendingContext.Authorized` + 恢复传递
- `cmd/panda/ask.go`：`--authorize` flag
- `config/capabilities.orangepi3b.yaml`：`gpio:read` 去 sudo（`gpioinfo` tier 1）
- `scripts/deploy-opi.sh`：scp 前停 systemd 服务（释放运行中二进制）

## 9. 香橙派环境变更

- udev 规则 `/etc/udev/rules.d/99-gpio.rules`（gpiochip 0664 + gpio 组）
- `xenith` 加入 `gpio` 组

---

*Phase 2 完成 · 2026-08-13 · 下一步：Phase 3（记忆系统 / Skill 自进化 / 桌宠硬件）*
