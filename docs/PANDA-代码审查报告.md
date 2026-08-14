# PANDA 项目代码审查分析报告

> **来源**：workbuddy hy3
> **审查日期**：2026-08-14
> **审查对象**：`/Users/xenith/Documents/project/panda`（PANDA · Personal Adaptive Node-based Distributed Assistant）
> **对照基准**：`PANDA-总览设计文档.md`（v4.6）、`PANDA-产品开发计划书.md`（v1.1）、`docs/` 下各 phase/sprint 报告
> **方法**：静态代码审查（不修改任何代码）+ 编译/ vet / 全量测试的客观验证 + 需求逐项对照。遵循 Superpowers 方法论的 Code Review 阶段，按严重性分级（Critical / Major / Minor）。
> **工具链客观结果**：`go build ./...` 通过；`go vet ./...` 通过；`gofmt -l .` 无未格式化文件；`go test ./internal/... ./cmd/...` **15 个包全部通过**；Go 工具链 1.26.5（darwin/arm64）。

---

## 一、执行摘要

**一句话结论**：PANDA 的**软件核心质量很高**（编译/测试/规范全绿、架构清晰、状态机与委派链路实现扎实），但**文档对"完成度"的宣称明显超前于代码**——计划书 Phase 2 的「防御链」「熔断器」「合并门禁」「GPIO 硬件扩展」与 Phase 3 的「语音入口」「PWA 控制台」「安全加固（沙箱/网络白名单/密钥审计）」在代码中**均不存在**，而 `docs/phase2-sprint3.md`、`phase2-sprint5.md` 仍以「✅ 完成 / Phase 2 完成」为标题。这是本次审查最核心的发现。

**总体评级**：

| 维度 | 评级 | 说明 |
|---|---|---|
| 代码可读性 / 规范性 | **良好** | 命名清晰、注释充分、gofmt/vet 干净、模块边界合理 |
| 错误处理 | **良好** | 普遍使用 `%w` 包装、日志分级；存在 1 处 panic 风险 |
| 并发与状态一致性 | **良好** | mutex/sync.Map 使用得当，状态机与幂等契约正确 |
| 测试覆盖 | **良好** | 全量测试通过，核心模块有单测（memory 82%+、skills 80%+，报告自述） |
| 需求吻合度 | **部分** | Phase 0/1/2 核心、Phase 3 记忆与 Skill 真实落地；防御链/硬件/语音/安全/PWA 缺失 |
| 文档准确性 | **需修正** | 多处「✅ 完成」与实际代码不符，存在"报告领先代码"风险 |

---

## 二、需求对照（计划书 vs 实际代码）

下表逐 Phase 标注实现状态。`●`=计划本期交付。

### Phase 0 · 本地任务闭环
| 计划项 | 代码实际 | 状态 |
|---|---|---|
| 项目骨架 / 配置 / 日志 / SQLite / UUIDv7 | `cmd/panda`, `internal/config`, `internal/log`, `internal/storage`, `internal/util/uuid.go` | ✅ 已实现 |
| 节点注册 / 心跳 / 能力目录 | `internal/ledger/capability.go`, `internal/core/node.go`（注：计划文件名 `ledger/join.go`、`core/beat.go` 未采用，功能归入现有文件） | ✅ 已实现（文件布局与计划不同，但功能到位） |
| 任务状态机（10 态 + 合法转移 + 幂等 + 级联取消） | `internal/core/state.go` | ✅ 已实现，状态表正确 |
| WebSocket 通信 / 消息信封 / router / recv / hello/join | `internal/bus/ws.go`, `internal/bus/msg.go`, `internal/core/handlers.go`（`router.go`/`recv.go` 未单列，逻辑并入 handlers/dispatch） | ✅ 已实现 |
| native 执行器 + 1 个 agent adapter + Commander 整合 | `internal/commander/{native,commander,adapter}.go`, `adapters/claude_code.py` | ✅ 已实现 |
| 任务 context（file 类型打包传输） | `internal/core/context.go`, `internal/ctxstore/store.go` | ✅ 已实现（含 SHA-256 缓存） |

> **结论**：Phase 0 核心目标**真实达成**，文件命名与计划有差异但属合理重构。

### Phase 1 · 单级委派与入口模型
| 计划项 | 代码实际 | 状态 |
|---|---|---|
| 统一入口模型（分类 answer/tool_call/task） | `internal/entry/{classify,model,router,prompt,fallback}.go` | ✅ 已实现 |
| 三层能力路由（native/agent/manual） | `internal/commander/commander.go`（`Route`）、`internal/scheduler/route.go` | ✅ 已实现 |
| CLI 面板（ask/queue/task/cancel/logs/status） | `cmd/panda/{ask,panel,skill}.go`（计划单列文件合并为 `panel.go`） | ✅ 已实现 |
| 第二个 agent adapter + 注册发现 | `adapters/opencode.py`, `internal/commander/adapter.go` | ✅ 已实现 |

> **结论**：Phase 1 **真实达成**。

### Phase 2 · 多级委派 + 上下文 + 防御（**重点问题区**）
| 计划项 | 代码实际 | 状态 |
|---|---|---|
| P2P 逐边委派（chain / 转发 / 子调度 / transfer / 评分） | `internal/scheduler/{chain,route,priority,capacity}.go`, `internal/core/handlers.go`（转发/链回传） | ✅ 已实现 |
| 多类型上下文 + 分级传输（pointer/summary/full + LRU） | `internal/core/context.go`, `internal/ctxstore/store.go` | ✅ 已实现（核心闭环） |
| **防御链 Layer 1-4**（范围漂移/收益递减/超时资源/上级决断/对抗自审） | `internal/defense/` 仅有 `permission.go`（+ `permission_test.go`）；缺失 `scope_guard.go`/`loopdetect.go`/`resource_guard.go`/`escalation.go`/`postmortem.go` | ❌ **未实现** |
| **Tier 1/2 权限判定引擎 + 自动审批 + 用户决断** | `internal/defense/permission.go`（`Authorize` + `TierFromCommand`） | ⚠️ **仅确定性 Tier 门禁**，自动审批/用户决断回传/审批流缺失 |
| **合并门禁**（范围检查 / 破坏性命令匹配） | 缺失 `defense/merge_gate.go` | ❌ **未实现** |
| **熔断器**（per agent:task_type，3 失败→open→cooldown→half_open） | schema 中预留了 `circuit_breakers` 表（`migrate.go:68`，含 `failure_count`/`state`/`cooldown_until` 字段），但全仓**无任何 Go 代码读写该表**；`circuit.go` 不存在 | ❌ **未实现（仅有空表脚手架）** |
| **GPIO 硬件扩展**（Python sidecar + Unix socket + hardware 上下文 + `panda detect`） | `extensions/` 下存在 `dreamer/`、`gpio/`、`voice/` 三个**空子目录**，但无任何 `.py` 实现文件（无 `servo.py`/`buzzer.py`/`sidecar.go`）；`config/capabilities.orangepi3b.yaml` 中 `gpio:read` 仅为普通 native 命令（`gpioinfo`，tier 1，无 sudo）；`phase2-sprint5.md` 自承"GPIO 权限模型未接入" | ❌ **未实现（硬件/sidecar 层缺失，仅有空目录占位）** |

> **结论**：Phase 2 的**通信/委派/上下文核心**真实落地，但 Sprint 2.3「防御链 + 权限」与 Sprint 2.4「GPIO 硬件」**绝大部分未实现**。`docs/phase2-sprint3.md` 第 9 行称"Phase 2 的计划全部落地"、第 98 行 `phase2-sprint5.md` 称"Sprint 2.5 完成"——**与代码事实不符**。
>
> **复核勘误（2026-08-14 二次核对）**：经逐条比对代码，更正三处表述——(1) `extensions/` 并非空目录，而是含有 `dreamer/`、`gpio/`、`voice/` 三个**空子目录**（无 `.py` 实现），故"extensions 为空 / voice 不存在"不准确；(2) `internal/security/` 目录**存在但无 Go 源文件**（无 security 包），并非"缺失"；(3) `circuit` 命中实为 `migrate.go:68` 的 `circuit_breakers` **表脚手架**（schema 已建，无任何 Go 代码读写），并非 `scheduler_tier` 关键字；熔断器逻辑仍缺失。详见下文修订。

### Phase 3 · 记忆 + 语音 + 安全
| 计划项 | 代码实际 | 状态 |
|---|---|---|
| 双层记忆（Hermes + 项目）+ 语义检索 + 注入 + 隔离墙 + daily | `internal/memory/{hermes,project,injector,isolation,daily,format,provenance,signals}.go` | ✅ 已实质实现 |
| Dreaming 引擎（Light/REM/Deep + 调度 + DREAMS.md + provenance） | `internal/memory/{dream,dream_scheduler,dream_diary}.go` | ✅ 已实质实现 |
| Skill 自进化（触发/生成/审批/作用域/加载/生命周期） | `internal/skills/{skill,trigger,generator,loader,lifecycle,tracker}.go` + `cmd/panda/skill.go` | ✅ 已实质实现 |
| **语音入口**（Porcupine 唤醒 / ASR / TTS / VAD / voice_pipeline） | 全仓无 `voice`/`porcupine`/`asr`/`tts` 实现代码；`extensions/voice/` 目录存在但为空（无 `.py`） | ❌ **未实现**（报告已声明"下一步"） |
| **PWA 控制台 + Web Push + 移动审批** | `web/pwa/` **目录为空** | ❌ **未实现** |
| **安全加固**（沙箱 / 网络白名单 / 密钥隔离 / 审计日志） | `internal/security/` 目录存在但无 Go 源文件（无 security 包）；仅 `defense/permission.go` 的 Tier 门禁 | ❌ **未实现**（报告已声明"下一步"） |

> **结论**：Phase 3 的**记忆与 Skill 子系统真实且质量较高**（报告 §8 还记录了两轮自查修复）。但"语音/安全/PWA"三大块**完全未启动**，且 `web/pwa` 与 `extensions` 物理上为空目录。

### Phase 4 · 硬件 + 发布（预期未来）
| 计划项 | 状态 |
|---|---|
| 桌宠硬件 / 3D 外壳 / 托管服务 `server/` / GitHub Actions / Homebrew | 均未开始（符合计划"持续/远期"定位，非缺陷） |

---

## 三、文档与代码一致性核查（核心发现）

`docs/` 下存在一组"完成报告"，部分标题与代码严重脱节：

1. **`docs/phase2-sprint3.md`**（防御链/权限/GPIO/调度评分）
   - 标题/首段："本阶段补齐 Phase 2 的最后两块 MVP 基线（防御链 + 权限）……**至此 Phase 2 的计划全部落地**"；结尾"**Phase 2 完成**"。
   - 代码事实：防御链（scope/loop/resource/escalation/postmortem）、合并门禁、熔断器、GPIO sidecar **均无**（`internal/defense/` 仅 `permission.go` + 测试；`migrate.go:68` 有 `circuit_breakers` 空表脚手架但无逻辑）。`permission.go` 仅含确定性 Tier 门禁，其文件头注释本身写明"Layer 2+ 是 later phases"。
   - 报告第 62 行虽承认"对抗性双模型自审、合并门禁均属后续增强"，但仍以"完成"定调——**表述与实现存在实质性落差**。

2. **`docs/phase2-sprint5.md`**（多机联调）
   - 第 16、41 行以 `✅` 标注"GPIO 硬件能力 `gpio:read` 跨设备执行"；第 98 行自承"**GPIO 权限模型未接入**……`gpio:read` 用 `sudo` 直接执行，尚未纳入防御链/权限判定"。
   - 复核注：该报告称"sudo gpioinfo"，但**当前** `config/capabilities.orangepi3b.yaml` 中 `gpio:read` 实际为 `gpioinfo`（libgpiod）、`tier: 1`、**无 sudo**；报告的 sudo 描述与当前代码不符。无论是否带 sudo，"GPIO 权限模型未接入"属实——`gpio:read` 只是被当作普通 native 命令执行，无 GPIO 专属权限路径或硬件上下文类型。

3. **`docs/phase3-report.md`**（记忆系统）
   - 相对诚实：明确列出 voice/security 为"下一步"，并在 §6 列出与计划书的偏离。**记忆与 Skill 部分属实**，质量良好。可作为"诚实报告"的范本。

4. **`docs/DEVELOPMENT.md`** 第 139 行已标注"Sprint 3.4 语音入口 ⬜"，与代码一致——说明作者心中清楚缺口，但 phase2 系列报告的"完成"标题与之矛盾。

**风险判定**：文档"报告领先代码"会导致（a）外部协作者/评审误判项目成熟度；（b）决策门（计划书 §4.0.5/§4.1.5）基于不实"完成"信号进入下一 Phase。建议把 `phase2-sprint3/5` 标题改为"软件核心完成（防御链/硬件待补）"并明确待办清单。

---

## 四、项目结构与目录布局评估

**优点**：
- 分层清晰：`core`（节点生命周期+状态机+消息处理）、`commander`（路由+执行）、`bus`（传输）、`ledger`（能力目录）、`scheduler`（委派决策）、`ctxstore`（上下文快照）、`memory`/`skills`（Phase 3）、`storage`（持久化）、`config`/`log`/`util`（基建）。依赖方向合理。
- 计划书指定的文件名（如 `ledger/join.go`、`core/recv.go`、`commander/result.go`）虽未严格沿用，但功能已归并到语义正确的文件中，属合理演进，不算缺陷。
- `adapters/` 用 Python 子进程实现 agent 适配，符合"胶水层 30 行"的设计意图，且具备错误处理。

**问题**：
- **空目录误导**：`extensions/` 下确实存在 `gpio/`、`voice/`、`dreamer/` 三个**子目录**，但均为空（无 `.py`/`.go` 实现），而计划书与部分报告暗示其已有实现；`web/pwa/` 同样物理为空。应在 README/文档中明确"未实现"或删除空目录。
- **`internal/security/` 目录存在但无 Go 源文件（无 security 包）**：计划书 Phase 2/3 的权限/沙箱/网络/密钥/审计全部约定在 `internal/security/*`，实际仅落在 `internal/defense/permission.go`。目录语义与计划偏离，新人不便定位。
- **`check_emails.py` / `fetch_emails.py`** 位于仓库根，与 PANDA 核心无关（疑似个人工具混入），建议移出或归入独立目录，避免污染核心产物。

---

## 五、代码质量评估

### 5.1 可读性与规范性 —— 良好
- 命名规范（如 `CanTransition`、`Route`、`RelayToParent`）、注释密度高且解释"为什么"而非"是什么"（例如 `core.go` 对握手终止、peer 移除的注释）。
- `gofmt -l` 无输出，`go vet` 无告警。
- 类型与接口边界清晰（如 `commander.Plan`/`Result`、`bus.Envelope`/`Payload`）。

### 5.2 错误处理 —— 良好（1 处风险）
- 普遍用 `fmt.Errorf("...: %w", err)` 保留错误链；对 `ErrIllegal`/`ErrConflict`/`ErrCancelled` 用 `errors.Is` 判断，设计正确。
- 消息处理对坏 payload 走 `reply(decline)` 而非崩溃，稳健。
- **风险点（Major）**：`internal/core/core.go:382` `mustUUID()` 在 UUID 生成失败时 **`panic(err)`**。该函数在**每条出站消息**与 `dial` 握手中被调用（`dispatch`→`reply`、`forwardDelegated`、`handleResult`→`signalResult` 等热路径）。一旦 UUIDv7 失败（极端但非零概率），goroutine 直接 panic：在 `MaintainPeer` 的 `for` 循环中被调用时，panic 会**杀死整个重连循环且不被 recover**，导致该对等节点的重连永久失效。应改为返回 `error` 并由调用方降级（记录日志并跳过该消息/重连）。

### 5.3 并发与状态一致性 —— 良好
- `peers`/`greeted` 受 `mu` 保护；`waiters`/`pendingCtx` 用 `sync.Map`；`Daily.Append/Prune` 已加 `sync.Mutex`（phase3 报告 §8 第一轮修复，已落地）。
- 状态机 `CanTransition` 表正确；`handleResult` 对 attempt_id 做陈旧性校验（`p.AttemptID != t.AttemptID` 时忽略），解决了计划书"旧 attempt 不覆盖新 attempt"的验收点。
- 幂等：`handleDelegate` 对已存在的 task_id 直接忽略；`handleResult` 对未知任务会 `CreateFromRemote` 重建——需注意：此分支构造的 chain 为 `[本节点, 来源]`，可能与上游真实委派链长度不一致，极端情况下 `relayToParent` 的 `Predecessor` 计算会指向错误父节点（Minor，MVP 可接受，建议加注释或回传原始 chain）。

### 5.4 测试 —— 良好
- 全量 `go test ./internal/... ./cmd/...` **15 个包全部 ok**（core 7.4s、bus/commander/entry 等稳定通过）。
- 覆盖到状态机、幂等、委派链、上下文、权限、Skill 生命周期、记忆评分等关键路径；memory/skills 自报覆盖率 80%+。
- 不足：未见显式的 **E2E/集成** 测试脚本（`docs` 报告称在 loopback 用真实 WebSocket 验证，但未以 `_test.go` 或 CI 形式固化），回归保障偏弱。

### 5.5 安全性 —— 中等（多处与计划背离）
1. **Tier-2 权限门禁可被轻易绕过（Major）**：`internal/defense/permission.go:35` `TierFromCommand` 仅匹配**命令首词**固定白名单（`rm`/`dd`/`sudo`/`mkfs` 等）。`bash -c 'rm -rf /'`、`python -c '...'`、`sh -c`、`curl|sh`、或任何解释器包装都能**完全绕过** Tier-2 校验。计划书 P2-25/26 的"合并门禁（diff vs scope + 破坏性命令模式匹配）"本应兜底，但**未实现**。当前"权限模型"仅对最直白的命令有效，存在**虚假安全感**。
2. **Agent 子进程无隔离（Major）**：`internal/commander/adapter.go:54` 以 `os.Environ()` + 注入 env 启动 adapter，**继承全部父进程环境变量且无沙箱/网络限制**（计划书 P3-29 沙箱、P3-30 网络白名单未做）。若 agent 适配器或 prompt 注入导致执行任意命令，节点无防护边界。鉴于 PANDA 跨设备自动执行任务，此风险随规模放大。
3. **密钥管理（良好，需肯定）**：`internal/config/config.go` 支持 `PANDA_MODEL_API_KEY` 等 env 覆盖，且 `config.example.yaml` 中 `api_key` 已注释——默认**不把密钥写入文件**，经 env 注入 adapter（`adapter.go:31-33` 仅注入到子进程 env、不写日志）。此设计契合计划书 P3-31 意图，**应予保留并在文档强调**。
4. **审计日志缺失（Minor）**：计划书 P3-32 要求高风险操作全记录（谁/何时/什么/结果）；当前仅有 `task_events` 状态流水与 slog，无独立的不可篡改审计层。

---

## 六、问题清单（按严重性分级）

### Critical（阻塞发布 / 数据或安全致命）
- 无。未发现数据丢失、密钥明文落盘或可导致系统崩溃的致命缺陷。

### Major（核心功能/安全缺口，应在进入下一 Phase 前解决）
1. **防御链整体缺失**：范围漂移、收益递减、超时/资源超限、上级决断、对抗自审、合并门禁、熔断器——计划书 P2-17~P2-28 几乎全部未实现，仅 `permission.go` 的确定性 Tier 门禁落地。（`internal/defense/` 仅 `permission.go` 1 个实现文件 + `permission_test.go`，无 scope_guard/loopdetect/resource_guard/escalation/postmortem/merge_gate/circuit 等）
2. **Tier-2 门禁可被绕过**：`defense/permission.go:35` 首词白名单易被包装命令绕过；合并门禁未实现导致无兜底。（安全风险）
3. **Agent 子进程无沙箱/网络隔离**：`commander/adapter.go:54` 继承全量 env 且无限制，跨设备自动执行场景下风险放大。
4. **`mustUUID()` panic 风险**：`core.go:382` 在消息热路径 panic，可杀死对等节点重连循环且不可恢复。
5. **文档"完成"宣称与代码不符**：`phase2-sprint3.md`/`phase2-sprint5.md` 以"完成"定调但防御链/GPIO 硬件未实现，误导决策。

### Minor（体验/一致性/可维护性）
6. **空目录误导**：`extensions/`、`web/pwa/` 为空却出现在计划/报告中，建议标注或删除。
7. **`internal/security/` 目录存在但无 Go 源文件（无 security 包）**：权限/安全相关代码散落 `defense/`，与计划目录约定不符。
8. **仓库根 `check_emails.py`/`fetch_emails.py`** 与核心无关，建议迁出。
9. **`handleResult` 的 `CreateFromRemote` 分支**构造的 chain 可能与真实委派链不一致（见 5.3）。
10. **E2E/集成测试未固化**为可重复脚本，回归保障弱。
11. **`phase3-report.md` §4** 列出 `internal/memory/search.go` 为"新增"，但该文件已在 §8 第一轮自查中被删（死代码）；文档内部略有不一致，建议同步修订。

---

## 七、潜在风险（前瞻性）

- **决策门失真**：若基于"Phase 2 完成"的误判进入 Phase 3/4，会在防御与安全真空上叠加更多功能，技术债复利增长。
- **跨设备自动执行的攻击面**：native 命令 + agent 适配器的自动委派，在缺沙箱/缺合并门禁时，任一被攻陷的入口（如 prompt 注入）可横向移动至全部联网节点。
- **硬件 Sprint 未启动却被"✅"**：GPIO 硬件扩展（sidecar/servo）未实现，`gpio:read` 当前仅作为 tier 1 的普通 native 命令（`gpioinfo`，无 sudo）执行，无 GPIO 专属权限路径或硬件上下文类型；一旦在真实香橙派多机环境铺开且仍无权限模型兜底，存在误操作系统风险。
- **测试金字塔倒挂**：计划书 §5.1 要求"单测 ~200+、集成 ~50、E2E ~20"，当前单测充足但集成/E2E 未见固化，跨模块回归主要依赖手工 loopback 验证。

---

## 八、改进建议（按优先级）

**P0 — 立即（在进入下一 Phase 前）**
1. 修正 `phase2-sprint3.md`/`phase2-sprint5.md` 标题与结论，明确"防御链/熔断器/合并门禁/GPIO 硬件**未实现**"，列出待办；与 `DEVELOPMENT.md` 的 ⬜ 标记对齐。
2. 将 `mustUUID()` 改为返回 `error`，调用方降级处理（core.go:382），消除 panic 热路径。
3. 强化 Tier-2 判定：在 `defense` 包实现**命令解析层**（剥离 `bash -c`/`sh -c`/`python -c` 等包装后取真实命令再判定），至少挡住最常见绕过；并将"合并门禁"列入明确待办。

**P1 — 短期（Phase 2 收尾）**
4. 实现计划书 P2-27 **熔断器**（`defense/circuit.go`，per agent:task_type 三态机）——它是防御链里性价比最高、最易独立验证的一块。
5. 实现 P2-25/26 **合并门禁**（范围 diff 检查 + 破坏性命令模式匹配），作为 Tier-2 的兜底。
6. 为 agent 子进程增加**最小隔离**：受限 env（仅注入必要变量）、可选 `seccomp`/沙箱占位、网络出口限制（计划书 P3-29/30 前移）。

**P2 — 中期**
7. 启动 Phase 3 的语音/PWA/安全加固，或正式将其从"Phase 3"重排为后续独立阶段，避免"Phase 3 完成"被误读。
8. 将 `extensions/`、`web/pwa/` 中的空目录标注"TODO/未实现"或删除；迁出根目录的邮件脚本。
9. 固化 1–2 个 **E2E 测试**（loopback 双节点全链路），纳入 CI，防止回归。

**P3 — 长期**
10. 建立 `docs/` 报告的**完成度分级标准**（如：核心逻辑 ✅ / 硬件联调 ⏳ / 安全加固 ⬜），杜绝"全完成"式标题。

---

## 九、结论

PANDA 是一个**工程底子扎实、架构清晰、测试通过、代码可读性高**的分布式任务编排项目，其 Phase 0/1/2 的通信与委派核心、以及 Phase 3 的记忆与 Skill 子系统均已**真实且高质量地落地**，值得肯定。

但**项目目前的"完成度叙事"超前于代码**：计划书中 Phase 2 的防御链、熔断器、合并门禁、GPIO 硬件扩展，以及 Phase 3 的语音入口、PWA、安全加固**在代码中并未真正实现**（相关目录多为空占位——`extensions/` 仅含 `dreamer/`、`gpio/`、`voice/` 空子目录，`web/pwa/` 为空，`internal/security/` 为空目录，`internal/defense/` 仅有 `permission.go` 确定性门禁），而 `docs/` 的若干"完成报告"仍以"✅ 完成"为标题。这一偏差若不修正，将误导后续 Phase 的决策门判断，并在缺乏防御/隔离的前提下放大跨设备自动执行的安全风险。

**建议**：先修正文档口径、补上 `mustUUID` 的 panic 修复与 Tier-2 绕过加固（P0），再按 P1 补齐熔断器和合并门禁，使"完成度"与代码重新对齐，之后再推进语音/安全/PWA。

---

*本报告由 workbuddy hy3 生成，仅作静态分析，未对任何源代码作出修改。所有 `文件:行号` 引用均基于 2026-08-14 仓库快照。*
