# Phase 3 · Sprint 3.1 完成报告 · PANDA

> 阶段：Phase 3 · 双层记忆（Hermes + 项目记忆）
> 日期：2026-08-13
> 状态：✅ 完成 · 已对齐 Hermes / OpenClaw 的真实记忆模型

## 1. 交付物概览

Phase 3 从「双层记忆」切入（设计 §17）。本 Sprint 完成 P3-01 至 P3-07，建立 Hermes 个人记忆与项目记忆的存储层、格式、注入、隔离墙与 daily 日志，并接进入口模型和 agent 执行上下文。

**实现方式对齐上游而非重造**：MEMORY.md 格式、热层上限、读写模型均照 Hermes Agent（Nous Research）的真实实现；冷层目录照 OpenClaw。见 §9。

| ID | 任务 | 产出 | 状态 |
|---|---|---|---|
| P3-01 | MEMORY.md 格式 | `internal/memory/format.go`：`§` 分隔条目 + 字符上限 + add/replace/remove | ✅ |
| P3-02 | Hermes 记忆管理器 | `internal/memory/hermes.go`：热/温/冷三层路径，Load（frozen snapshot）/Save + 2200 字符上限 | ✅ |
| P3-03 | 项目记忆管理器 | `internal/memory/project.go`：`projects/{name}/MEMORY.md` + 名字校验防路径穿越 | ✅ |
| P3-04 | 语义检索 | ~~`internal/memory/search.go`（FTS5 冷存储检索 + BM25 + snippet）~~ | ⚠️ 已撤回（死代码，见 phase3-report §8） |
| P3-05 | 记忆注入器 | `internal/memory/injector.go`：`Conversation()`（Hermes 全量快照） | ✅ |
| P3-06 | 上下文隔离墙 | `internal/memory/isolation.go`：`ContextPack()`（仅项目，绝不 Hermes） | ✅ |
| P3-07 | daily 日志 | `internal/memory/daily.go`：追加 + 30d 归档 / 90d 删除 | ✅ |

## 2. 双层记忆，物理隔离（设计 §17.2）

```
memory/                    Hermes 个人记忆
  MEMORY.md                热层（§ 分隔条目，2200 字符 ≈ 800 token，冻结快照）
  daily/YYYY-MM-DD.md      温层（30d 归档 → 90d 删除）
  .dreams/                 冷层（Dreaming 状态，OpenClaw 点目录，Sprint 3.2）
  DREAMS.md                梦境日志（Sprint 3.2）
projects/{name}/MEMORY.md  项目记忆（仅注入同类项目任务）
```

隔离墙是**结构性**的：`Injector.Conversation()` 只读 Hermes，`Injector.ContextPack(project)` 只读项目，二者没有共同代码路径，按构造就无法互相泄漏。`internal/core/memory_test.go` 用「HERMES-SECRET / PROJECT-MEM」互斥标记在 core 层端到端验证。

## 3. MEMORY.md 格式（P3-01，对齐 Hermes）

**之前按「YAML frontmatter + Markdown body」实现是错的**——那是 SKILL.md 的格式（Harness/agentskills.io，属 Sprint 3.3），不是 Hermes 的 MEMORY.md。Hermes 实际是：

- **`§`（U+00A7）分隔的条目列表**，一条记忆一个条目，可多行。
- **字符上限而非 token 上限**（模型无关）：MEMORY.md 2200 字符（≈800 token）。PANDA 给项目记忆 8000 字符（注入完整 agent 上下文，给更多空间）。
- **add / replace / remove 三种操作**：`add` 拒绝空条目和精确重复；`replace`/`remove` 用**子串匹配**，匹配到多条则报错要求更具体的子串。
- **超限报错而非静默截断**：超限 `add`/`replace` 回滚并报 `ErrOverLimit`（带当前用量），由模型/调用方合并后重试——Hermes 的最佳实践是 >80% 时主动合并。
- **frozen snapshot**：`Load` 读取并应用上限后返回快照；会话内后续写入不改变已返回的快照（Hermes 借此保留前缀缓存）。

## 4. 语义检索（P3-04，对齐 Hermes 冷存储）

> **修订（2026-08-14）**：本 Sprint 交付的 `internal/memory/search.go`（FTS5）因无消费者（FTS5 检索需会话历史存储）已作为死代码删除，见 [phase3-report.md](./phase3-report.md) §8。语义检索整体延后至会话记录功能一起做。以下为当时的实现描述，仅供追溯。

Hermes 的 FTS5 **不是**给 MEMORY.md 建索引——热层足够小、整体注入即可。FTS5 是**冷存储检索**：索引 daily 日志 / 会话摘要，按需关键字查询（BM25 排序 + snippet），关键字匹配、无语义向量。这正是 Hermes 文档标注的已知局限；嵌入向量相似度留到有嵌入 provider 时再接（DeepSeek 的 Anthropic 端点无 embeddings 接口，见 [[model-provider-deepseek]]）。

## 5. 注入与集成

- **Hermes → 入口模型**：`cmd/panda/ask.go` 注入 Hermes 快照到系统提示词「用户记忆」段。MEMORY.md 已被字符上限约束（≈800 token），无需再做 800-token 截断。
- **项目记忆 → agent 执行**：`core.SetProjectMemory(injector)` 后，`core.run` 用 `withProjectMemory` 把项目 MEMORY.md 前置到 agent intent。daemon 与 `ask` 都接线，两层记忆真正进入运行路径。

## 6. 测试统计

| 包 | 覆盖率 | 关键测试 |
|---|---|---|
| `internal/memory` | 84.3% | § 解析/序列化往返 / add 去重+上限回滚 / replace/remove 子串匹配+歧义拒绝 / 名字校验防穿越 / FTS5 排序+upsert+净化 / daily 归档删除 / 隔离墙双向不泄漏 |
| `internal/core` | 62.8% | 新增 `TestWithProjectMemory`（隔离墙在 core 层端到端） |
| `go vet` / `gofmt -l` | ✅ 无告警 / 干净 | 全仓 `go test ./...` 通过 |

## 7. 与设计文档的偏离（已对齐上游）

| 设计文档 | 本实现 | 理由 |
|---|---|---|
| §17.2「1300 token 硬上限」 | **2200 字符**（≈800 token） | Hermes 真实数字是字符上限（2200），模型无关；「1300 token」是计划书的粗略目标。采用上游精确值 |
| §7.3「仅注入前 800 token」 | 注入整个 MEMORY.md | Hermes 的做法：MEMORY.md 本身已 ≤2200 字符 ≈ 800 token，整体作为 frozen snapshot 注入，无二次截断 |
| P3-01「YAML frontmatter + Markdown body」 | **`§` 分隔条目** | YAML frontmatter 是 SKILL.md 格式（Sprint 3.3），不是 MEMORY.md 格式，计划书张冠李戴 |
| `memory/dreams/` | **`memory/.dreams/`**（点目录） | OpenClaw 的机器状态目录就是 `.dreams/` |

## 8. 改动文件清单

新增：
- `internal/memory/format.go` + `format_test.go`（`§` 条目 + 字符上限 + add/replace/remove）
- `internal/memory/hermes.go` + `hermes_test.go`
- `internal/memory/project.go` + `project_test.go`
- ~~`internal/memory/search.go` + `search_test.go`（FTS5 冷存储检索）~~ ⚠️ 已撤回（死代码删除）
- `internal/memory/injector.go` + `injector_test.go`
- `internal/memory/isolation.go`
- `internal/memory/daily.go` + `daily_test.go`
- `internal/core/memory_test.go`

修改：
- `internal/config/config.go`：`StorageConfig.MemoryPath` / `ProjectsPath` + 环境变量覆盖
- `internal/core/core.go`：`Core.memory` 字段 + `SetProjectMemory`
- `internal/core/handlers.go`：`run` 用 `withProjectMemory` 前置项目记忆
- `cmd/panda/main.go`：daemon 接线 `SetProjectMemory`
- `cmd/panda/ask.go`：Hermes 记忆注入入口模型 + 项目记忆接线
- `config.example.yaml`：`memory_path` / `projects_path`

## 9. 参考来源（对齐依据）

- [Hermes Agent 持久化记忆文档](https://hermes-agent.nousresearch.com/docs/user-guide/features/memory/)：MEMORY.md/USER.md、2200/1375 字符上限、`§` 分隔、frozen snapshot、add/replace/remove、超限合并策略
- [Hermes Agent `tools/memory_tool.py`](https://github.com/NousResearch/hermes-agent/blob/a72bb037/tools/memory_tool.py)：写入动作与子串匹配的源码实现
- [How Memory works in Hermes Agent (Mem0)](https://mem0.ai/blog/how-memory-works-in-hermes-agent-(and-how-to-improve-it))：FTS5 会话检索为关键字冷存储、无语义匹配的局限
- [OpenClaw Dreaming 概念文档](https://github.com/openclaw/openclaw/blob/484195d1/docs/concepts/dreaming.md)：Light/REM/Deep 三阶段、`memory/.dreams/` 机器状态、六维 Deep 评分权重（Relevance .30 / Frequency .24 / Query diversity .15 / Recency .15 / Consolidation .10 / Conceptual richness .06）

---

*Phase 3 Sprint 3.1 完成 · 2026-08-13 · 下一步：Sprint 3.2 Dreaming 引擎（Light/REM/Deep + 调度器 + DREAMS.md + provenance），继续照 OpenClaw 的 dreaming 实现*
