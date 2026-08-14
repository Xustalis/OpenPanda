# PANDA 项目进度审计报告 · 虚假完成与实现质量

> 审计方法：Superpowers 方法论 Code Review 阶段
> 审计日期：2026-08-14
> 审计范围：需求基线（产品开发计划书）+ 各 Phase 报告 + 代码实现 + 测试/静态检查交叉验证
> 核心原则：**不采信任何报告结论**，对每一项"已完成"声明独立读代码、跑命令做交叉验证。

---

## 0. 执行摘要

| 维度 | 结论 |
|------|------|
| 测试/vet 声明 | ✅ **属实**：`go vet ./...` 0 告警，`go test ./...` 15 包全过（整体覆盖率 64.1%） |
| Phase 0 / Phase 1 声明 | ✅ 基本属实（含已知内存基线超标已接受） |
| **Phase 2 "全部落地"声明** | ❌ **虚假完成**：防御链、合并门禁、熔断器、GPIO 权限均未实现 |
| Phase 2 Sprint 2.5 "GPIO ✅" | ⚠️ **内部矛盾**：能力跑通但权限模型明确未接入（报告自承） |
| Phase 3 "✅ 完成" 标题 | ⚠️ **易误读**：报告自身范围仅限记忆/Skill，语音/PWA/安全加固按计划书属 Phase 3 但**未启动** |
| 既有《综合分析报告》行号引用 | ❌ **不实**：声称"已重新核查"却引用了不存在的文件与错位行号 |
| 实现质量（已实现部分） | ⚠️ 存在 9 项具体缺陷（状态机绕过、缺授权、死代码、无沙箱、panic 风险等） |

**严重程度统计**：P0（Critical）5 · P1（Major）6 · P2（Minor）4 · P3（文档/残留）3。

---

## 1. 已验证为"真完成"的项（防止误伤）

为避免把真实的进展也打成"虚假完成"，先确认以下声明**经独立验证属实**：

1. **构建与测试门禁通过** — 现场执行 `go vet ./...`（0 告警）、`go test ./...`（15 包全 ok）。覆盖率 64.1%（core 62.4%、cmd/panda 4.3%，后者因 ask 子命令尚未被单测覆盖，属已知缺口，非虚假）。
2. **权限分级门控已接线** — `internal/commander/commander.go:92` `Route` 填 Tier，`commander.go:112` `Execute` 调 `defense.Authorize` 对 Tier-2 拦截（permission.go:24）。命令级权限判定真实生效。
3. **Skill 自进化系统真实落地** — `internal/skills/{generator,tracker,lifecycle}.go`；`tracker.Record` 在 `handlers.go:435` 接线、`RecordUse` 在 `handlers.go:422` 接线。注意：`generator.Generate` 是确定性模板（无 LLM 蒸馏），与 phase3-report 措辞"轻量模型蒸馏"略有出入，但功能本身真实。
4. **上下文分级传输机制可用** — `packContext`（`context.go:34`）pointer/summary 两路真实工作；跨设备委派闭环经 sprint5 实测日志跑通（Mac↔香橙派 3B）。
5. **状态机正常转移正确** — `CanTransition`（`state.go:96`）对常规 10 态转移表正确（除下文明示的缺口外）。

---

## 2. P0 — Critical（虚假完成 / 高安全风险）

### P0-1　Phase 2 报告声称"计划全部落地"，但防御链/门禁/熔断器/GPIO 权限均未实现
- **声明来源**：`docs/phase2-sprint3.md:9` "至此 Phase 2 的计划全部落地"；`:5` 标"✅ 完成"。
- **需求基线**：`PANDA-产品开发计划书.md:472-482` 要求
  - P2-17 `internal/defense/scope_guard.go`（范围漂移检测）
  - P2-18 `internal/defense/loopdetect.go`（收益递减/循环检测）
  - P2-19 `internal/defense/resource_guard.go`（超时/资源超限）
  - P2-20 `internal/defense/escalation.go`（上级接管）
  - P2-21 `internal/defense/postmortem.go`（对抗性剖析）
  - P2-25/26 `internal/defense/merge_gate.go`（合并门禁）
  - P2-27 `internal/defense/circuit.go`（熔断器）
- **代码事实**：`internal/defense/` 目录下**仅有 `permission.go`**（+测试），上述 7 个文件**全部不存在**（Glob 验证）。计划书:183/185/186 功能矩阵将防御链、合并门禁、熔断器标在 Phase 2（●）。
- **判断依据**：报告结论与仓库事实直接冲突，属**虚假完成（false-complete）**。后果：任何"越权/失控任务"在当前系统无自动拦截能力。
- **修正建议**：报告应改为"仅权限分级（Tier）落地，防御链/门禁/熔断器未实现"，并将对应任务退回 in_progress。

### P0-2　GPIO 硬件能力被标 ✅，但权限模型自承未接入
- **声明来源**：`docs/phase2-sprint5.md:16`/`41` 表格与验证均标 GPIO ✅。
- **矛盾证据**：同一报告 `phase2-sprint5.md:98` 明确写"**GPIO 权限模型未接入**（Sprint 2.4）：`gpio:read` 用 `sudo` 直接执行，尚未纳入防御链/权限判定"。
- **代码事实（已与今日早前复核对齐）**：当前能力卡 `gpio:read` 实配 `gpioinfo`、**tier 1、无 sudo**（sprint5 报告的 sudo 描述为旧状态）；但核心事实不变——GPIO 执行**未纳入防御链/权限判定**（Tier-1 自动放行，Sprint 2.4 设计的 GPIO 权限模型未接入）。
- **判断依据**：同一文档前后矛盾；能力"能跑"≠"权限合规"。按计划书 P2-29~P2-34，GPIO 权限是验收项，当前**仅完成执行、未完成权限接入**，不应标 ✅。

### P0-3　agent 子进程无沙箱隔离
- **位置**：`internal/commander/adapter.go:54` `cmd.Env = append(os.Environ(), modelEnv(model)...)`
- **问题**：Python adapter 子进程继承父进程**完整环境变量**（含密钥、网络凭证），无任何 seccomp/namespace/资源上限。结合 P0-1 防御链缺失，执行侧无兜底。
- **判断依据**：计划书将"沙箱"列入 Phase 3 安全加固（P3-25+），但作为远程委派执行第三方 adapter 的基础防线，**在未落地前跨设备执行已具备真实风险**。属"已实现但未达安全基线"。

### P0-4　命令 Tier 首词白名单可被绕过
- **位置**：`internal/defense/permission.go:35` `TierFromCommand` 仅匹配命令首词。
- **问题**：`bash -c 'rm -rf /'`、`python -c 'import os; os.system("...")'`、`sh -c '...'` 等首词不在白名单 → 被判为 Tier 1（可逆/自动放行）。破坏性操作可经解释器逃逸 Tier 判定。
- **判断依据**：设计文档 §16 意图是"特权/破坏性动词默认 Tier 2"，首词匹配无法覆盖解释器封装，属实现与需求不符。

### P0-5　`mustUUID` 在热路径 panic
- **位置**：`internal/core/core.go:382` `func mustUUID()` → `util.UUIDv7()` 失败即 `panic(err)`。
- **问题**：UUIDv7 依赖时间戳/随机数源，在资源紧张或时钟异常时可能返回 err，导致**整个 daemon 进程崩溃**而非优雅降级。该函数被 `reply`（`handlers.go:568`）、`NewEnvelope` 等高频路径调用。
- **判断依据**：生产代码对基础设施类错误用 panic 违反健壮性基线；应返回 error 并由上层处理。

---

## 3. P1 — Major（实现缺陷 / 逻辑错误）

### P1-1　`Cancel` 绕过状态机与 owner 守卫
- **位置**：`internal/core/tasks.go:462` `func (s *TaskStore) Cancel`
- **代码**：直接 `UPDATE tasks SET state=cancelled ...`，仅跳过终态（`Terminal`），**不调用 `CanTransition`**、**不校验 lease owner**。
- **矛盾**：`docs/phase12-review.md` 称"transition 统一做 state/owner/version 三重校验"，但 Cancel 是例外直通，声明不实。
- **风险**：任意可调用 Cancel 的路径都能把 `running`/`waiting_context` 任务强置 cancelled，破坏状态不变量。

### P1-2　`handleCancel` 无来源/owner 鉴权
- **位置**：`internal/core/handlers.go:555` `func (c *Core) handleCancel`
- **代码**：解析 `TaskCancelPayload` 后直接 `CancelCascade`，**无校验发送方是否为任务 owner 或父任务节点**。
- **风险**：网络中的任意节点可发 `task_cancel` 取消他人任务（P2P 场景下为越权取消）。

### P1-3　`waiting_context` 无 → `cancelled` 转移边
- **位置**：`internal/core/state.go:105-106` `case StateWaitingCtx: return to == StateRunning || StateFailed || StateExpired`
- **问题**：状态表**不允许** `waiting_context → cancelled`，但 `Cancel`/`CancelCascade`（tasks.go:462/483）会强制写入 `cancelled`。
- **矛盾**：状态机定义与 Cancel 实现内部不一致——Cancel 写入了一个状态机认为非法的转移，破坏"状态机是唯一真相源"的设计前提。

### P1-4　上下文 `full` 级从未产生
- **位置**：`internal/core/context.go:34` `packContext`
- **代码**：仅 `file`+`RepoPath` 打包为 `pointer`；其余一律退化为 `summary`。`full` 级**只在调用方显式传 `ContextHash`+`ContextLevel="full"` 时才透传**，自身从不生成。
- **配套缺口**：`RepoPath` 在 CLI 未接线（`cmd/panda/submit.go:22` 注释 "MVP: CLI not yet wired"）。
- **风险**：需要完整上下文的高风险合并场景只能拿到 summary，与设计 §上下文分级传输"full 用于高风险"目标不符。

### P1-5　`circuit_breakers` / `task_dependencies` 表为死脚手架
- **位置**：`internal/storage/migrate.go` 建 6 张表，含 `task_dependencies`、`circuit_breakers`。
- **代码事实**：Grep 全仓，除 `migrate.go` 与 `storage_test.go` 外，**无任何生产代码读写这两张表**（与 P0-1 呼应：熔断器逻辑根本未实现，仅留空表）。
- **判断依据**：建表即"完成"的假象；属于计划书 P2-27 熔断器、P2 任务依赖的未落地占位。

### P1-6　`scheduler.Priority` / `scheduler.Account` 为死代码
- **位置**：`internal/scheduler/priority.go:20` `Priority`、`internal/scheduler/capacity.go:30` `NewAccount`/`TryAcquire`
- **代码事实**：Grep 全仓，这两个符号**仅被各自 `_test.go` 调用**；生产调度（`core`/`commander`）使用的是 `ledger.Capacity`（另一结构体），未引用 scheduler 包的评分/容量账户。
- **判断依据**：计划书:468 称"调度评分后续增强基础（优先级评分 + 容量并行）"，实际代码未接入主链路，属"写了未用"。

---

## 4. P2 — Minor（实现欠妥 / 已知残留）

### P2-1　`ask` 与 daemon 复用同一 NodeID（节点身份冲突残留）
- **位置**：`cmd/panda/ask.go:85` 与 `cmd/panda/main.go:100` 均为 `core.NodeID(cfg.Node.Name)`。
- **问题**：`panda ask` 作为独立进程启动时会用与 daemon **相同的节点身份**；若 ask 会话与 daemon 并发运行，网格内出现 NodeID 冲突。
- **来源**：`docs/phase2-sprint5.md` 已记录该问题，但 `ask.go:85` 未修复。

### P2-2　Phase 3 报告文件清单列已删除文件
- **位置**：`docs/phase3-report.md:57` 列出 `internal/memory/search.go`（FTS5 冷存储检索）为交付物。
- **事实**：该文件**已不存在**（phase3-report 自身修复记录"删除 Searcher"）。清单未同步更新。
- **判断依据**：文档瑕疵，但会误导读者以为 FTS5 检索已实现。

### P2-3　既有《综合分析报告》行号引用不实，降低其可信度
- **来源**：`PANDA-综合分析与优化报告.md` 声称"所有行号已重新核查代码仓库确认"。
- **抽查不符**：
  - 称 `extractJSONObject` 在 `internal/entry/entry.go:107`，但 `internal/entry/` 目录**不存在**，真实位置为 `internal/core/router.go:107`。
  - 称 `run` 在某文件，实际在 `internal/core/handlers.go`。
  - 称 `Dreamer.light` 在某行，实际在 `internal/extensions/dreamer/dream.go:82`。
- **判断依据**：该报告本身的"已核查"声明不实，因此其列出的 I-01~I-17 问题项**需逐条重新核对代码**后方可采信。

### P2-4　Phase 3 标题"✅ 完成"易被误读为计划书全 Phase 3 完成
- **事实**：`docs/phase3-report.md:139` 本身把"语音入口（3.4）""安全加固（3.5）"列为**下一步**；报告范围仅 Sprint 3.1-3.3（记忆+Skill）。
- **风险**：计划书 Phase 3 含 P3-20~P3-24 语音、P3-25~P3-32 PWA+安全加固（沙箱/网络白名单/密钥隔离），这些**均未启动**（`extensions/voice`、`web/pwa`、`internal/security` 目录全空，Glob 验证）。读者只看标题会误判为全完成。

---

## 5. P3 — 文档/一致性（低优先级）

- **P3-1** `phase2-sprint2.md` 注明 "full 级由调用方内联携带"，与 P1-4 现状一致，但计划书上下文分级目标未达成，建议计划书与现状对齐。
- **P3-2** `docs/phase12-review.md` 称并发安全、CancelCascade 无数据竞争——与 P1-1/P1-3 状态机绕过问题并存，该结论需限定范围后重述。
- **P3-3** `config.example.yaml` 与 `config.yaml` 缺少 GPIO 权限字段占位，与 P0-2 权限缺口呼应。

---

## 6. 审计结论与建议动作

**结论**：项目在"测试门禁 + 权限分级 + 跨设备委派闭环 + 上下文传输 + Skill 系统"上确为真实完成；但在**Phase 2 防御体系（防御链/门禁/熔断器/GPIO 权限）**与**Phase 3 语音/PWA/安全加固**上存在系统性"报告领先于代码"的虚假完成，且已实现的取消/鉴权/执行侧存在多处与安全基线不符的缺陷。既有《综合分析报告》因行号不实，其结论需重新核对。

**立即动作（建议）**：
1. 将 `phase2-sprint3.md` "全部落地"更正为"仅权限 Tier 落地"，退回防御链/门禁/熔断器任务。
2. 修复 P0-3/P0-4/P0-5（沙箱 env、Tier 解释器绕过、mustUUID 改返回 error）。
3. 修复 P1-1/P1-2/P1-3（Cancel 走状态机+owner 校验、handleCancel 加来源鉴权、补 waiting_context→cancelled 边）。
4. 清理死脚手架（P1-5/P1-6）或接入主链路，避免"建表即完成"假象。
5. 重核《综合分析报告》所有行号与文件引用。

---
*审计方式说明：本报告每一项结论均附 file:line 证据，且对"测试/vet 通过""权限门控""Skill 系统""跨设备闭环"等声明做了正向验证，非一律否定。*
