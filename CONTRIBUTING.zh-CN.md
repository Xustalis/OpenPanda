# 为 OpenPanda 贡献代码

感谢你对改进 OpenPanda 的兴趣。本文档涵盖工具链、每项改动必须通过的工程门槛，以及让代码库随规模增长仍保持可读性的约定。

## 环境要求

| 工具    | 版本要求 | 用途                          |
| ------- | -------- | ----------------------------- |
| Go      | ≥ 1.22   | 内核、CLI、控制台、测试       |
| Node.js | ≥ 18     | Web 控制台（`webui/app`）     |
| Python  | ≥ 3.10   | 语音 sidecar、Agent 适配器    |

## 快速开始

```bash
git clone https://github.com/xenith/openpanda
cd openpanda
make run            # 使用示例配置启动守护进程
make web            # 把控制台构建到 webui/panel/dist（go:embed）
make build-webui    # 构建独立面板 sidecar（控制台嵌入其中）
```

守护进程在构建时嵌入 Web 控制台。若不执行 `make web`，会嵌入一个占位页面，这样 `go build` 无需 Node 也能运行——**提交 UI 改动前务必先 `make web`**。

体验交互入口：

```bash
panda repl          # 斜杠命令：/tasks /approve /projects /nodes /web …
```

## 工程门槛

只有**全部**门槛通过的 PR 才会被合入。推送前请在本地执行：

```bash
make gate           # build + vet + test + race（合入门槛）
gofmt -l internal/ cmd/ adapters/ webui/panel/   # 必须无输出
cd webui/app && npm run typecheck                # 改动 Web 控制台时
```

- **`go test -race ./...` 必须通过**——内核是并发系统（peer 注册、任务存储、SSE 分发）；竞态检测器报出的任何问题都是阻塞性问题，不是警告。
- 核心模块（`internal/core`、`internal/scheduler`、`internal/storage`）在可行范围内测试覆盖率应保持 **~60% 以上**。Bug 修复请附带回归测试：修复前该测试失败，修复后通过。
- 新增线协议或委派行为必须提供回环测试——参考 `internal/core/dedup_test.go` 与 `scripts/smoke-delegate` 的模式。

## 代码规范

- **错误处理**：用 `%w` 包装，用 `errors.Is/As` 判断。绝不静默丢弃错误；同一函数内**不能**既记录错误日志又把该错误返回（二选一）。
- **注释写"为什么"，不写"做什么"**。六个月后的读者需要的是不变量、取舍权衡或问题来源——而不是对代码的复述。不明显的并发决策（锁顺序、为什么在锁外 close 等）必须在现场写明。
- **不留死代码，不做投机性抽象**。三行相似代码胜过过早抽象的接口。删除不用的代码，不要注释掉放在那里。
- **并发**：每个互斥锁写明它保护什么。在 I/O 或 channel 发送期间不持有锁。发送方负责关闭；goroutine 的所有者可以从它的 spawn 位置唯一识别。
- **安全**：默认关闭（fail closed）。任何做分类、授权或脱敏的逻辑，默认必须走限制分支（详见 `internal/defense` 的 Tier 模型）。新增的"含秘钥"配置项必须有 0600 chmod + 告警处理；绝不记录秘密信息到日志。

## 提交信息风格

采用与仓库历史一致的 Conventional Commits：

```
feat(cli): 交互式 REPL — 斜杠命令、/web 控制台、五语言 i18n
fix(core): 确定性互拨去重 — 消除 1s 断连/重连抖动
feat(web): 完整控制台 — 队列/详情/提问/项目/节点 + go:embed 单二进制
```

使用 `feat` / `fix` / `docs` / `refactor` / `chore` / `test` 作为类型，scope 来自顶层布局（`core`、`cli`、`web`、`scheduler`、`defense` …）。主题行用祈使语气，具体到能在 `git log --oneline` 中看懂。

## Web 控制台（webui/app）

- 技术栈：Vite + Preact + TypeScript，运行时只依赖 Preact。
- **所有用户可见字符串必须走 i18n**。新增键时同步加到 `webui/app/src/i18n/` 每个语言文件（英文是兜底；缺键会落到英文，因此绝不能"先加英文再翻译"就提交）。
- CLI 同样：`internal/i18n/messages.go`。
- 新增语言：复制英文映射表→翻译→在 `internal/i18n/i18n.go` 与 `webui/app/src/i18n/index.ts` 同时注册→在 README 中加链接。保持键名可 grep —— 键就是唯一标识。

## 发起 Pull Request

1. Fork 仓库，从 `main` 切分支（`feat/…`、`fix/…`）。
2. 保持 PR 小而单一：一个功能与其引发的重构应分两个 PR。
3. 更新 `CHANGELOG.md` 的 `[Unreleased]` 段——按 Added / Changed / Fixed 整理。
4. 五语言 README 的更新：只有当你改动了用户可见的 CLI 行为，或在功能列表里新增条目时才需要；把你写的段落翻译成另外四语言是加分项，但不阻塞合入——维护者会同步翻译。
5. `make gate` 绿灯 → 提交 PR → Review。

## 报告安全问题

传输鉴权、Tier 模型或脱敏层存在漏洞时，**不要**在公开 Issue 中披露。请通过仓库设置中的安全联系人，把复现步骤私下发送给维护者。审计链（`panda audit verify`）正是为此设计：修复后可以验证改动没被篡改。

## 许可

提交代码即视为同意你的贡献以项目的 [MIT License](LICENSE) 发布。
