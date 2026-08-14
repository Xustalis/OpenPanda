# PANDA · 开发工作手册

> 面向持续开发者的实操文档。区别于 [phase0-report.md](./phase0-report.md)、[phase1-report.md](./phase1-report.md)（阶段验收记录）和设计文档（架构权威）。
> 更新日期：2026-08-13 · 适用阶段：Phase 1 完成，准备 Phase 2

---

## 1. 快速开始

```bash
# 前置：Go 1.22+（本机 1.26.5）、Python 3.10+、make
make build           # 编译 bin/panda（release，strip 符号）
make test            # 全部测试
make vet             # go vet
make run             # 用 config.example.yaml 启动
make measure         # 实测稳态 RSS（多次采样，单次不可靠）
```

配置加载优先级：`--config` 路径 > 环境变量 > 默认 `/etc/panda/config.yaml`。
本机开发用 `testdata/mac-config.yaml`（loopback 单机）或 `testdata/opi-config.yaml`（模拟香橙派）。

## 2. 目录地图

```
cmd/panda/          守护进程入口（注册/心跳/监控/WS server/peer 重连）
                    + 子命令：ask（统一入口模型）+ status/queue/task/cancel/logs（面板）
internal/
  core/             核心：节点生命周期(node.go) + 状态机(state.go/tasks.go)
                    + 消息路由(core.go) + 处理器(handlers.go)
                    + 本地执行管线(submit.go：SubmitLocal + TaskDetail + SetDetail)
  entry/            统一入口模型（Phase 1）：prompt.go + model.go + router.go
                    + spec.go 校验 + classify.go + fallback.go 降级
  bus/              WebSocket 传输 + 消息信封(msg.go/payloads.go/ws.go)
  commander/        三层能力执行：native(exec) / agent(adapter) / manual
  scheduler/        委派路由决策（chain + route）
  defense/          权限 Tier 门禁 + 熔断器（circuit）+ 范围漂移 + 循环检测
  security/         执行侧加固：沙箱 / 网络白名单 / 密钥脱敏 / 审计日志
  panel/            PWA 控制台 HTTP API（队列 / 详情 / 审批）
  ledger/           能力目录（capabilities.yaml 解析 + employee_cache CRUD）
  ctxstore/         上下文快照 LRU
  memory/           双层记忆（USER/MEMORY 双文件 + 项目记忆 + 隔离墙）
                    + memory 工具 + daily 日志 + Dreaming 引擎
  skills/           程序性记忆（SKILL.md 自进化：触发/作用域/渐进加载/生命周期）
  config/           YAML 配置加载（含 model 段）
  storage/          SQLite(WAL) 封装 + 迁移
  log/              slog JSON 日志
  util/             UUIDv7
adapters/           Agent 适配器（claude_code.py + opencode.py）
extensions/voice/   语音 sidecar（wake/stt/tts/vad，待硬件实测）
web/pwa/            PWA 前端（manifest + sw + 队列/详情/审批面板）
config/             示例能力卡（macbook / orangepi3b）
testdata/           双节点 loopback 测试配置
docs/               阶段报告 + 本手册
```

## 3. 消息协议速查（Phase 0）

信封格式（`internal/bus/msg.go`）：
```json
{"v":1,"type":"task_delegate","msg_id":"<uuid7>","from":"mac","to":"opi",
 "ts":1723456789,"payload":{...}}
```

| 消息 | 方向 | 语义 |
|---|---|---|
| `hello` | 节点→节点 | 双向握手（只回一次，防 ping-pong）|
| `task_delegate` | 委派方→执行方 | 创建本地任务并执行 |
| `task_accept` | 执行方→委派方 | 父节点更新 dispatched→running + owner |
| `task_decline` | 执行方→委派方 | 任务回 queued，带原因 |
| `task_result` | 执行方→委派方 | 完成/失败，**必须带 attempt_id** |
| `task_cancel` | 委派方→执行方 | 级联取消 |

关键不变量（设计 §5.3.1）：
- `task_id` 是跨节点幂等键（委派方指定，执行方保留）
- `attempt_id` 每次执行唯一；旧 attempt 结果被拒
- 每个任务任意时刻只有一个 `owner_node`
- 状态转移必须过 `CanTransition`（`state.go`）

## 4. 质量门槛（提交前自查）

```bash
make vet && make test          # 必过
gofmt -l internal/ cmd/ adapters/   # 必须无输出
go test ./... -cover           # 核心模块尽量 >60%
```

已固定的工程规范：
- **错误处理**：每层 `fmt.Errorf("...: %w", err)` 包装，`errors.Is` 判断哨兵
- **复杂度**：避免循环内重复 DB 查询、O(n²) 扫描；状态机转移用 O(1) 查表
- **无死代码**：未使用的导出符号（类型/字段/函数）一律删除
- **并发安全**：`bus.Conn` 单写者锁；Core 的 peers map 用 RWMutex
- **异步执行**：任务执行在独立 goroutine，消息循环永不阻塞（否则 cancel 会滞后）
- **gitignore 陷阱**：`.gitignore` 里裸 `panda` 会误匹配 `cmd/panda/` 源码目录——编译产物必须锚定为 `/panda`（实际二进制在 `bin/`，已被 `bin/` 忽略）。新增目录名与二进制同名时务必检查 `git check-ignore`。

## 5. 已知决策与约束

| 决策 | 约束 | 来源 |
|---|---|---|
| Go 核心 + Python 胶水 | 核心锁死 Go；扩展 subprocess 模式 | 设计 §4 |
| modernc sqlite（纯 Go）| **RSS ~13-20MB**（已接受，调整验收为 ≤30MB）；换 CGO 驱动会破坏交叉编译 | phase0 §4 |
| WebSocket + JSON | 控制面可调试；数据面后续 MessagePack | 设计 §10.6 |
| WS 心跳 | 应用级（ledger）+ 传输级（ping/pong 30s）| §5.1 |
| 重连退避 | 1s→2s→…→30s 封顶 | main.go |

## 6. 内存实测方法

`make measure` 只采一次，**结果不可靠**（GC 时机噪声）。正确做法：
```bash
make build
for i in 1 2 3 4 5; do
  ./bin/panda --config testdata/mac-config.yaml >/dev/null 2>&1 &
  PID=$!; sleep 3
  ps -o rss= -p $PID | awk '{printf "%d MB\n", $1/1024}'
  kill -TERM $PID; wait $PID 2>/dev/null
done
# 取多次的众数/稳定值，不要用单次
```

已证实：GOGC 调整不构成稳定优化（曾观测 -34% 复测为噪声）。

## 7. 端到端验证

双节点真 WebSocket 协议测试（Go 内联，无需 Tailscale）：
```bash
go test ./internal/core/ -run 'TestTwoNodeProtocol|TestDelegateIdempotent|TestCancelPropagates' -v
```

真实二进制验证（loopback）：
```bash
make build
./bin/panda --config testdata/opi-config.yaml --card config/capabilities.orangepi3b.yaml &
./bin/panda --config testdata/mac-config.yaml --card config/capabilities.macbook.yaml &
# 观察日志中的 "peer registered" / "peer hello" 双向握手
```

跨设备（Tailscale）未验证——本机无 Tailscale，需香橙派就绪后补。

## 8. Phase 3 进度（下次会话从这里继续）

Phase 3 = 记忆 + 语音 + 安全（[phase3-report.md](./phase3-report.md) 为记忆系统完成报告）。

- **Sprint 3.1 双层记忆**：✅ `internal/memory`（USER.md/MEMORY.md 双文件 + 项目记忆 + 隔离墙 + daily 日志 + memory 工具执行器），已接入入口模型与 agent 上下文（FTS5 语义检索已撤回为死代码，留待会话记录功能）
- **Sprint 3.2 Dreaming 引擎**：✅ Light/REM/Deep 六维评分 + provenance taint gate + 调度器（Idle + 每日 1 次）+ DREAMS.md
- **Sprint 3.3 Skill 自进化**：✅ `internal/skills`（SKILL.md 格式 + 触发 + 作用域 + 渐进加载 + 生命周期 + 审批）
- **记忆逻辑（对齐 Hermes/OpenClaw）**：✅ save/skip 治理规则入 system prompt、模型自主合并（memory 工具）、离线沉淀（Dreaming）
- **Sprint 3.4 语音入口**：⚠️ sidecar + 管线代码已落地（`extensions/voice/` + `internal/entry/voice_pipeline.go`），待 Porcupine key + 麦克风硬件实测
- **Sprint 3.5 PWA + 安全加固**：✅ 安全加固 MVP（`internal/security/`）+ PWA 控制台（`internal/panel/` HTTP API + `web/pwa/`，审批流接通 review→done/failed）；Web Push 发送端（VAPID）仍待做
- **Sprint 3.6 集成与验证**：⚠️ 记忆隔离 / Dreaming / Skill 生命周期测试已覆盖；语音 E2E（待硬件）与安全渗透（人工）未做

Phase 1 遗留（已完成，存档）：
- **内存基线决策**：✅ 已接受 13-20MB，验收标准调整为 ≤30MB（2026-08-13）
- **Agent adapter 真实调用**：✅ claude_code.py 已走 DeepSeek 实测通过
- **统一入口模型**：✅ `internal/entry` 完成（answer/tool_call/task 三分类 + 校验 + 降级），`panda ask` 可端到端调用 DeepSeek
- **三层能力路由**：✅ 入口模型 `task` 输出 → `SubmitLocal` → 三层能力执行 → 持久化（本地闭环）
- **CLI 面板**：✅ `panda ask` + `status`/`queue`/`task`/`cancel`/`logs` 全部可用
- **第二个 adapter（OpenCode）**：✅ `adapters/opencode.py` + 卡片注册 + 路由测试
- **香橙派部署**：`bin/panda-linux-arm64` 静态二进制已就绪，待 Armbian 实测

## 9. 测试清单（当前状态）

| 包 | 覆盖 | 关键测试 |
|---|---|---|
| util | 95.7% | UUIDv7 格式/唯一/排序 |
| log | 84.6% | JSON 输出、level 过滤 |
| config | 85.2% | 加载/缺省/环境覆盖/坏文件 |
| bus | 81.9% | 信封往返、WS server/client、ping 循环 |
| storage | 75.0% | 迁移、幂等迁移、WAL |
| ledger | 77.3% | 注册/心跳/下线/名称过滤/多 agent 卡片 |
| commander | 67.5% | 三层路由、native 执行、adapter 桥、第二 agent、上下文 hash |
| entry | 66.7% | 三分类、校验、降级、模型调用（httptest）|
| core | 62.1% | 状态机、幂等、级联取消、超时、恢复、双节点协议、SubmitLocal、detail 落库 |
| cmd/panda | 5.9% | toTaskInput 合成（面板为黑盒覆盖）|

---

*PANDA 开发工作手册 · 2026-08-13 · 随开发持续更新*