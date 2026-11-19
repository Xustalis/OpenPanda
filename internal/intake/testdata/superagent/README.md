# DeepSeek SuperAgent 原生吸收：主线进度与实施交接

**2026-11-18 决策：复用 Panda 的业务任务和执行内核，新增来源事件、授权和原串回执；不引入第二套任务系统。** 本轮完成调研和开工验证，没有把实验接到生产。第一项试点应是一个专用 GitHub 仓库里的一条显式请求，而不是监听所有事件自动接单。

## 项目做到哪里了

检查时 OpenPanda 本地主线与远端 HEAD 同为 `8b87a8ee73b42b9d859d385d11a3894ab8b89394`，已合入 #342。下表评估这个提交，不把工作区其他未提交改动或历史门禁结果算作本次发布证据。

| 用户关心的问题 | 主线已有 | 还缺什么 |
|---|---|---|
| 外部请求能否进入队列？ | [GitHub handler](../../server.go) 验签后应用仓库、操作者等配置；[Core.IntakeTask](../../../core/intake.go) 对有编号的投递使用持久请求。已验证同编号并发重投只排一项，发布失败后可恢复。 | 身份是一次投递，不是一条来源事实。相同评论经不同编号输入可以排两项。 |
| 同一个用户目标能否跨越多次执行？ | [TaskHub](../../../taskhub/store.go) 区分业务任务与执行，[本地恢复](../../../core/request_recovery.go) 处理已预留的请求。 | GitHub 入口直接创建执行；没有把讨论串绑定到同一业务任务。`StartLocal` 仅支持 `manual`、`rerun`，自动追问不能冒充人工重跑。 |
| 停止后会不会被旧消息唤醒？ | [taskflow](../../../taskflow/stopped_test.go) 有 TSync 专属暂停、授权和迟到回执测试，[远端准备](../../../taskflow/preparation.go) 有自己的执行边界。 | 没有可供 GitHub 在发布、每次实际执行及重启时重验的来源授权。收到已签名的消息不等于有权使用工具。 |
| 完成后能不能在原讨论串确认结果？ | [Web 工作详情](../../../../webui/app/src/views/workflow.tsx) 有步骤、验证、剩余项。已有 [TSync outbox](../../../taskflow/outbox.go)、[受限文件交付](../../../taskflow/finalaction/file.go) 和传递来源信息的 [hooks](../../../core/hooks.go)。 | 这些不能证明 GitHub 评论送达。目前没有内置的“发送结果未知 → 原串只读核对 → 远端评论 ID 或人工处理”的闭环；送达也不等于验收。 |

## 为什么原生吸收

上游仍是 [`8c32a9dc`](https://github.com/deepseek-ai/dsh-superagent-prototype/tree/8c32a9dc5d72c033816c6a8110b37f40cf94fba4)。[作者说明](https://github.com/deepseek-ai/dsh-superagent-prototype/blob/8c32a9dc5d72c033816c6a8110b37f40cf94fba4/README.md)将它定位为研究参考，并说明公开版与文章描述不完全相同。本次复核固定版本源码，没有运行上游服务或复制其实现。

- **采用**[领域模型](https://github.com/deepseek-ai/dsh-superagent-prototype/blob/8c32a9dc5d72c033816c6a8110b37f40cf94fba4/docs/001-domain.md)的分工：来源记录事实，讨论串负责归属，业务任务保存目的，单次执行可以终止或重建。保留现有 TaskHub、Core、adapter 的所有权，避免两边都认为自己可以重试。
- **收紧**[授权与恢复](https://github.com/deepseek-ai/dsh-superagent-prototype/blob/8c32a9dc5d72c033816c6a8110b37f40cf94fba4/docs/002-authorization.md)：人类追问可补上下文，但不自动恢复已停止的工作。来源身份、当前项目审批和允许的操作要分别验证；机器人、CI 和关联 PR 不能替人扩权。
- **借鉴但不照搬**[持久副作用](https://github.com/deepseek-ai/dsh-superagent-prototype/blob/8c32a9dc5d72c033816c6a8110b37f40cf94fba4/src/app/effects.ts)和[发送回执](https://github.com/deepseek-ai/dsh-superagent-prototype/blob/8c32a9dc5d72c033816c6a8110b37f40cf94fba4/src/app/outbox.ts)。事务提交后再调用外部服务。若评论可能已发出，不能把超时当成发送失败并再发一条。

把整个服务包进 adapter 会把长期责任和取消状态藏进另一套运行时；用 sidecar 则需额外解决谁能接单、谁能恢复。这两种成本对单仓库试点都没有必要。现有 DeepSeek Harness adapter 是执行器，与是否支持 GitHub 持续协作是两个问题。现有模型、端点和凭据不需要为此切换。

## 已复现的差异

主线对照使用真实 `ParseGitHub`、`Core.IntakeTask` 和临时数据库；输入从“已准入”处开始，没有启动队列执行器。新方案使用 Panda 自身的验签逻辑和文件型 SQLite WAL。实验表只存来源身份、规范化字段和正文摘要，不存正文或凭据。`pending_evaluations` 是待处理标记，**不是已评估、已授权或已执行**。

| 输入或故障 | 观察结果 |
|---|---|
| 主线：一条 `issue_comment.created`，依次输入 `d1, d1, d2` | 最终 2 个请求、2 个排队任务。GitHub 官方[重新投递会保留原编号](https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks#use-the-x-github-delivery-header)；不同编号是为多入口/补录设计的受控场景，不是声称 GitHub 普通重投会重复执行，也不是生产事故样本。 |
| 原型：同样的编号序列；同样文字但换评论 ID | 前者是 2 条投递、1 条事实、1 条待评估记录；后者是独立事实，不按正文或整个讨论串粗略合并。 |
| 同编号的原始字节变化，或同评论的正文、发送者、所属 Issue 变化 | 返回冲突，原证据不变，事务不留下孤立记录；不同编号但只有 Issue 标题变化则可以归入同一事实。尚未持久化冲突本身。 |
| 同一事实经 2 个数据库句柄、16 个并发调用、4 个投递编号写入 | 最终 4 条投递、1 条事实、1 条待评估记录。 |
| 待评估写入失败；关闭并重新打开数据库；评估标记已消费后再投递 | 失败事务三张表均无残留，重开可重试；已移除的待评估记录不会因重投被重新创建。这是事务故障和重开测试，不是进程强杀或断电测试。 |

## 从实验到单仓库试点

| 顺序 | 实现归属与接口 | 合并或启用条件 |
|---|---|---|
| 1. 仅观察（P0-A/B） | 将实验提升为 `internal/storage` 生产迁移和独立 intake 写入接口。来源由已配置的安装和仓库 ID 决定；一次事务写入事实、投递、待评估记录。版本化入口与旧入口只选其一。 | 默认关闭；升级/停用不影响旧任务。增加冲突留证、签名原文或可验证正文的最小保留策略、未消费记录重启扫描；演练新旧入口切换与历史投递，不双重入队。实验不为缺少安装 ID 的普通仓库 hook 猜身份。 |
| 2. 显式受理（P0-C/D） | 一个允许的来源操作者、一个项目。将业务任务绑定、授权依据和启动意图同事务写入；用稳定请求 ID 调 Core。发布、实际 attempt、恢复分别复查授权和审批版本。 | 同一事实只有一个业务任务、一个请求；当前审批不足、恢复校验缺失、授权撤销或人工停止均不执行。主动接单、P2P 转发保持关闭。 |
| 3. 原串交付（P0-E/F） | 根据持久执行结果生成固定目标、账号与正文的回复意图。出站状态区分待发、结果未知、已核对送达；工作详情可定位和处理人工待办。 | 在专用仓库验证真实评论：模拟远端成功后丢失本地回执，只读核对目标、作者、正文及远端 ID，不盲重发；证据不足需人工处理。停用、重启、回滚演练通过才启用。 |
| 4. 持续跟进（P1 以后） | 每条追问有稳定输入 ID；复用 Core 消费确认。结束后的续跑须新请求、重验权限；附带 PR/CI 只是上下文。 | 覆盖投递/消费/终止竞争及停止后旧消息到达；再评估主动受理的误判、漏判和模型成本。在此之前不扩展渠道或多节点。 |

## 重跑与边界

仓库根目录执行以下命令。基线脚本从指定 Git 对象生成临时源码树，不切分支或覆盖工作区。实验全部置于 `testdata`，必须显式指定路径；不会被应用编译或普通 `go test ./...` 自动执行。

```sh
sh internal/intake/testdata/superagent/check-baseline.sh
go test -race -count=10 ./internal/intake/testdata/superagent
go vet ./internal/intake/testdata/superagent
```

2026-11-18 使用 Go 1.26.11（darwin/arm64）。3 个主线对照测试通过；实验 5 个顶层测试（含 8 个来源边界子用例）在 race 下连续 10 轮通过。另在固定主线运行 `go test -race -count=1 ./internal/intake ./internal/taskhub ./internal/taskflow/... ./internal/taskproviders/...`，6 个包通过。历史 13 表 SQL 提案的 29 项内存检查也重新通过，但并非生产 migration 或上线验收。

未运行全仓发布门禁，未调用真实 GitHub 写接口、模型、远端 TSync 或多节点。没有改应用代码、生产 schema、服务配置或 webhook。当前决策是**可以开始仅观察阶段的产品开发；还不能启用自动执行或宣称结果已送回 GitHub**。
