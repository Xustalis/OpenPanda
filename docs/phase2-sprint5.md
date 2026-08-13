# Phase 2 · Sprint 2.5 完成报告 · PANDA

> 阶段：Phase 2 · 多机联调（真实硬件）
> 日期：2026-08-13
> 状态：✅ 完成 · MacBook ↔ 香橙派 3B 跨设备委派闭环跑通

## 1. 交付物概览

Sprint 2.1/2.2 的委派与上下文代码此前只在 loopback 上用真实 WebSocket 验证。本次把真实香橙派 3B（RK3566, 2GB, Armbian/Debian 13）接入，跑通了从 Mac 发起、香橙派执行的完整跨设备闭环，并修复了 4 个多机才暴露的 bug。

| 交付物 | 产出 | 状态 |
|---|---|---|
| 香橙派部署 | 交叉编译 aarch64 二进制 + 能力卡 + 配置 + 进程启动 | ✅ |
| 双节点互见 | Mac daemon ↔ 香橙派 daemon 双向 hello + 能力卡交换落库 | ✅ |
| 跨设备委派闭环 | `panda ask` → 香橙派执行 `sys:info`(uname) → 结果回传 | ✅ |
| GPIO 硬件能力 | `gpio:read`(`sudo gpioinfo`) 跨设备执行 + 大结果回传 | ✅ |
| 代码 bug 修复 ×4 | 连接风暴 / err 作用域 / greeted 泄漏 / prompt 边界 | ✅ |
| 环境配置 | ufw 放行 7836、能力卡 gpio 命令修正 | ✅ |

## 2. 部署步骤

```bash
# 香橙派（aarch64, 无需 Go 运行时）
scp bin/panda-linux-arm64 orangepi:~/panda/panda
scp config/capabilities.orangepi3b.yaml orangepi:~/panda/capabilities.yaml
scp testdata/deploy-opi.yaml orangepi:~/panda/config.yaml
ssh orangepi 'setsid ~/panda/panda --config config.yaml --card capabilities.yaml >> daemon.log 2>&1 </dev/null &'

# Mac（发起端）
./bin/panda ask --config config.yaml --card config/capabilities.macbook.yaml "获取香橙派的系统信息"
```

香橙派节点配置见 [testdata/deploy-opi.yaml](../testdata/deploy-opi.yaml)：`Micro` 资源类，监听 `:7836`，peers 指向 Mac `192.168.0.100:7836`。

## 3. 验证结果

| 验证项 | 结果 | 证据 |
|---|---|---|
| 双向 hello 握手 + 能力卡交换 | ✅ | Mac daemon 日志 `peer registered orangepi3b`；香橙派日志 `peer registered macbook-m1` + `connected to peer` |
| 跨设备委派（sys:info） | ✅ | `panda ask "获取香橙派的系统信息"` → `task … done` + `Linux orangepi3b 6.1.115-vendor-rk35xx … aarch64` |
| GPIO 硬件能力（gpio:read） | ✅ | `panda ask "读取香橙派的 GPIO 引脚状态"` → 回传 6 个 gpiochip 完整引脚状态 |
| 大结果回传 | ✅ | gpioinfo 输出（~190 行）完整沿链回传，无截断 |
| 连接风暴修复 | ✅ | 修复后连接成功仅一次 `connected to peer`，失败按 30s 退避 |

`aarch64 GNU/Linux` 的输出证明命令确实在香橙派上执行（Mac 为 Darwin/arm64）。

## 4. 发现的 bug 与修复

### 4.1 连接风暴（`cmd/panda/main.go` + `internal/core/core.go`）

daemon 的 peer dial 循环把异步的 `DialPeer`（`go handleInbound`）当作阻塞调用，`DialPeer` 立即返回 nil 后循环 1 秒重连一次，造成每秒新建一个 WS 连接的连接风暴。

**修复**：`core.go` 提取私有 `dial`（建立连接 + 发 hello），新增 `MaintainPeer`（同步 `handleInbound`，阻塞直到连接断开）；`main.go` 循环改用 `MaintainPeer`——dial 失败指数退避，连接建立后断开则短退避重连。`DialPeer` 保留异步语义给短命调度器（ask）用。

### 4.2 err 作用域（`cmd/panda/main.go`）

`logger.Warn("peer dial failed", "err", err)` 的 `err` 引用的是闭包捕获的外层 `runDaemon` 的 `err`（恒为 nil），而非 `DialPeer` 返回值，导致日志 `err` 恒为 `null`，真实拨号错误被吞掉。

**修复**：把 `err := coreNode.MaintainPeer(ctx, p)` 提为循环体局部变量。

### 4.3 greeted 状态泄漏（`internal/core/core.go`）

`greeted` 标记「已回 hello 给某节点」以终止握手 ping-pong，但 peer 断开时从不清理。同一 node ID 重连时（Mac daemon 停后，`panda ask` 复用 `macbook-m1` 身份拨入），对方因 `greeted` 已置位而跳过 hello 回复，导致发起端收不到回复、无法把对方注册进 `peers`，委派时报 `peer not connected`。

**修复**：`removePeerForConn` 删除 peer 时一并 `delete(c.greeted, id)`，让重连重新完成握手。

### 4.4 entry prompt 边界不清（`internal/entry/prompt.go`）

系统 prompt 未区分「设备能力」与「受控工具」，模型把 native 能力 `sys:info` 误判为 `tool_call`（`{"tool":"sys:info"}`）而非 `task.requires`，导致 `ask` 不进入执行管线。

**修复**：prompt 明确「设备列表里的 native/agent 能力只能走 task；tool_call 仅用于设备能力之外的受控工具（天气/提醒等）」。

## 5. 环境配置（非代码）

| 问题 | 处理 |
|---|---|
| 香橙派 ufw 默认 DROP，仅放行 22，7836 被拦 | `sudo ufw allow 7836/tcp` |
| 香橙派只有 libgpiod（`gpioinfo`/`gpioget`），无 wiringPi 的 `gpio` 命令 | 能力卡 `gpio:read` 由 `gpio readall` 改为 `sudo gpioinfo` |
| `/dev/gpiochip*` 需 root 权限 | 用 `sudo` 调用（已确认 NOPASSWD） |

## 6. 改动文件清单

新增：
- `testdata/deploy-opi.yaml`（香橙派真实部署配置）

修改：
- `cmd/panda/main.go`：dial 循环改用 `MaintainPeer` + 修复 err 作用域
- `internal/core/core.go`：`DialPeer` 重构（提取 `dial`）+ 新增 `MaintainPeer` + `greeted` 断开清理
- `cmd/panda/ask.go`：P2P 连接提前到 `Classify` 之前（让入口模型看到远程能力）+ `runAskTask` 简化为只 Submit
- `internal/entry/prompt.go`：native 能力 vs tool_call 边界
- `config/capabilities.orangepi3b.yaml`：`gpio:read` 命令修正为 `sudo gpioinfo`

## 7. 遗留问题（后续 Sprint）

- **ask 与 daemon 的 node ID 冲突**：两者都用 `cfg.Node.Name`（`macbook-m1`）。若 daemon 常驻，`ask` 拨入会被对方 `ensurePeer` 忽略、回包发错连接。本次以「停 daemon、仅用 ask 作短命调度器」规避；长期需区分短命调度器与常驻节点的身份（如 ask 用独立临时 node ID，或 daemon 提供本地 IPC 提交入口）。
- **香橙派 daemon 未接 systemd**：当前 `setsid` 手动启动，重启不自动拉起；正式部署应加 unit 文件。
- **上下文分级传输（Sprint 2.2）未做真实多机验证**：pointer/full 打包 + fetch/ack 恢复路径仍在 loopback 验证，需在真实两节点上补测。
- **GPIO 权限模型未接入**（Sprint 2.4）：`gpio:read` 用 `sudo` 直接执行，尚未纳入防御链/权限判定（设计 §2.3/§2.4）。

---

*Sprint 2.5 完成 · 2026-08-13 · 下一步：2.3 防御链/权限（熔断器/权限判定引擎）或 2.4 GPIO 权限接入*
