# 分布式核心测试排期

这份排期对应 `scripts/lab/generate-three-node.sh`、`testdata/scenarios/long_task.py`、`scripts/scenario-model` 和 `scripts/task-timeline`。

## T0：本机静态和场景自测

- `GOCACHE=/tmp/openpanda-gocache go test ./internal/commander ./scripts/task-timeline`
- `python3 testdata/scenarios/long_task_test.py`
- `bash scripts/lab/generate-three-node_test.sh`
- `python3 tests/adapter_contract_test.py`：Claude Code/Codex/OpenCode fake CLI 黑盒契约（argv、cwd、白名单凭证、JSON/JSONL、progress、timeout）。
- 验收：场景 Agent 第一次输出不完整，第二次输出完整；配置生成包含 entry/agent/tools 三节点。

## T1：本机三进程网络测试

This is a `local multi-process simulation`, not real multi-device validation:
all three daemons share one physical host and are explicitly marked as three
isolated VM identities in the generated config. It verifies protocol and
routing behavior only. Do not report it as three-device mutual recognition.

- 在允许回环 bind/connect 的环境生成 lab：`scripts/lab/generate-three-node.sh .lab/three-node`
- 使用同一 `OPENPANDA_ADAPTER_DIR` 指向 `testdata/scenarios`
- 启动三个 `panda daemon`，等待三方 hello/heartbeat
- 用 `scripts/smoke-delegate` 验证 entry -> tools 的 native 委派
- 用长程任务入口验证 entry -> agent 的 Agent 委派和 supervisor 两轮闭环
- 用 `scripts/task-timeline` 汇总三个 SQLite 数据库
- 验收：状态、attempt、委派链和监督事件完整可解释。

## T2：故障注入测试

- dispatch 前断开目标节点
- agent running 中杀掉 adapter/daemon
- result 回传前断开连接并重连
- 重放旧 attempt、重复 result、伪造 sender
- 重启 entry 或 agent 后执行 `Recover`
- 验收：继续、重路由、review 或 failed，不能假成功、重复副作用或永久卡死。

## T3：真实双设备测试

- Mac 使用 `testdata/live/mac-*` 的能力卡和配置模板
- SBC 使用 `testdata/live/pi-*` 的能力卡和配置模板
- shared secret 只通过环境变量或受限配置注入
- 先验证 `sys:smoke`，再验证长程 Agent 任务
- 记录网络延迟、断线恢复时间、任务完成率和节点资源占用

## T4：连续运行和发布门禁

- 连续运行多任务、并发任务和至少一个超过 10 分钟的任务
- 检查 SQLite WAL、审计链、事件链、metrics 和任务恢复
- 只有 T1-T3 的核心场景稳定通过，才进入 UI、文档和外围运维收敛。
