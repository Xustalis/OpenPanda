# OpenPanda 增长诊断与 30 天执行计划

基线采集时间 2026-10-30。

## 一、基线数据

| 指标 | 数值 | 含义 |
|---|---|---|
| Star | 15 | 两个半月匀速增长约每周 1 个，没有出现过任何跳变 |
| Fork / Watcher | 4 / 0 | 没有一个订阅者 |
| 14 天浏览 | 383 次，20 个独立访客 | 平均每天 1.4 个真人 |
| 14 天 clone | 3699 次，362 独立 | clone 数远高于浏览数，签名是爬虫和镜像，不是用户 |
| 外部 referrer | 无，只有 github.com | 从来没有一个站外渠道链进来过 |
| Issue | 全部 14 个，全部由作者本人当天创建 | 读者看到的是待办清单，不是社区提问 |
| PR | 历史 113 个，外部提交实际来自两个同一控制人的账号 | 贡献者图谱只有一个人 |
| Discussions | 已开启，0 个主题 | 一个空房间 |
| Release | 19 个，最近 8 天发了 4 个 | 发版没有配套的对外公告 |
| License 识别 | Other | GitHub 没认出 AGPL，license 筛选直接搜不到 |

## 二、结论

问题不在代码。两个半月 780 次提交、19 个 release、五个语言的全套文档，工程强度远超 15 star 的项目该有的样子。

真实情况是两个互相独立的问题同时存在。

第一，没有人到达。外部 referrer 为零，意味着这个项目在 GitHub 之外从未被任何渠道展示过。Star 不来自 GitHub 搜索结果，来自某个人在 Hacker News、Reddit、X、某个 awesome list、某个 MCP 目录里看见它，然后点进来。这条链路目前一次都没有发生过。

第二，到达的人留下来的是零。383 次浏览换来 20 个独立访客，而这 20 个里大部分是作者自己。README 首屏是一段 60 词的抽象描述加一幅 ASCII 架构图，从头到尾没有一张产品截图。对一个终端工具来说，这等于让读者闭着眼睛判断。

只做渠道不做首屏，一次发布会的流量会全部漏掉，而且同一个项目很难有第二次像样的发布机会。所以顺序是先修转化，再制造到达。

## 三、第一优先：把到达的人留下（约一天工作量）

### 1. README 首屏重写

现在的第 17 行到第 48 行是 About 段落加 ASCII 图。读者在前 20 行里得不到"我周二会拿它干什么"的答案。

改成这个顺序：

```
# OpenPanda
一行副标题，带上搜索词：Run Claude Code, Codex and other agent CLIs
as one crew across your machines. Self-hosted, no cloud.
[15-20 秒 demo GIF]
三句话讲清楚：你有什么问题，它怎么解决，跟现有工具什么关系
一个 10 行以内的最小可跑示例
然后才是特性列表
```

标题里的 "Multi-Agent Orchestration OS" 建议去掉 OS。它不是操作系统，技术读者看到 OS 会先减一分信任。Description 里的 "kernel" 同理，换成更具体的名词。

特性的写法也要换。现在"每个 episode 的预算防止通道变成闲聊"这类句子是设计文档的语言，读者关心的是"四个 agent 同时干活不会互相踩脚"。每条特性改成一句用户视角的结果，括号里再给实现细节。

### 2. 补 demo 素材（这是最缺的一样东西）

全仓库没有任何 gif、mp4、webm、截图。对一个有 TUI、有 Web 控制台、有 P2P mesh 的项目来说，这是最大的转化损失。

最有说服力的两段演示：

- 多设备：一个任务在三台机器上跑，中途杀掉一台，任务在另一台上继续完成。
- 并行：一条指令扇出给四个不同 harness，收回来一个判定结果。

用 `charmbracelet/vhs` 录，因为它把终端会话写成脚本，产物可以复现，改 UI 之后重录一条命令就行。把 tape 文件放进 `docs/demo/`，README 引用生成的 GIF。

另外去仓库 Settings 上传 social preview 图，1280x630，带标题和一张真实界面截图。现在分享到 X 或 Discord 时显示的是默认灰卡片。

### 3. 加一张对比表

几乎每个长得快的开发工具都有一张。读者心里第一个问题是"为什么不直接用 Claude Code 或者 AgentFarm"。用一张表正面对上：单独用 agent CLI、用 cron 加 ssh、用托管型编排服务、用 OpenPanda，四列五行，讲清楚差在哪。回避这个问题，读者就自己脑补一个不利答案。

### 4. 修 license 识别

`.github` 之外的 `LICENSE` 文件开头插了 Dual Licensing Notice，GitHub 的识别器因此给了 Other。代价是 `license:agpl-3.0` 的搜索筛选永远搜不到，部分 awesome list 和企业扫描也会因为 Other 直接跳过。

改法是把 `LICENSE` 恢复成纯 AGPL-3.0 全文，双许可声明单独放 `LICENSING.md`，README 的 License 段落链过去。这是双许可项目的通行做法。

### 5. Description 与 topics

现在的 description 后半句 "freezes relations, not cognition" 对检索没有贡献。GitHub 搜索结果卡片只显示前约 100 字符，要用满。

建议改成：

```
Run Claude Code, Codex and other agent CLIs as one crew across your
Mac, Linux box and Raspberry Pi. Self-hosted P2P mesh, no cloud relay.
```

topics 现在 18 个，多数是宽泛高热词，竞争激烈且不匹配检索意图。缺的恰好是有真实流量的那几个。换成这一组（GitHub 上限 20）：

```
claude-code, codex, ai-agents, agent-orchestration, multi-agent,
self-hosted, homelab, raspberry-pi, p2p, local-first, mcp,
golang, cli, tui, anthropic, openai, orchestration,
distributed-systems, edge-computing, autonomous-agents
```

`claude-code` 的 topic 页当前是这个领域最热的入口之一，现在完全没有占位。

### 6. 官网英文交给爬虫

`xustalis.github.io/OpenPanda/` 的英文文案全部放在 `js/i18n.js` 里，服务端返回的 HTML 只有中文。`/en/` 是 404，sitemap 只有一条 URL，H1 也是中文。Google 和所有英文搜索入口看到的是一个纯中文页面。

要么出独立路径加 hreflang，要么构建时把英文渲染进 HTML。这块不修，整个官网对国际受众不可见。

## 四、第二优先：制造到达

按投入产出排序。

### MCP 目录（最被低估的一条）

仓库里已经有 `panda mcp` 这个 MCP stdio server，这是当下 agent 工具最重要的分发面。去 mcp.so、Smithery、PulseMCP、glama.ai 提交，同时给 awesome-mcp-servers 提 PR。这条路径上的人已经在找 agent 编排工具，意图最精准。

### Awesome lists（长期被动流量）

按优先级：awesome-go、awesome-selfhosted、awesome-claude-code、awesome-ai-agents、awesome-mcp-servers、awesome-homelab、awesome-raspberry-pi。每个都是一个小 PR，通过之后长期带量。

### Docker 镜像

仓库没有 Dockerfile，也没有 compose 文件。r/selfhosted 和 awesome-selfhosted 的第一道门槛就是"有没有 compose"。发一个 `ghcr.io/xustalis/openpanda` 加一份单机 compose 示例，能打开整个 homelab 人群。

### 发布文章与视频

写一篇英文的技术叙事，讲清楚为什么要做这个：手上有四台机器和一堆 agent CLI，任务在机器之间搬来搬去，缺的是一个调度层。这类第一人称的构建记录，是这类项目最有效的传播材料。

配一个两分钟的 YouTube 演示。YouTube 在这个细分里是一个真实的搜索入口，而且视频可以同时放进 README、Show HN 和所有帖子。

### 正式发布

准备齐全之后再发，三条渠道同一天：

- Hacker News 的 Show HN，标题把价值说穿：`Show HN: OpenPanda – run Claude Code on all my machines as one crew (P2P, no cloud)`。周二到周四，美东时间上午 8 到 10 点。
- Reddit 分角度发，不要同一段话复制粘贴。r/selfhosted 讲自托管和 Docker，r/homelab 讲香橙派和家庭网络里的常驻节点，r/ClaudeAI 和 r/ClaudeCode 讲并发和故障转移，r/LocalLLaMA 讲本地优先的编排。
- X 上发 demo 视频，配一条线程讲设计取舍。

发布之后 48 小时内把每个评论都回一遍。这个阶段回复率比内容质量更影响后续曝光。

只有一次机会。首屏和演示素材没准备好之前不要发。

## 五、第三优先：贡献漏斗

### Issue

没有用户的工具不会有 issue。现在 14 个 open issue 全是作者自己当天建的路标，读者打开 issues 页看到的是路线图，不是社区。

两件事一起做。第一，把路线图挪到一个 pinned issue 或 Project 看板，issue 列表留给真实报告。第二，给"报告问题"造一条最短路径：`panda doctor` 的输出末尾给一个预填好的 GitHub issue URL，带上版本、系统、节点状态；TUI 和 Web 控制台里各放一个"报告"入口。默默放弃的人不会自己来写 issue，把退出动作变成一个点击，issue 量会变。

### PR

最合适的对外贡献入口是 `adapters/`。加一个新的 agent CLI 适配器是一个独立 Python 文件，配上已有的 `make adapter-test` 契约测试，外部贡献者能在半天内跑到绿灯。

做法是为缺失的流行 CLI 各开一个 good first issue：Gemini CLI、Cursor CLI、Aider、Qwen Code、Kimi CLI、Crush、Goose、Amp。每条都写清楚"一个文件加一组测试，`make adapter-test` 通过即可"。

这件事还有第二层收益。每支持一个新 CLI，就是向那个 CLI 的用户群做一次定向曝光，而且贡献者是那个工具的用户本人，他会主动去说。

### Fork

Fork 的主要驱动力是自托管和改造。降低门槛的三件事：单二进制下载、官方 Docker 镜像、一份能在一台机器上跑通的示例配置。现在 Quick Start 要求先 `panda init` 配模型 provider，没有 key 的人走到这一步就停了。给一条不需要 key 的路径，哪怕只是能起来看界面。

### Follow

没有人会 follow 一个仓库，他们 follow 一个有观点的人。持续写、持续发演示、在 r/LocalLLaMA 之类的地方认真回答编排相关的问题。这条没有捷径，但复利最高。

## 六、可信度信号

- 贡献者图谱现在只有一个人加两个同控制账号。不要靠这个数字撑门面，读者一对照 PR 作者就能看出来，代价是信任。第一次真实的对外贡献，比三十个自建 PR 有用得多。
- 8 天发 4 个 release，节奏看起来不稳，而且没有任何一条公告。改成每周一次，release notes 写成对外公告的形态。
- Discussions 已经开启但为零。最便宜的激活方式是发一个 pinned 的 "Show your setup" 主题，让人贴自己的设备拓扑。这种话题能自然产生可见的活跃度，也给后来者一个发帖的理由。
- ja/es/de 三套 README 和 CHANGELOG 的维护成本已经开始反噬（issue 152、153 就是自己给自己开的翻译同步任务）。在受众出现之前，冻结到中英双语，省下的时间放到演示素材和渠道上。
- CLA 对双许可商业模式是必要的，不要取消。但它目前是 PR 的 required check，一次 typo 修复也要先签协议。给纯文档改动一条 `cla: not-required` 的标签通道，把签约留给实质代码贡献。

## 七、30 天排期

第 1 周（转化）
- README 首屏重写，加 demo GIF、对比表、用例段
- 录两段 vhs 演示
- 修 license 识别，换 description 和 topics
- 上传 social preview
- 官网英文改成可被爬取的独立路径

第 2 周（渠道基建）
- Docker 镜像加 compose 示例
- MCP 目录提交，awesome list PR
- 英文构建记录长文，两分钟演示视频

第 3 周（正式发布）
- Show HN 加 Reddit 加 X，同一天
- 48 小时全程回复

第 4 周（沉淀）
- 开适配器 good first issues
- `panda doctor` 接 issue 预填
- pinned "Show your setup" 讨论
- 复盘哪个渠道带来的 star 与 issue，下一轮加码

## 八、不要做的事

- 不要在没有演示视频和像样首屏的情况下发 Hacker News。同一个项目只有一次像样的机会。
- 不要用机器人账号补 star 或 PR。GitHub 的异常检测会动手，而且对真正会 star 的那批人来说这是反向信号。
- 不要把精力继续投在多加一种语言的 README 上。翻译不带来用户。
- 不要为了凑 issue 数把路线图拆成几十条。open issue 数量不是社交证明，响应质量才是。
- 不要改项目名。Panda 这个词确实被 pandas 淹没了，但改名现在只会丢掉已有的那点链接和认知，让 description 和 topic 去承担检索职能就够了。

## 九、预期

准备充分的前提下，一次进入 Hacker News 首页的发布，这个体量的项目通常能拿到 100 到 400 star，随后一两周靠 awesome list 和 MCP 目录的被动流量维持每周十几个。多数发布会掉下来，所以渠道要铺多条，并且允许重复尝试（同一条渠道隔几个月换角度再发一次是正常的）。

到 1000 star 这个量级，靠的是持续三个月的重复曝光，不是单次事件。
