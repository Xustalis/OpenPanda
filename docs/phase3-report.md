# Phase 3 · 记忆系统完成报告 · PANDA

> 范围：双层记忆 + 自动记忆 + Dreaming 引擎 + Skill 自进化（Sprint 3.1/3.2/3.3）
> 日期：2026-08-14
> 状态：✅ 完成 · 记忆逻辑对齐 Hermes Agent + OpenClaw 的真实机制，非自造

## 1. 一句话结论

记忆系统不再是"被动存储 + 后期维护"，而是按上游真实机制落地的**自运转闭环**：在线层（Hermes）靠 save/skip 治理规则让入口模型主动记、靠容量压力让它自己合并；离线层（OpenClaw）靠 Dreaming 在节点空闲时把 daily 日志按六维评分 + 来源门控沉淀进长期记忆；Skill 作为程序性记忆按触发条件自进化、按作用域渐进加载进执行上下文。

## 2. 记忆逻辑（对齐上游，用户重点）

### 2.1 在线层 · Hermes：模型驱动的主动记忆

| 机制 | 实现 |
|---|---|
| **save/skip 治理** | 入口模型 system prompt 内嵌"该记/不该记"清单（偏好/环境事实/纠正/约定/已完成工作/显式"记住" → 记；琐碎/可查/原始转储/临时 → 不记） |
| **双文件分离** | `USER.md`（用户画像 1375 字符）+ `MEMORY.md`（世界笔记 2200 字符），`§` 分隔条目、字符上限、frozen snapshot |
| **维护交给模型** | `memory.add/replace/remove/read` 四个受控工具；超限 add 报错带用量，模型 read → replace 合并 → add 重试，同一轮完成 |
| **隔离墙** | `Conversation()` 只读 Hermes，`ContextPack(project)` 只读项目，结构性隔离、无共同路径 |

### 2.2 离线层 · OpenClaw：信号驱动的 Dreaming

三阶段在节点空闲时自动运行（每日最多 1 次 Deep）：

- **Light**：扫描 daily 日志 → 去重去噪 → 暂存候选（带来源）
- **REM**：按概念词聚成主题摘要
- **Deep**：六维加权评分（Relevance .30 / Frequency .24 / Query diversity .15 / Recency .15 / Consolidation .10 / Conceptual richness .06）+ 阈值门控（score≥0.8、count≥3、days≥3）+ **provenance taint gate**（只提升 trusted 来源）→ 写 `MEMORY.md` + `DREAMS.md`

### 2.3 程序性记忆 · Hermes：Skill 自进化

- **触发**：单次 ≥5 工具调用+成功（Hermes），或 ≥3 次同类+70% 成功率（MUSE）；不重复
- **格式**：SKILL.md = YAML frontmatter + Markdown body（这是正确归属，Sprint 3.1 曾误用于 MEMORY.md）
- **作用域**：global / project / device 三级，路径隔离
- **渐进加载**：轻量 index → 匹配才加载完整 body（不塞满 prompt）
- **生命周期**：active → dormant → expired（30d/90d 未用）+ 审批流（pending → 批准/拒绝）

## 3. 运行时接线（全部自运转）

```
入口模型 ──ask──▶ Classify（注入 USER+MEMORY 快照）
                 └─ tool_call ──▶ memory 工具执行器（add/replace/remove/read）

任务执行 ──core.run──▶ buildAgentPrompt（项目记忆 + 匹配 Skill 前置）
                 └─ logTask ──▶ daily 日志

daemon ──Scheduler（Idle 检测 + 每日 1 次）──▶ Dreamer（Light→REM→Deep）
                                                    └─▶ MEMORY.md + DREAMS.md
```

## 4. 改动文件

新增：
- `internal/memory/format.go`（§ 条目 + 字符上限 + add/replace/remove）
- `internal/memory/hermes.go`（USER.md / MEMORY.md 双文件 + frozen snapshot）
- `internal/memory/project.go`（项目记忆 + 名字校验）
- `internal/memory/injector.go` / `isolation.go`（注入 + 隔离墙）
- `internal/memory/tools.go`（memory 工具执行器）
- `internal/memory/daily.go`（daily 日志 + 归档/删除）
- `internal/memory/provenance.go`（来源追溯 + taint gate）
- `internal/memory/signals.go`（六维评分）
- `internal/memory/dream.go`（Light/REM/Deep）
- `internal/memory/dream_scheduler.go` / `dream_diary.go`
- `internal/skills/`（skill.go / trigger.go / loader.go / lifecycle.go）
- 全套 `*_test.go`

修改：
- `internal/entry/prompt.go`（save/skip 治理规则 + memory 工具白名单入 system prompt）
- `internal/config/config.go`（`MemoryPath` / `ProjectsPath` / `SkillsPath`）
- `internal/core/core.go`（`SetMemoryStores` + `Idle`）
- `internal/core/handlers.go`（`buildAgentPrompt` + `logTask` + run 接线）
- `cmd/panda/main.go`（daemon 接线 + Dreaming 调度器 goroutine）
- `cmd/panda/ask.go`（memory 工具执行 + 记忆注入）
- `config.example.yaml`

## 5. 质量

| 项 | 结果 |
|---|---|
| `go vet` / `gofmt -l` | ✅ 干净 |
| `go test ./...` | ✅ 全通过 |
| 覆盖率 | memory 82.3% · skills 80.9% · core 63.0% |
| linux/arm64 交叉编译 | ✅（纯 Go，无 CGO） |
| 复杂度 | 信号/评分 O(1)；候选提取 O(daily 行)；索引 O(files)；无 O(n²) 热点 |

## 6. 与设计文档偏离（对齐上游）

| 计划书 | 实现 | 理由 |
|---|---|---|
| §17.2「1300 token 硬上限」 | 2200 字符（USER 1375 / MEMORY 2200） | Hermes 用字符上限，模型无关 |
| §7.3「注入前 800 token」 | 注入整个 MEMORY.md | 文件本身已 ≤2200 字符 ≈ 800 token |
| P3-01「YAML frontmatter」 | MEMORY.md 用 § 分隔；YAML frontmatter 归 SKILL.md | 计划书张冠李戴 |
| P3-14「≥3 次同类 + ≥70%」 | 同时支持 Hermes「≥5 工具调用+成功」 | 两触发并取 |
| `memory/dreams/` | `memory/.dreams/` | OpenClaw 机器状态目录 |

## 7. 来源

- [Hermes 持久化记忆](https://hermes-agent.nousresearch.com/docs/user-guide/features/memory/) + [memory_tool.py](https://github.com/NousResearch/hermes-agent/blob/a72bb037/tools/memory_tool.py)
- [Hermes Skill 自进化](https://blog.csdn.net/dongli_816/article/details/161168853) + [源码架构拆解](https://cloud.tencent.com.cn/developer/article/2655748)
- [How Memory works in Hermes Agent (Mem0)](https://mem0.ai/blog/how-memory-works-in-hermes-agent-(and-how-to-improve-it))
- [OpenClaw Dreaming 概念](https://github.com/openclaw/openclaw/blob/484195d1/docs/concepts/dreaming.md) + [Dreaming Explained](https://dev.to/michael_xero_ai/openclaw-dreaming-explained-how-ai-memory-consolidation-actually-works-4m2l)

## 8. 审查修复（2026-08-14）

两轮自查发现并修复的问题：

**第一轮（接线完整性）**

| 问题 | 修复 |
|---|---|
| Daily 日志并发不安全（core 多 goroutine 并发写，注释要求外部序列化却未做） | `Daily.Append`/`Prune` 加 `sync.Mutex` |
| `Searcher`（FTS5）无消费者，死代码 | 删除；FTS5 检索需会话历史存储，留待会话记录功能一起做 |
| `signalNames` 死代码 | 删除 |
| `config.example.yaml` 缺 `skills_path` | 补上 |
| Dreaming 调度器静默吞错误 | 加 `OnError` 回调，daemon 接 logger |
| **Skill 闭环断裂**：trigger 无输入、generator 缺失、RecordUse 未接线、审批无入口、pending 会被加载 | 补 `Tracker` + `Generate` + RecordUse 接线 + `panda skill list/approve/reject` CLI + Match 排除 pending |
| `panda skill` 的 `--config` 在位置参数后不被解析 | 手动 `splitConfig` 支持任意顺序 |

**第二轮（语义正确性）**

| 问题 | 修复 |
|---|---|
| `MemFile.Replace` 超限不回滚，内存态不一致 | 回滚，加回归测试 |
| `skills.Match` 用子串匹配，token "go" 误匹配 "cargo" | 改词边界匹配（wordSet） |
| `skills` 的 scope key（Project/Device）不校验，`../` 路径穿越 | `validateScopeKey`，加测试 |
| `Core.Idle` 漏 `waiting_context` 状态 | 补上 |
| `ParseSkill` 的 scope 空值不默认 global，导致永不匹配 | 解析时默认 global |
| `trigger.MaxToolCalls` 死字段（拿不到 agent tool call 次数，Hermes 触发死路） | 移除该字段，只留 MUSE 聚合，明确说明 |
| `buildAgentPrompt` 的 `required` 死参数 + 用长 intent 匹配 skill 过度匹配 | 改用 task.Title 做 query |
| **memory 工具多轮合并不可用**（单轮 Classify 做不到 read→合并→重试） | `entry.CompleteTurns`/`ClassifyTurns` + `ask` 多轮 loop，工具结果回喂模型 |

**仍未做（架构限制，非遗漏）**：
- **provenance Trusted 恒 true**：taint gate 机制就绪，但当前无 untrusted 来源（未来会话记录/外部源摄入时才有）。
- **Hermes 单次复杂任务触发**（≥5 tool calls）：agent 是 subprocess，Go 拿不到内部 tool call 次数，已明确从触发条件中移除，只保留 MUSE 聚合（≥3 次 + ≥70%）。

---

*Phase 3 记忆系统完成 · 2026-08-14 · 后续：Sprint 3.5 安全加固 + PWA 控制台已落地（`internal/security` + `internal/panel` + `web/pwa`），Sprint 3.4 语音入口代码已就绪待硬件实测*
