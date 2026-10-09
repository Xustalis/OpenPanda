# 不可恢复操作审批：现状排查与后续建议

> 日期：2026-09-09  
> 状态：排查记录，尚未实施  
> 范围：调度启动前的操作审批策略、代理适配器边界、恢复证据与审批展示

## 1. 目标语义

OpenPanda 的审批边界应由“是否不可恢复”决定，而不是由“是否使用编码代理”或“是否有副作用”决定。

- 读取、搜索、检查、状态查询等只读操作不审批。
- 普通本地修改、普通 `git push`、发送消息、创建远端资源和可回滚部署，不因存在副作用而自动审批。
- 已确认不可逆且不可恢复的操作需要审批。
- 删除或覆盖只有在执行前验证真实恢复路径后才可自动执行。
- 破坏性意图明确、但恢复路径无法验证时，需要审批。
- 破坏性操作无法可靠分类或载荷不透明时，保持 fail-closed。

这里的“可恢复”必须是可验证事实，而不是根据操作名称、工作目录属于 Git 仓库或存在一个提交哈希进行推测。

## 2. 已确认的直接根因

当前“普通查看也要求审批”的直接原因是编码代理被静态标成 Tier 2，而 commander 在启动代理前按整个 agent plan 的 tier 进行一次总授权。

### 2.1 编码代理默认被写成 Tier 2

`internal/agents/registry.go` 中已知编码代理的 `DefaultTier` 均为 2，包括 Claude Code、Codex、OpenCode、Grok Build、DeepSeek Harness、OpenClaw 和 Hermes。

这些默认值会继续从多个入口写入 capability card：

- `internal/carddetect/carddetect.go`：自动检测复制 `Known.DefaultTier`，零值仍回退到 2。
- `internal/askengine/mgmttools.go`：`card_agent_add` 默认 tier 为 2。
- `cmd/panda/cardedit.go`：`panda card agent add` 默认 tier 为 2。
- `cmd/panda/repl_card.go`：`/card agent add` 默认 tier 为 2。

因此，仅重新检测或重新添加代理不会消除误审批。

### 2.2 授权发生在代理启动前，粒度是整个任务

`internal/commander/commander.go` 在 agent plan 执行前调用 `defense.Authorize(plan.Tier, authorized)`。显式 Tier 2 会在 adapter 启动前拒绝整个任务，代理尚未读取文件或生成任何具体命令。

这意味着只读任务与潜在破坏性任务共享同一个静态审批结果。当前门禁无法知道代理随后会执行 `Read`、普通编辑，还是不可恢复命令。

未声明的 agent tier 在 commander 中已经回退到 Tier 1；问题主要来自生成并持久化的显式 Tier 2。

### 2.3 `TaskSpec.Risk` 不是此次误审批的判定源

任务的 `Risk` 会被持久化和展示，但当前授权门使用执行 plan 的 tier。修改风险文本本身不能修复只读任务误审批。

## 3. 已有命令分类能力

`internal/defense/permission.go` 的 `TierFromCommand` 已比“所有副作用都审批”更接近目标语义：

- 普通 build、install、restart、远程执行和普通 `git push` 通常属于 Tier 1。
- force push、破坏性 Git reset/clean、删除、覆盖、磁盘格式化、分区/固件、电源状态操作等属于 Tier 2。
- `find -delete`、`sed -i`、`rsync --delete` 等参数敏感形式会被识别。
- wrapper、解释器载荷和部分嵌套命令会被递归检查。
- 不透明编码载荷与无法安全判断的破坏性解释器载荷保持保守处理。

但该函数只接收命令及参数，没有具体目标对应的恢复证据，因此无法表达“这次删除虽然语法上具有破坏性，但目标内容已被可靠备份并验证”。下载到文件等不透明写入形式目前也可能比产品目标更保守。

## 4. 现有机制不能证明可恢复

### 4.1 Scope snapshot 只能发现变化

`internal/defense/snapshot.go` 保存普通文件的 size 和 mtime，用于执行后发现新增、删除或修改。它不保存文件内容，不能恢复被删除或覆盖的数据，也不能在操作前证明恢复路径。

`internal/defense/scope.go` 判断变化是否位于声明范围内。“在范围内”不等于“可恢复”。

### 4.2 Git 元数据不能保护所有本地工作

`internal/commander/context.go` 的 `FileContext` 保存 repo、branch、commit、scope 和环境元数据，但不保存 dirty tracked 内容或 untracked 文件。

因此，仅有提交哈希不足以证明某个删除、覆盖、reset 或 clean 可恢复。只有确认目标内容已经进入可读取的 Git object，且不存在会丢失的未提交或未跟踪内容时，Git 才可能成为有效恢复证据。

### 4.3 Context store 不是回滚仓库

`internal/ctxstore/store.go` 是可淘汰的内容寻址上下文缓存，服务于任务上下文传输。它没有面向恢复证据的固定保留、引用保护和恢复验证语义，不能直接作为持久回滚存储。

### 4.4 当前 sandbox 不是隔离边界

`internal/security/sandbox.go` 只设置工作目录并收缩环境变量。它不限制文件系统访问、网络、系统调用或资源使用，不能作为“Tier 1 agent 无法执行破坏性操作”的依据。

### 4.5 Artifact 能力可供设计参考，但尚非恢复证据系统

`internal/artifact/` 与 `internal/core/artifact.go` 已提供内容寻址、流式哈希验证和本地 artifact pool；`internal/core/project.go` 也会打包项目目录用于跨节点传输。`internal/install/install.go` 还有面向卸载目标的 `BackupZip` 模式。

这些代码证明仓库已有真实内容备份与校验的基础组件，但当前 artifact 生命周期、保留策略、目标绑定和恢复演练并未定义为操作前回滚保证，不能直接声称已有通用恢复能力。

## 5. 代理适配器的真实边界

当前 adapter progress 是单向事件流，并且命令或文件变化事件通常在操作发生后才出现。执行后观察不能充当执行前授权门。

- `internal/commander/adapter.go` 的请求包含 prompt、timeout、cwd、resume 和 tools policy；`ProgressFunc` 只能上报 note/kind，没有同步 request/decision 协议。
- `adapters/_harness.py` 接收一次请求并输出最终结果及 stderr progress，不具备通用的逐操作仲裁握手。
- `adapters/claude_code.py` 当前使用 `--permission-mode acceptEdits`。Claude Code 暴露的 host permission、permission prompt tool 和 hook 参数是候选集成入口，但本仓库尚未接入和验证。
- `adapters/codex.py` 当前设置 `approval_policy="never"`；Codex 的 sandbox、approval、execpolicy 和 app-server 是候选入口，但当前实现没有安装任务级操作策略。
- Hermes 等其他 adapter 也不能默认视为具备安全的操作前仲裁。

所以，只把所有编码代理默认改成 Tier 1 虽然会消除只读误审批，却会让代理内部真正不可恢复的操作失去现有的启动前保护。默认 tier 调整必须与操作前仲裁或等价约束一起设计。

## 6. 建议的决策模型

后续实现应形成类型化的 operation decision，至少包含：

- operation：规范化操作类型。
- target：具体文件、目录、引用、资源或远端目标。
- effect：read、write、delete、overwrite、external 等效果。
- reversibility：reversible、irreversible、unknown。
- evidence：恢复证据种类、标识、覆盖目标和保留状态。
- evidence_verified：是否在执行前完成验证。
- decision：allow 或 require_approval。
- reason：明确的审批原因。

审批原因至少区分：

1. `confirmed_irreversible`：操作已确认不可逆且无恢复路径。
2. `recovery_unverified`：操作会删除或覆盖，但恢复证据不存在、覆盖不完整或验证失败。
3. `opaque_unclassifiable`：载荷不透明，无法可靠排除不可恢复效果。

恢复证据必须绑定具体 operation 与 target。可接受证据可以包括已验证的 Git 对象、内容快照、备份 archive 或事务回滚点，但都需要验证目标覆盖范围、可读取性、保留期限和恢复方法。

## 7. 适配器兼容策略

- 支持同步操作前仲裁的 adapter：普通操作直接执行；遇到需审批操作时暂停并把类型化 decision 交给现有审批/恢复生命周期。
- 能证明任务只读的 adapter 或受限模式：只读任务不审批。
- 不支持仲裁且允许任意修改的 adapter：不能宣称内部操作受到不可恢复审批保护；可能修改但无法观察的任务应保守处理。
- 未知或不透明的破坏性载荷保持 fail-closed，但不能把所有普通读取重新归入 Tier 2。

现有 typed approval、同任务恢复、并发 CAS、取消传播和 TUI 同卡片生命周期应被复用，不应另建第二套审批任务流。

## 8. Card 默认与迁移建议

实施时需要统一 registry、自动检测、管理工具、CLI、REPL、注释、示例和 i18n 的默认语义，避免不同入口再次生成冲突配置。

既有 capability card 的迁移必须区分“旧版本自动生成的 Tier 2”与“用户有意设置的 Tier 2”。没有可靠来源信息时，不应静默覆盖用户策略；更安全的方式是增加来源/策略版本元数据，或提供显式、可预览的迁移命令。

## 9. 审批展示要求

TUI、CLI 和 Web 不应只显示笼统的“Tier 2”或 agent 名称。审批信息应至少展示：

- 将执行的 operation。
- 具体 target。
- 为什么判定不可恢复或为什么无法验证。
- 已找到的恢复证据及验证状态。
- 批准后的实际执行语义。

Web 当前的 `tier2OpJSON` 只有 op、target、risk；需要扩展证据和 reason。TUI 审批卡也应显示相同的类型化信息，避免各入口解释不一致。

## 10. 建议实施顺序

1. 统一 agent 默认 tier 的生成语义，并设计不会覆盖用户显式策略的 card 迁移。
2. 引入共享的 typed operation decision 与审批原因。
3. 为本地删除/覆盖建立目标级恢复证据、验证与保留机制。
4. 分别接入 Claude Code、Codex 的同步操作前仲裁；为其他 adapter 声明能力并设置保守 fallback。
5. 将 decision/evidence 写入任务事件，并统一 TUI、CLI、Web 展示。
6. 复用现有审批恢复链路执行被批准的原任务，不创建替代任务。

## 11. 验收场景

- 读取文件、搜索代码、查看状态：不审批。
- 普通编辑、普通 `git push`、发送消息、创建资源、可回滚部署：按策略直接执行。
- force push、磁盘格式化或确认无恢复路径的不可逆操作：执行前审批。
- 删除或覆盖且没有恢复证据：执行前审批，原因是 `recovery_unverified`。
- 删除或覆盖且目标已有经过验证、仍被保留的恢复证据：自动执行，并记录证据。
- 不透明破坏性载荷：执行前审批，原因是 `opaque_unclassifiable`。
- 审批后仍沿原 task、原 workdir、原 live card 继续执行，且可取消、不会双跑。

## 12. 关键文件索引

- 权限分类：`internal/defense/permission.go`
- agent 路由与授权门：`internal/commander/commander.go`
- agent 默认注册表：`internal/agents/registry.go`
- card 生成入口：`internal/carddetect/carddetect.go`、`internal/askengine/mgmttools.go`、`cmd/panda/cardedit.go`、`cmd/panda/repl_card.go`
- scope 元数据：`internal/defense/snapshot.go`、`internal/defense/scope.go`
- adapter 协议：`internal/commander/adapter.go`、`adapters/_harness.py`
- adapter 实现：`adapters/claude_code.py`、`adapters/codex.py`
- 上下文与 artifact：`internal/commander/context.go`、`internal/ctxstore/store.go`、`internal/artifact/`、`internal/core/artifact.go`
- 当前审批事件与界面：`internal/core/handlers.go`、`cmd/panda/tui_view.go`、`webui/panel/panel.go`
