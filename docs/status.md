# 项目现状

> 2026-10-30 更新（`v0.1.3`「Constellation」发布准备：稳定性与成本的补丁，配置与存储兼容——schema 只多一条纯索引迁移（v65，为 `tasks.session_id` 建索引），协议没有新帧。适配器与执行侧：hermes 与 openclaw 的一次性静默 CLI 运行每 20 秒发心跳，长任务不再被静默看门狗当成 `adapter stalled` 杀掉；启动记账帧带 `phase: "spawn"`，首次真实输出前只有心跳、不会提前关闭宽限窗口；编排器自己的静默看门狗、工具调用熔断与硬超时（exit 124 加上 "timeout" 结论）不再触发注入模型的整体重跑，第一次尝试已经产生的副作用不会执行两遍。控制台：事件流连续错过三次心跳即取消重连，浏览器重新上线或唤醒时立即重连，ask 流中途断掉的一轮改为有截止时间的跟进；经反向代理时 `/api/events` 带上 `X-Accel-Buffering: no`；看板的「最近活动」查询只点名列出的任务，实时 trace 流按批排空，任务详情页与展开的事件链改为游标增量；会话内的任务列表共享，已完成的 Markdown 解析结果复用，流式增量每帧合并一次。未做：UPnP/NAT-PMP、punch/dir 源签名收敛、daemon→面板 mesh 指标 IPC。（跨节点 hello 重放已关闭，见「已知限制」。））

> 2026-10-29 更新（`v0.1.2`「Constellation」发布准备：对话有了权限口径，任务有了自己的证据。五档权限模式（自动/手动/接受编辑/计划/绕过权限）存在会话上，并随每个任务规格下发给执行端——计划只给智能体只读工具面并拦住原生命令，手动把每次改动交给前台确认，绕过权限全部放行但仍写审计；对话选定的模式压过记住的批准、`--authorize` 与全局 `approval.mode: never`。交付合同落地并收口：`panda task add --accept`（或 `--accept auto`，按验证性排序、枚举上限与合同上限一致）在智能体结束后由编排器跑检查，基线与终局对比区分「回归」和「本来就红」；执行端丢弃对端撰写的 `spec.acceptance`、拒绝不可逆命令（记为跳过）、CI 步骤只接受验证子命令（`npm publish`/`make release` 不再入选）、断言 `.` 的 artifact 检查被拒，超时按超时记录。控制台：工作空间浮动面板的落点限制在可视带内、加载时修正画布外坐标、新增「整理布局」；待办输入常驻并支持 `n` 快捷键、筛选带计数、空看板单行说明；未安装的 agent 在名单中禁用并写明原因。内核：judge 现在拿到退出码与声明的 scope，「判定完成但无输出且无改动」转人工复核；适配器按 PATH 解析 CLI 别名、以 `-` 开头的提示加 `--`、dsh 静默期每 20 秒心跳、dsh 模型补丁改用 JSON 转义。schema 迁移到 64（合同表），自动执行。未做：UPnP/NAT-PMP、punch/dir 源签名收敛、跨节点 hello 重放、daemon→面板 mesh 指标 IPC；计划模式在没有只读工具面的 agent 上会改派到有能力的备选，无备选则明确失败并要求换 agent 或换模式。）


> 2026-10-27 更新（`v0.1.1`「Constellation」发布准备：发布级版本线上的补丁版，配置与存储不变——无 schema 迁移、无改名的键；协议没有新帧，唯一的格式变化在 UDP 数据报的封帧方式，它与 0.1.0 节点在那个平面上不互通。这一轮是入口客户端新增 OpenAI Responses 与 Google Gemini 两种原生方言（流式、工具调用、用量计量，拒绝文本与超大事件的边界一并补齐）、OpenCode Go / Console 成为内置供应商、会话花名册与节点 pin 打通终端（`/team`、`/node`、`panda ask --team/--node`、待办板）、派发预算改由名单推导（上限 8）、跨进程读写改为文件锁后的原子操作（`internal/fslock`）、任务可钉自己的工作目录、项目工作目录拒绝文件系统根与家目录、`sandbox.mode: strict` 在后端缺失时拒绝执行、控制台给请求体与事件流及 agent 探测加上限。发布审计关掉一批「报绿但没做」的假阳性：名册外点名被拒不换人、轮次小结等真正跑起来的员工、替补者报出双方名字、注入提示压成一行。CI 侧所有 workflow action 钉到 commit sha 并加守卫脚本，adapter 契约与两条 TUI pty 契约在 Windows 与 macOS 上也跑。门禁的 race 腿还抓到并修掉了队列关停与派发之间的一处数据竞争——Go 把 WaitGroup 的首次自增与首个等待者建模成一对读写，两者无序即报竞争；现在运行位在等待方同样持有的锁下取，关停开始后拒绝再取。发布前又收口三处：控制台在把项目工作目录收进可读根之前重新套用宽度规则（升级前写入的越界行不再能整盘读取），两个入口在规则拒绝时回 400 并带上原因；`defense.HashDir` 每轮分配从约 165 MB 降到约 4 MB，摘要不变。安全面另修一处：UDP 数据报改为按对端 X25519 密钥封帧，发送方 id 明文充当密钥选择器并与信封 `From` 对账，未发布身份密钥的对端仍走旧的 mesh 密钥路径；但 0.1.0 节点都发布了身份密钥，所以那个平面要求整网一起升级（WebSocket 通道不受影响，punch 帧不变）。另外把模型注册中心按连接分组（一个端点 + 一把 key 即一个账户，控制台在该连接下列出它的模型），并给开机自启的 launchd/systemd 服务补上 `carddetect` 同款的 PATH——登录时启动的守护进程与终端里启动的能找到同样的 agent CLI，契约测试保证两份清单不漂移。未做：UPnP/NAT-PMP、punch/dir 源签名收敛、daemon→面板 mesh 指标 IPC、跨节点 hello 重放。）

> 2026-10-23 更新（`v0.1.0`「Constellation」发布准备：默认通道从 `v0.0.11` 切到 `v0.1.0`；`v0.1.0-preview` 与更早的预览 tag/release 已恢复保留，供显式钉版本的用户使用。发布前统一口径——站点、README 与本文件指向 `v0.1.0`，CHANGELOG 的 0.1.0 小节并入 #104 的控制台语音输入、每 agent 模型 pin 与工作空间设备面板，站点不再承诺具体下一版号。安装器 23 项自测通过，`make gate` 全绿。）

> 记录时间：2026-08-30（`main`，v0.0.7 发布）；2026-08-31 更新（审查问题全面修复一轮，已并入 v0.0.7）；2026-09-02 更新（v0.0.8-preview：项目一等公民、审批门重写、plan 看板端点，及当日审查的 P1/P2 修复）；2026-09-05 更新（v0.0.8-preview：正式开源项目收尾发布，管理工具族、TUI 运行中转向、模型主动故障转移与注入、多模型注册中心全面落地）；2026-09-06 更新（技能中心与自主发现安装、TUI 全屏化与首次运行向导、Web 技能面板与会话取消）；2026-09-19 更新（随 `v0.0.8-preview` 预发布完成多轮验证）；2026-09-20 更新（`v0.0.8` 正式发布：全链路提示词多语言策略、Provider 地理归属分类、敏感凭据自动脱敏、SQLite WAL 并发优化与发版门禁打通）；2026-09-23 更新（`v0.0.9-beta`：混合传输/DTN 架构完成——延迟加权 mesh 路由、DTN bundle 上线、令牌预算、影子副本、执行器通路、agent 自管理面 `panda mcp`、分块可续传 artifact 推送、DTN 多跳中继与会话检查点；模型配置面完整落地，模型管理 TUI 重设计为全宽注册表 + 单屏表单）；2026-09-25 更新（`v0.0.9` "Periapsis" 正式发布：UDP 数据报平面与 mesh 协调的 NAT 打洞、DTN bundle 载荷加密 v2、接触计划路由、Ed25519 节点身份与签名授权、受限设备 lite 构建、审批记忆与项目级审批策略、执行归属标注、Web 侧栏节点状态徽标、任务管线与 TUI 可靠性一轮修复——批量工具调用、DSML 兼容、审批处置语义、队列管理工具族；安全面收紧：skill URL 导入防 SSRF、`/api/*` 移除 query token 认证、STUN 按源限流；破坏性变更：移除内置模型厂商默认值，`panda init`/`panda model add` 为配置入口；发布前追加：`panda web --lan` 默认支持的局域网分享（自动临时令牌 + 可分享链接）、`panda task add --agents a,b --mode parallel|serial` 单任务多 harness 编排（复用 plan/DAG）、跨进程队列认领轮询 2s→400ms）；2026-10-06 更新（安全加固一轮：`task_events`/`audit_log` 签名验证绑定本节点密钥并拒绝签名尾部剥离，`audit_log` 逐行 Ed25519 签名（迁移 v37）；更新器支持 `OPENPANDA_UPDATE_PUBKEY` 带外 Ed25519 验签与 `OPENPANDA_UPDATE_REPO` 自托管通道；消息去重表改为按最旧淘汰；`SECURITY.md`/配置示例与代码现状对齐）；2026-10-06 性能一轮（能力匹配改为零分配 on-the-fly 分词——2709ns/40allocs → 1619ns/0allocs；artifact 数据面 1 MiB 读缓冲池化并移除每块一次的无用 DB 写；面板会话总结 sweep 从「全表任务行 + 逐任务事件时间线」改为单条 NOT EXISTS 查询；新增 `make bench` 基准：路由匹配/决策、控制与 1 MiB 数据帧编解码、消息去重；实测接收侧编解码 ~86 MB/s、发送侧 ~184 MB/s，结构性提升需二进制数据道，已记录待设计）；2026-10-06 更新（`v0.0.10-preview` "Apoapsis" 预览发布：LAN 自动发现与指纹确认准入、节点 TOFU 密钥钉住、工作树随委派文件任务同行、澄清回路、actuator 派发与五个参考驱动（含串口 MCU）、Pi adapter 与免 Python 内建 generic 执行器、启动目录即项目空间——连同当日的安全加固与性能一轮；破坏性变更：非加密底层上的明文 `ws://` 拨号被拒绝）；2026-10-07 更新（许可证切换为 AGPL-3.0-or-later + 商业双许可，存量 MIT 时代同意经 `terms_version` 重弹一次条款；`feat/session-tree-rpc-auth` 分支未发布工作：会话树 `panda session fork`/`tree` + `/fork` + `POST /api/sessions/{id}/fork`、溢出历史自动压缩成 running digest、`panda rpc` NDJSON-over-stdio 嵌入面、`panda auth login anthropic` 订阅 OAuth——均标 Experimental；修复两个 PTY 契约测试夹具缺少 `terms_version` 被重授权门拦截的问题，`make gate-all` 全绿）；2026-10-07 更新（`v0.0.10` "Apoapsis" 正式发布：会话树/自动压缩/`panda rpc`/订阅 OAuth 随 stable 合入 main，发布流水线全绿、11 个产物上架、Homebrew tap 同步；发布期修复——`session fork` 的 `--at` 值 flag 解析、Windows auth 0600 断言、三处 CI 定时 flake 放宽；五语 README 与脚本/文档中的 preview 语义清理）；2026-10-10 更新（`v0.0.11-preview` "Rendezvous" 预览线 10-10：hello 内协商的逐连接会话 AEAD 加密、蓝牙式 `panda pair` 配对、签名发现 beacon 与 `k:<pub>` 稳定身份、硬钉住路由、断链终局结论 outbox 托管重投、daemon 权威位仲裁、静默看门狗默认 240s 杀停滞 agent、二进制数据帧协商；`timeouts.silence_s` 语义变更——0/unset=默认 240s 杀而非禁用）；2026-10-11 更新（`v0.0.11-preview` 预览线 10-11：控制台实况执行叙事、任务简报卡、CLI 流式对账、迁移连接池固定、Windows 适配器编码修复；未合入实验项：信任分区 mesh/guarded/open 三档与发送侧钳制、签名 `task_accept`、`routing.reputation` 本地信誉进路由分）；2026-10-12 更新（`v0.0.11-preview` 预览线 10-12：实机三设备验证的多跳 mesh 加固——硬钉选经 gossip 拓扑解析两跳外节点且线上保持最终目的地身份、context/artifact 沿委派链逐跳中继（部分网状数据面）、`timeouts.native_s` 原生命令预算、计划阶段继承审批裁决、askengine 挂产物池修静默产物断裂、父子任务资源键死锁与运行中孵化子任务折叠、拒绝轨迹与重启中断原因完整上溯、产物越界探测回 EOF、$HOME 误打包拒绝+source 打包凭证剪枝、agent 替换显式标注、未执行任务渲染 `✗ failed`、审批门补齐重定向/管道/进程替换/PowerShell 下载写法）；2026-10-12 更新（发布日审查修复一轮：guarded/open 来源任务默认截留本节点模型凭据——注入、凭据透传与认证失败回退三条通道齐断，`network.trust.accept.guarded.model_creds` 可显式 provide；`internal/core` 全部裸 goroutine 收进 `guard` 恢复面、服务端连接回调经 `guard.Call`（阻塞计址语义不变）；自更新在无 `ReleasePubKey` 时 fail-closed，`OPENPANDA_UPDATE_UNSIGNED=1` 显式放行 checksum-only 通道；`handlers.go` 4338 行按职责拆为 handlers/run/prompt/wire 四个文件；cardmut/providers/nodeidentity 补测试 39→72%、42→94%、59→73%）。；2026-10-13 更新（任务间消息板落地：同一 plan 的兄弟阶段 / 同一父任务的子任务集之间可互发有界短消息——槽位化（每发送者每收件人一格、新覆盖旧、迟到旧拷贝被 seq 守卫拒绝）、三档粒度（status ≤400B / report ≤4096B / artifact_ref 指针）、每 episode 发送预算（`messages.budget`，默认 8，超支拒发并折回提示）、跨节点 owner 扇出（plan 板归计划原点、children 板归父执行节点，与结果回传同规则），读取发生在监督轮边界，不阻塞运行中的 agent；`panda task board <id>` 为查看入口，设计见消息板设计笔记；单测与三节点进程内 e2e 全绿，真实多机验证待做）；2026-10-15 更新（v0.0.11-preview 之后 main 上一批 computer-use 与安全收尾：computer-use「眼/手」落地——`drivers/panda-screen`（screencapture/grim/scrot/PowerShell 截屏）与 `drivers/panda-input`（cliclick+osascript / xdotool / ydotool / PowerShell 点击·输入·按键·滚动，tier 2），`DetectActuators` 自动探测 bundled `drivers/` 且 `Interface:"desktop"` 执行器只在疑似有 GUI 会话时广告（macOS 查 console 属主、Linux 查 DISPLAY/WAYLAND_DISPLAY、`OPENPANDA_GUI_SESSION` 可覆盖）；产物回传链补齐——裸任务私有工作目录产出自动打包收回、面板三个认证端点 + 前端内联预览、`panda task artifact` CLI；截屏升级为一等敏感类别——`requires`/`action_spec`/宽令牌命中即标记，`send.guarded.screen` 默认拒绝，准入/发送/产物服务三道闸，执行端运行时 `screen_output` 打标随 `task_result` 逐跳传播（迁移 v45），授权缓存命中时对 screen 条目逐次复检对端信任档；自更新通道收尾——官方验签公钥编译内置、`OPENPANDA_UPDATE_UNSIGNED` 逃生口、`install-local` 符号链接布局）；2026-10-15 更新（`v0.0.11` "Rendezvous" 正式版准备：发版审查一轮并修复——屏幕产物作为下游任务 input 的 mesh-only 溯源补齐（`ArtifactRef.Screen` 打标，路由/准入/服务判定三面读取，消费显示字节的任务自身判为 display-bound 逐级级联）、artifact 空清单序列化为 `[]` 而非 `null`、panda-input 按键别名补全与 macOS 真双击、panda-screen 缺值守卫、shell 补全补 `task board`/`task artifact`；版本转正 `0.0.11`，`make gate` 全绿）；2026-10-19 更新（`v0.0.12-preview` "Constellation" 预览发布：同项目任务并行编排——`paths` 路径作用域锁 / `isolate` 隔离 worktree 三向合并回主树、委派边界 `delegation.{enabled,max_children,fan_out,max_depth}` 随 `TaskDelegatePayload` 逐跳取严、匿名任务改锁 `task:<id>`，CLI/REPL/Web 三面同等暴露 `--paths`/`--isolate`；`task send` 双模式消息（queue 看板便签 / inject steer 打断当前轮）、`task continue` 让轮预算耗尽的停驻任务带 steer 注记重回队列；跨进程执行租约 `exec_locks`（BEGIN IMMEDIATE + GC 触发器 + 每轮 nonce）；上下文溢出恢复——回放窗按模型窗口 3/16 自适应（8K–160K）+ 同轮一次重试；模型错误分类落地（重试倒数、provider 明细 + 建议折叠）；daemon `--debug-addr` pprof + 面板资产 gzip + settle watch 事件化；控制台收拢成一处干活——队列/计划/项目合并「总控」页、工作空间页画全舰队拓扑图并可直接派活进选中对话、新增个人资料页与 `/api/stats`；模型目录换代 + 按模型 token 策略 + MiniMax 入列；安全加固一轮——产物不再夹带节点配置/能力卡/工作根、用户数据收紧 0600/0700、取消任务连带清掉 agent CLI 进程树、解包拒绝越出目标目录；版本定位 `0.0.12-preview`）；2026-10-21 更新（跨网段去中心化组网 P1–P5：双钥匙身份与能力宣告（x_pub/relay/addrs）、transit 盲中继端到端密封、流行病式目录差量同步（dir_digest/dir_pull/dir_rows + 源签名行）、K=2 动态会合边与可达性阶梯（addrs 直拨→自动打洞+punch_want→nat_suspect 持久化→transit→outbox）、策略面落地（network.bootstrap_edges/auto_punch/transit/rendezvous + trust.relay.{transit,serve,max_kbps,max_sessions}）、`panda://join` 冷启动 URI 与 `panda join`/`nodes invite --uri`、pair 帧经 transit 的接触点代理（att_clients 回程绑定 + seal=none 配对豁免）、AutoNAT `dial_check` 回拨证明会合角色、`panda nodes` 组网列与 doctor 会合检查；未做：UPnP/NAT-PMP 自动端口映射、punch/dir 帧源签名收敛、transit/dir 指标计数已落在 `Core.MeshMetrics()`（relay 收发字节、目录行收发、pull、打洞尝试/成功、dial_check 回执）；面板是独立侧车进程，拿不到 daemon 内存里的计数器，`/api/mesh` 出口需要先定 daemon→侧车的 IPC 形态）
> 2026-10-21 更新（自进化 Phase 1–4 主体：`self_evolution` 默认关闭——`panda self` 签名补丁集（manifest + 逐 patch sha256）、`<data>/self` 队列与合并 worktree、合并提案作为审批任务、批准后 apply→`make gate`→build→`StageLocal`→swap、`panda.release` 回滚目标；跨进程 worktree 锁、合并前 base 新鲜度检查与 `+self.N` 版本分配、服务重启延后到任务落定；Web 补丁集/diff 审查卡与只读 `/api/selfevo`；未做：mesh 流转、auto 模式、产出侧 gate 摘要、`panda update --no-rebase`、macOS 稳定签名）。
> 2026-10-22 更新（`v0.1.0-preview`「Constellation」：发布级版本线的第一个切点。安装器这一轮同时加固——每条安装路径都拒绝降级（Windows 侧补齐 schema 守卫与 `-Force`）、`install.sh` 校验发布签名（本机 openssl 支持 Ed25519 时签名不符即中止，不支持时明确告警）、`install.ps1` 从它真正下载的仓库解析版本；安装器自动化覆盖扩到 23 项（新增降级拒绝与 `--force` 放行、伪造签名拒绝、无签名发布的告警与 `OPENPANDA_REQUIRE_SIG` 下的强制拒绝、`--lite`）。跨网 P1–P5 全线落地（双钥匙身份与可达性卡、transitive 盲中继、流行病式目录同步、K=2 会合边与可达性阶梯、策略面与中继预算、`panda://join`、pair over transit、`dial_check`）并完成发布审计与第二轮加固——多跳经中继拉取产物、模型自报失败不再记成完成、对端拒绝后回到有能力的发起节点、hello 身份钉住、项目包准入与解压上限、UDP 通道崩溃修复、strict 模式机密读拒绝、独立控制台补齐会话/技能 API；加固轮再修：目录份额上限按签名者计数（幻影行曾能填满目录、饿死诚实节点）、拨号预算不再连坐连接生命周期、读不到信任断言时按 open 处理、配对清扫的空指针（曾可让 daemon 整体停机）、transit `SealNone` 豁免补回归测试、经中继的打洞请求保留请求方。真实跨网多跳已实测（Mac → 中继 → 云服务器，3 MiB 项目树哈希一致）。审计与跨网部署结论汇总在 `CHANGELOG.md`；未做：UPnP/NAT-PMP、punch/dir 源签名收敛、daemon→面板 mesh 指标 IPC、hello 跨节点重放（需协议级质询-应答，后续版本处理）。
> 2026-10-21 更新（P0–P2 复审一轮的收口——自进化面：合并提案钉住被审清单的 manifest 摘要，审批时清单变了就要求复审（开机 rebase 扫掠会就地改写队列里的补丁集，原来会把没人看过的字节合进去）；rebase 不再改写终态（已拒绝/已合并）集合，也不再抹掉「已拒绝」标记（原来一次 rebase 就把人的「否」变回待提案）；`panda self rebase` 与开机扫掠共用同一条准入规则（producers=self 下不再给外来集合补签成「本机签的」）；清单 base 必须是钉死的 commit sha；集合 id 一律按目录名校验（不再能穿越队列根）；导入不再把源目录的 `.state` 标记带进来；`+self.N` 计数器落盘，`reset --purge` 后不再倒退；回滚的 schema 护栏改 fail-closed 并用真实数据目录的 floor。跨网段面：transit 落地端核内层信封收件人确是自己（否则替别人执行任务并替人回结果）；中继带宽按真正上线的字节计费；目录 gossip 里未签名的行只能新建、不能顶掉已有行（否则任何成员都能改写别人的能力卡/地址并把它踢出拨号路径）；每发送者的新 id 预算改为锁内预留（消除 TOCTOU），并加单发送者份额上限（轮换 session id 也逃不掉）；want 列表截断前排序（原来按 map 随机序截断，两边永远追不上同一批 id）；目录摘要剔除实测 RTT（原来摘要随每次 ping 变动，两端目录永远不收敛、只能每 30s 互拉一次）并加 2s 缓存（每个入站心跳原本要全表扫描+哈希）；会合选举的故障域从硬过滤改为偏好（LAN 内所有节点同域，原来只选得出一个））。
> 这份文档回答一个问题：**现在做到哪一步了，哪些能用、哪些还不能。**

## 一句话结论

单机与可信内网（当前三台：Orange Pi / MacBook / Windows）**可以按设计使用**：
一句话进来会被分成「本地回答 / 派一个任务 / 拆成跨机器的多阶段计划」，
不可逆操作停在待审批队列等人批。v0.0.7 补齐了可用性层：能力卡可从所有
界面编辑且热生效、设备配对产品化、每个任务结果有 LLM 汇报。

v0.0.8-preview 的主要增量：**项目成为一等公民**——有元数据表和
落盘的「当前项目」指针，任务自动继承，被委派的任务把项目记忆和工作树
一起带到执行节点，产出覆盖式收回；**审批门重写为「仅不可逆」**（下载
落盘向量在 9/2 审查后重新纳入门控）；控制台补上 plan 看板端点与
审批/路由/记忆上限/注入设置。

2026-09-06 v0.0.8-preview 追加：**技能系统学会自我服务**——离线精选
技能中心（`panda skill hub`）、从路径/URL/归档导入（`panda skill import`）、
Ask 引擎在任务执行中自主发现并安装所需技能；`panda skill` 命令族与
`/skill` REPL 命令、Web 技能管理面板同批落地。TUI 迁入 alternate screen
全屏模式并带首次运行引导向导；Web 控制台新增会话取消、keepalive 重试
客户端、目录选择器与执行事件时间线。引擎侧新增模型健康熔断与回退路由、
noauth 模型支持；SQLite 打开外键约束。

P0 安全发现（路径穿越、结果送达、TUI 退出）已关闭，P1 安全加固（默认回环
监听、context_fetch 授权、supervisor 不可达停泊）已落地。信任模型仍有已知
缺口（见「已知限制」P2-8），但安全 posture 显著提升。

2026-08-31 审查修复一轮已落地（已并入 v0.0.7）：
- **后端**：常驻协程 recover 守护（`internal/guard`，panic 记日志 + 受控关停）；
  存储初始化收敛（`cmd/panda/store.go`，daemon 与面板同一条路径）；迁移互斥
  （`BEGIN IMMEDIATE` + 事务内复查 `user_version`，旧二进制遇新库明确报错）；
  系统配置目录平台化（unix `/etc/openpanda` 不变，Windows `%ProgramData%\OpenPanda`）；
  Windows 优雅关停（`SetConsoleCtrlHandler`）与 Windows 控制台颜色；
  `make build-darwin-amd64`；`skill`/`reminder --help` 退出码修复。
- **前端**：统一事件总线单例（单条 SSE + `Authorization` 头 + 指数退避重连）；
  会话流竞态守卫（流式中切会话不再串气泡）；顶层错误边界；命令面板/确认框焦点圈闭；
  看板卡片键盘操作；系统轮询隐藏标签页暂停；面板事件指纹缓存（扫描负载与连接数解耦）。

## 能力对账

| 设计目标 | 状态 | 落地位置 |
|---|---|---|
| 语音发布任务（桌宠入口） | 🟡 已接通，真机未验 | `cmd/panda/voice.go`（`panda voice`）+ `extensions/voice/*.py` |
| 简单问题本地答、复杂问题外派 | ✅ | `internal/entry` 四分类：`answer` / `tool_call` / `task` / `plan` |
| 一次多任务负载均衡到多台并行 | ✅ | `internal/scheduler/score.go`（资源效率 0.4 + 用户优先级 0.3 + 节点等级 0.2 + 等待时间 0.1，另有项目驻留 0.2、链路延迟 0.15、opt-in 信誉 0.1 三个附加项，再乘心跳新鲜度衰减，半衰期 30 分钟） |
| 多适配器各自独立调度 | ✅ | `internal/agents/registry.go` 注册 10 个 adapter（另有 generic.py 通用桥）；`requires: ["agent:codex"]` 是硬过滤条件 |
| 按硬件路由（显存任务不落到派上） | ✅ | `resource_profile` → `ledger.Fits`；未声明硬件的旧卡片按「沉默 ≠ 0」放行（`Declared()`） |
| 跨机器多阶段流水线 | ✅ | `internal/plan` + `internal/core/plan.go`；阶段产物经 `internal/artifact` 分块搬运 |
| 完成后上报、队列告知完成 | ✅ | `panda queue` / `panda task <id>` / `panda plan show <id>` |
| 不可逆操作进待审批 | ✅ | tier 判定 `internal/defense/permission.go`（含 Windows 动词/解释器表）→ `review` 状态 → `panda approve` / `reject` |
| 统领再往下分给 claude code / codex | ✅ | `internal/commander` 三层：`native` / `agent` / `manual` |
| 去中心化、每个节点都能是根 | ✅ | 逐边路由 + `AppendChain` / `Predecessor` 防环，无主根 |
| **能力卡全界面编辑 + 热重载** | ✅ | `panda card native/agent/manual` 子命令、`/card` REPL/TUI、Web `/api/card`；`ReloadCard` 热重载 + 带卡心跳广播 |
| **设备配对产品化** | ✅ | `panda pair`、`panda nodes add <addr>`、Web "Invite a device" CTA |
| **LLM 任务汇报** | ✅ | `entry.SummarizeResult` 在每个内联任务后调用，REPL/TUI/Web 渲染汇报 |
| **Bubble Tea TUI** | ✅ | `panda` 默认进入 TUI，带 tier-2 审批路径；经典 REPL 可通过 `PANDA_CLASSIC_REPL=1` 使用 |
| **项目随任务跨设备**（v0.0.8-preview） | ✅ 代码落地 | `panda project` 命令族 + settings 指针；委派载荷内联项目记忆、工作树走分块 artifact、产出覆盖式收回（`internal/projects` / `internal/core/project.go`） |
| **Plan 看板端点**（v0.0.8-preview） | ✅ | `GET /api/plans`、`GET /api/plans/{id}`（阶段 + artifact 接线）；Web 看板视图已接（`views/plans.tsx`） |
| **LAN 自动发现 + 指纹准入**（v0.0.10） | 🟡 Experimental | UDP beacon（`network.discovery_addr` :7837）→ `pending_nodes`（60s TTL + 64 行上限）→ `panda nodes admit`；`nodes verify` 钉住 Ed25519 指纹 |
| **工作树随委派同行**（v0.0.10） | 🟡 Experimental | `context_type=file` 仓库任务打包 `__worktree__` artifact（≤256 MiB），执行端私有目录跑完收回 |
| **澄清回路**（v0.0.10） | 🟡 Experimental | agent 以 `PANDA_QUESTION:` 行挂起 → `panda approve <id> -m "答案"` 带答案恢复 |
| **任务间消息板**（v0.0.11） | 🟡 Experimental | `internal/core/messages.go`：`plan:<id>` / `task:<parent>` 两种 scope 的槽位板（每发送者每收件人一格、新覆盖旧）；agent 以结构化 `messages` 字段或 `PANDA_MSG` 行发消息，轮边界折进 `[sibling board]`；`bus.MsgTaskMessage` 跨节点 owner 扇出，接收端校验成员资格；`panda task board <id>` 查看 |
| **会话树 / 自动压缩 / RPC / 订阅 OAuth**（v0.0.10） | 🟡 Experimental | `panda session fork`/`tree`/`/fork`、溢出历史折叠为 running digest、`panda rpc` NDJSON-stdio、`panda auth login anthropic` |
| **会话 AEAD + 蓝牙式配对**（v0.0.11） | 🟡 Experimental | hello `caps` 协商 `sessaead` 逐连接 AES-256-GCM（HKDF 绑两端 nonce/身份）；`panda pair` X25519 + 短验证码确认 + 密封下发共享密钥 |
| **信任分区**（v0.0.11） | 🟡 Experimental | `network.peers[].trust`：mesh/guarded/open 三档（`panda nodes trust`）；guarded 钳制委派暴露面 + 准入策略（`network.trust.accept.guarded`，可选 policy_llm）；非 mesh 来源任务默认截留模型凭据（`model_creds`）；签名 `task_accept`；`routing.reputation` 本地信誉；截屏任务 mesh 独占（`send.guarded.screen`，准入/发送/产物拉取三道闸 + 执行端 `screen_output` 运行时打标逐跳传播 + 授权缓存命中逐次复检信任档——降级即时断流） |
| 引脚驱动舵机（香橙派） | 🟡 通路已落地，真机未验 | v0.0.10 actuator 派发：卡片声明 `actuators` + argv 模板，`spec.action_spec` 跨委派传递，执行端替换占位符跑驱动；`drivers/` 随发行包附带 servo/mic/camera/notify/mcu/screen/input 七个参考实现 |
| **Computer-use 眼/手**（v0.0.11） | 🟡 通路已落地 | `hardware:screen_capture`（截屏，tier 1）+ `hardware:gui_input`（点击/输入/按键/滚动，tier 2 因不可逆）两个参考驱动；`DetectActuators` 自动探测 bundled `drivers/`（`OPENPANDA_DRIVERS_DIR` 独占覆盖），Windows 解析 `.ps1` 入口并以 `pwsh -File` 运行（.cmd/.bat 刻意不解析：cmd.exe 的解析规则与 Go 的 argv 转义不兼容）；`Interface:"desktop"` 执行器只在疑似有 GUI 会话时广告（console 属主 / DISPLAY·WAYLAND_DISPLAY / 非 Session 0，`OPENPANDA_GUI_SESSION` 覆盖）——无头守护进程不会虚报屏幕；路由提示词已教会 capture→reason→actuate 链 |
| **任务产物取回**（v0.0.11） | ✅ | `panda task artifact <id> [--list|--file|--out]`；面板 `GET /api/tasks/{id}/artifact`、`/files`、`/file?path=`（Bearer 内）；控制台 ArtifactSection 列清单 + 图片/文本内联预览 + 下载；裸任务私有目录产出自动打包（空目录不产空包，固定 workdir 永不整包） |
| **同项目并行编排**（v0.0.12-preview） | ✅ | `paths` 作用域锁（`scope:<project>/<path>`，不相交兄弟并行）+ `isolate` 私有副本（detached worktree 叠未提交增量，三向合并回主树，冲突上报父看板）；用户入口 `panda task add --paths/--isolate`、REPL `/task add` 同款 flag、Web 新建任务并行模式选择器；委派边界 `delegation.{enabled,max_children,fan_out,max_depth}` 随 `TaskDelegatePayload` 逐跳取最小；匿名任务锁 `task:<id>` 不再全局串行；看板泳道条 + 任务详情父链 + 逐泳道提示 |
| **任务消息 queue/inject + 续跑**（v0.0.12-preview） | ✅ | `panda task send <id> -m …` 排队便签（下轮监督边界折进意图）；`--inject` steer 级中止当前轮并领头下一轮；看板承载、跨进程降级为下一边界投递、远端任务如实拒绝；`panda task continue [-m]` 把 `needs_followup` 停驻任务 CAS 回队列、复用 agent 会话与工作目录（`/task continue`、`POST /api/tasks/{id}/continue` 同步） |
| **跨进程执行租约**（v0.0.12-preview） | ✅ | `exec_locks` 表（迁移 v47–49）：所有运行认领走 `BEGIN IMMEDIATE` 下的键集合 + 执行器行；GC 触发器在退出活跃态时释放；每轮 nonce 谓词保证迟到解锁只能丢自己的行 |
| **上下文溢出恢复**（v0.0.12-preview） | ✅ | `sessions.ReplayBudget` 按模型窗口 3/16 自适应（钳 8K–160K），贯穿 REPL/ask --continue/会话线程/RPC/Web；溢出轮内一次性重试（摘要折叠 + 四分之一预算 + 技能降级索引行），再溢出照旧停复核 |
| **控制台一体化**（v0.0.12-preview） | ✅ | 队列/计划/项目合并 `#/control`「总控」页；`#/workspace` 设备拓扑图（hello 广播 OS/机型，迁移 v54，`/api/nodes` 携带链路状态画对等边）；工作空间页页脚直接派活进选中对话；`#/profile` 个人资料页 + `/api/stats` 活动日历；记忆/技能并入设置；模型切换挪到发送键旁（Codex 式菜单） |
| **模型目录换代 + 按模型 token 策略**（v0.0.12-preview） | ✅ | `ModelSpec` 行最长前缀解析（MiniMax-M3 1M/512K 与 M2.x 200K/192K 并存）；超上限 `max_tokens` 钳制、未设置时按窗口八分之一自动装预算；MiniMax 入 provider 目录；厂商检测识别模型名别名；全系默认值跟进当前代际（sonnet-5-5 / gpt-6.1-sol / kimi-k3 / glm-5.3 / qwen3.7-plus 等） |
| **控制台语音输入**（v0.1.0） | ✅ | `webui/panel/voice.go` + `POST /api/voice/transcribe`：页面录 16 kHz 单声道 WAV，转发到 OpenAI 兼容的 `/audio/transcriptions`；`asr` 段可选、留空继承当前模型；厂商拒绝原样回传 |
| **每 agent 模型 pin**（v0.1.0） | ✅ | `injection.agents.<agent\|adapter>` 只为该 agent 钉模型，未写字段继承节点模型；继承止步于 `params`/`headers`/`thinking`，避免把一种方言的请求体带进另一种 |
| **工作空间设备面板**（v0.1.0） | ✅ | `webui/app/src/views/workspace-panels.tsx`：面板是机器、磁贴是 agent 与实时忙闲；一轮工作不再因批量首份报告提前结束（`roundStillOpen()` 读任务板） |
| **原生方言：OpenAI Responses + Google Gemini**（v0.1.1） | ✅ | `internal/entry/responses.go` + `google.go`：入口客户端原生驱动两种线面，含流式、工具调用与用量计量；拒绝文本会流式落地，参数缺失 `output_item.added` 时给出诊断而非一个空答案，SSE 单事件上限 4 MiB，`bufio.ErrTooLong` 判为终态，不再重放并重复计费 |
| **OpenCode Go / Console 内置供应商**（v0.1.1） | ✅ | `panda model add opencode-go oc_sk_…` 一把 key 覆盖按量目录与 Go/Go Plus 订阅；按模型路由到 OpenAI Chat / Anthropic Messages / OpenAI Responses / Google Generative AI；只有 SystemOne（Jev）未支持，并在配置阶段就说明 |
| **终端名册与节点 pin**（v0.1.1） | ✅ | `/team`（空格切换的多选卡片）、`/node`、`panda ask --team/--node`、`panda session ask --node` 与 RPC 的 `AskScope` 一起承载名册与节点；`panda todo …` / `/todo` 把待办板带进终端；派发预算由名单推导（`task_budget` 可申报更宽的扇出，上限 8） |
| **跨进程存储原子写**（v0.1.1） | ✅ | 新增 `internal/fslock`：会话与记忆的读-改-写整段持锁（文件锁在进程内 mutex 之前，顺序一致），跨进程写入不再互相覆盖；项目改名一步搬完记忆，Dreaming 晋升先落盘再记账，什么都没晋升的整合清扫不重写文件 |
| **任务级工作目录**（v0.1.1） | ✅ | 任务可钉自己的工作目录，跑在那里并锁那个目录而不是项目根，两个目录树里的业务并行推进而不是互相排队；钉住目录的任务留在本机；项目树内部降级为作用域叶子。任务的 pin 走与项目同一套宽度规则，卷根、家目录与系统目录一律拒绝 |
| **队列关停与派发的顺序**（v0.1.1） | ✅ | `internal/scheduler/queue`：运行位在一把等待方同样持有的锁下取，关停开始后拒绝再取——等待开始前取到的位由它等待，之后取到的被拒绝，任务留在队列里；派发循环不再与引擎关闭时的 `wg.Wait()` 竞争（Go 把 WaitGroup 的首次自增与首个等待者建模成一对读写，两者无序即报数据竞争） |
| 最短路径多跳中继 | ✅ 图路由已落地 | 邻居广告构建链路状态图，边权取 `LinkMetrics` 的 RTT 采样（无测量走 `unknownLinkCost`），`graphFirstHop` 跑 Dijkstra 取全局最便宜路径的第一跳（`internal/scheduler/route.go`） |

## 两条入口都可达

跨机器流水线有两个入口，都通向同一个 `core.StartPlan`：

```bash
# 一、写成文件的流水线（每周要跑的那种，可读可 diff）
panda plan example > train.yaml
panda plan run train.yaml --dry-run     # 先看路由结果
panda plan run train.yaml
panda plan show <plan-id>

# 二、一句话（桌宠场景，不需要先写文件）
panda ask "写个图像分类的训练脚本，在有显卡的机器上跑，最后把结论发回来"
panda voice                              # 同一条路径，改成说出来
```

模型什么时候输出 `plan` 而不是 `task`，判断标准只有一条：**是否必须换机器**。
同一台机器上的连续几步仍然是一个 task。

## 阶段的审批语义

`core.StartPlan` **不给任何阶段 tier-2 同意**，`panda voice` 也没有 `--authorize`。
后果是刻意的：流水线里出现不可逆动作时，那个阶段停在 `review` 等人批，
而不是继承「你对整句话的一次授权」。对一条 shell 命令的同意，不等于对一条
三机器流水线的同意；麦克风听到的更不等于。

## 验证到什么程度

| 检查 | 结果 |
|---|---|
| `make gate`（fmt-check + vet + build + adapter-test + test + race-focused） | exit 0（2026-10-15 复验，含 computer-use 驱动/产物回传/截屏信任闸；race-focused 覆盖 core/trust/bus/storage） |
| `make gate-all`（+ web-gate + 两个 TUI PTY 契约） | exit 0（2026-10-07 复验；修复了两个 PTY 夹具缺 `terms_version` 被条款重授权门拦截的问题） |
| Web typecheck + 单测 + vite build | exit 0（79 tests，2026-10-07） |
| P0 安全发现（路径穿越、结果送达、TUI 退出） | ✅ 已关闭 |
| P1 安全加固（默认回环、context_fetch 授权、supervisor 不可达） | ✅ 已落地 |
| 2026-08-31 审查修复（后端稳定性 + 前端竞态/可访问性） | ✅ 代码落地，单测全绿；后端稳定性面由 v0.0.11-preview 三设备实机回归覆盖（委派/杀进程/重连/重放），前端竞态修复属单设备面 |
| 2026-09-02 审查（P0 编译错误、P1 审批门绕过、P2 SSE 错误语义） | ✅ 已修复：`priorityName`→`priorityLabel` 并入 932442a；curl/wget 下载落盘重入门控；指纹缓存等待者回传 error；`go build`/`vet`/相关单测全绿 |
| 任务间消息板（槽位/寻址/预算/drain/伪造 + 三节点 wire e2e） | ✅ 单测与进程内三节点 e2e 全绿（2026-10-13）；真实多机互发未验 |
| 三设备真机跑通旗舰流水线（派 → mac → win → 派） | ✅ v0.0.11-preview 在真实三设备舰队（macOS ↔ Windows ↔ Linux SBC）实机跑通：多跳委派与硬钉选（两跳外节点经 gossip 拓扑寻址）、两跳流水线（context/artifact 沿委派链逐跳中继）、执行中杀节点、断线重连与消息重放；项目包跨设备全链路（记忆内联 + 产出收回）仍待专项真机验证 |
| 语音链路真机跑通（唤醒 → 转写 → 派任务 → 朗读） | ❌ 只验了降级路径（缺 driver 时正确报错并退出） |

语音 sidecar 默认后端不需要密钥，但需要本地依赖：
`pip install openwakeword faster-whisper SpeechRecognition pyaudio numpy`。缺依赖时
`panda voice` 会打印 sidecar 自己的原因并退出 1，不会静默重启 python。

## 已知限制

**P2-8 全网单一共享密钥（已部分缓解）。** 网络共享密钥仍是成员凭据：持有它的
节点可以完成 mesh 加入。在此之上，Ed25519 节点身份与签名同意已落地——tier-2
的 `Authorized` 以 `Ed25519(taskID, authorized, ts, ConsentDigest)` 随任务传递，
执行端按目录中记录的来源公钥验签，签名剥离/篡改/换钥重签一律拒绝；`panda nodes
verify` 可钉住指纹，钉住后换钥 hello 直接拒绝。**仍存缺口**：共享密钥成员可为
「接收端从未见过其公钥的节点」伪造同意（多跳中继 + TOFU 的固有边界，现会打印
警告）；未钉住的身份仍按 TOFU 首次见到即信任。因此「不可逆操作必须我审批」在
单机和可信内网成立，一旦网络里有你不完全信任的节点就不成立。彻底修法是
per-node 密钥分发/准入——属于改信任模型，不是打补丁。

**跨节点 hello 重放已关闭，旧版本兼容路径除外。** 捕获的 hello 不能在另一台节点上
重放：hello 可以绑定接收方（`to` 字段加 `SigB`/`EdSigB`），接收端要求 `to` 等于
自己的节点 ID 且签名匹配，绑错目标的 hello 直接拒绝；`VerifyHelloP` 之上还有单次
使用缓存（键为发送方 ID、时间戳和实际验签的那个签名，窗口 `MaxHelloAge`），以及
降级防护——曾经绑定过 hello 的节点再发不带绑定的 hello 会被拒绝。三层合起来覆盖
了跨节点重放。残留的是从未绑定过的旧版本节点：它们仍走两段式 HMAC 验证，单次
使用缓存只在本节点内生效。原计划的协议级质询-应答没有采用，绑定签名在不增加
往返的前提下达到了同样效果。

**P2-9 审计链已签名，锚点在库内。** `task_events` 与 `audit_log` 的哈希链现在
逐行携带 Ed25519 签名（迁移 v35/v37），验证时绑定本节点公钥（`VerifyTaskEventChain`
/ `VerifyChain`：换钥重签会被拒绝，已签名链不允许再出现未签名行——防尾部剥离）。
但签名锚点在数据库本身：从未签过名的历史前缀无法与「被整体重写」区分，进一步
收紧需要外部存证（导出/公证链头），仍在路线图上。

**审批门的剩余不透明向量。** 审批门重写为「仅不可逆」后，`curl -o`/`wget -O`
下载落盘已重新纳入 Tier 2（下载字节对分类器不可见）；相邻写法随后一并收口：
`curl url > /tmp/x` 重定向写盘、`curl url | tee /tmp/x` 管道落盘、
`bash <(curl …)` 进程替换（含被引号包住的嵌套 shell，以及 `iwr -OutFile`
等 PowerShell 拼法）现在与 `-o` 同判为 Tier 2。仍存的是刻意的放宽：执行本机
已有脚本 `bash script.sh` 内容不扫描（否则节点跑不了自己的 build），因此
「先用任意方式写脚本、再执行」仍在门控之外；彻底修法是命令级污点追踪
（会话内新写的文件被执行时升级），属于大改。纵深是有的：能力卡上对 native
能力显式声明 `tier: 2` 永远压过推断。

**i18n 收了两轮，模型面基本对齐。** v0.0.6 把此前列出的面（语音链路、ask/repl
计划输出、会话摘要、卸载报错、一处 help 提示）挪进了 `internal/i18n`
五语言；本轮再清：ask 引擎边缘答复（「N 轮工具未收敛」「计划无法执行」）走
`ask.loop.*`/`ask.plan.invalid` 五语言 key；全部 ask 工具的工具描述与 schema
属性描述补齐为 "EN / 中文" 双语内联（沿用 `tools.go` 约定；`task_submit` 早已按
会话 locale 二选一，MCP 面纯英文）；给 agent 的注入通知、memory manifest/快照
围栏、dream 日记条目均已双语化。工具执行结果与错误体仍为中文（模型消费的中间
产物，测试已钉住；模型按用户语言转述），`supervise` 上级裁判提示词保持调好的
中文原文——这两块若要 5 语言是单独一轮设计。

**GPIO / 舵机 / computer-use 已有执行通路但真机未验。** v0.0.10 起 actuator 派发
落地（`action_spec` → 卡片 `actuators` argv 模板替换 → 驱动进程），`drivers/`
附带七个参考驱动且发行包自带；main 上新增 screen/input 的截屏与 GUI 输入链路
也只过了单测——舵机/麦克风/摄像头/串口没在任何一块真硬件上点过火，截屏的
「任务 → 驱动 → 产物 → 面板预览」整链只在开发机上抽查过（macOS `screencapture`
实采可用，TCC 授权只能由用户在 GUI 会话里授出）；`panda ask "把舵机转到 90 度"`
的端到端验证仍欠。

**Computer-use 的认知在 agent 侧，不在编排模型。** 截图以 artifact 形态回传、
用户可在 CLI/面板查看下载，但编排模型/provider 链路仍是纯文本——「看一眼屏幕
再决定点什么」的视觉闭环发生在执行节点的 agent 内部（agent 自己读 PNG），
编排模型本身看不到屏幕像素。这是刻意的当前边界：多模态进编排层是单独一轮
设计。另外 `guiSessionLikely` 只是会话探测而非权限探测——macOS 录屏/辅助功能
授权在驱动执行时诚实报错（exit 3/1），由用户在系统设置里授出。

**任务间消息板已落地，但真实多机未验，且它是协作信道不是保密信道。**
槽位语义（每发送者每收件人一格、新覆盖旧）、寻址（stage id / parent /
children / 成员 task id 及唯一前缀）、owner 判定、预算计费、接收端成员
校验都有单测与进程内三节点 wire e2e 覆盖；真实三设备上的互发与跨节点
`stage:<id>` 定向仍欠一轮验证。两条刻意的边界：其一，定向消息在**发送方
所在节点**的本地副本对同 scope 成员可见（该节点不持有 roster，只能就地
解析 `parent` 与具体 task id；`stage:<id>` 地址在发送节点按广播形态留底）
——同一 scope 内本就是协作方，但别把它当私聊；其二，投递尽力而为，无
独立 outbox，丢帧靠下一次发送自愈（槽位模型使然）。MCP 主动拉板
（extended policy 下不等轮边界）、webui 板视图、通信效率遥测未做。

**多跳已是加权最短路。** 邻居广告传播拓扑、`LinkMetrics` 携带链路 RTT，
`graphFirstHop`/`DTNNextHop` 在其上跑 Dijkstra。仍缺的深空级能力：链路度量
只有 RTT（无带宽/丢包维度）、拓扑广告未签名、路径在每一跳重算而非源路由固定。

**UDP 数据报平面与 NAT 打洞已上线（Periapsis）。** `internal/bus/udp.go` 提供
AES-256-GCM 密封信封 + HMAC 签名 punch 帧的第二条传输；`network.peers` 的
`punch:<node-id>` 条目触发经 mesh 协调的打洞（`punch_offer`/`punch_ready`
沿链路状态图中继，TTL 限界），打通的端点成为 `sendTo` 的回退路由。反射地址
发现零外部依赖：每个节点都应答 STUN binding 请求，hello 回包还白送观测 IP。
DTN bundle 载荷已加密（v2，兼容 v1 明文验证）。仍缺：对称 NAT（少数企业级
NAT）打不通时需要 TURN 式中继兜底；contact-plan 预约窗口投递未做。

## 下一步

版本语义已于 2026-10-21 更新（README 五语同步）：**v0.0.x 打磨内核，
确保功能可用；v0.1.x 特化为真正替你做事的助理；v0.2.x 自进化、
自迭代**。下列清单按时代归组。

### v0.0.x · 内核打磨（当前）

1. **三设备真机 lab 首轮已还账**（v0.0.11-preview：macOS ↔ Windows ↔ Linux SBC
   实机委派、执行中杀节点、断线重连、消息重放、两跳流水线）；剩余欠账：
   项目包跨设备全链路（记忆内联 + 产出收回）、全新设备的 LAN 发现→指纹
   准入→钉住冷启动一轮。
2. **actuator 真机点火**：舵机（gpiozero）、麦克风（arecord）、摄像头、串口 MCU
   各在一个真硬件上过一遍 `panda ask` → `action_spec` → 驱动链路；computer-use
   链路在带 GUI 会话的真机上跑一遍「截屏 → agent 看图 → gui_input 点击 →
   产物回传预览」闭环。
3. **语音入口全链路**真机跑一遍（唤醒 → 转写 → 派任务 → 朗读），以及项目跨
   设备旅行专项——工作树/context 中继层已随 v0.0.11-preview 实机过，项目包
   端到端仍未单独验。
4. 信任面收尾：P2-8 per-node 密钥分发/准入（收口「未知来源同意」路径）；
   P2-9 审计链头外部存证。
5. 信任分区收尾（已随 v0.0.11-preview 合入，Experimental）：guarded/open
   两档的行为、签名 `task_accept` 与信誉路由需在真实多机上过一轮再谈
   转正；会话树/自动压缩/`panda rpc`/订阅 OAuth 已随 v0.0.10 合入
   （Experimental），真实模型与真实订阅的验证仍欠一轮。
6. i18n 收尾、链路度量带宽/丢包维度，按需排期；记忆加固开关——external
   来源提升的整体禁用配置（约半天工程量）。

### v0.1.x · 助理特化

7. 桌面薄壳：托盘常驻、全局热键、原生通知、自更新、三平台安装包与
   macOS 公证（技术选型倾向 Wails，复用 `webui/app` + `webui/panel` +
   `internal/updater`）——Stage 3。
8. 织密网一层：**scout→plan 两阶段协议**（先探测代码库、拿报告再动态
   拆解派发）——消息板是它的通信基座，拆解协议本身仍待设计（见消息板
   设计 §8）；对称 NAT 的 TURN 式
   中继兜底与 contact-plan 预约窗口投递；消息板欠账（真实三设备互发、
   MCP 主动拉板、webui 板视图、通信效率遥测——消息数/陈旧率/预算命中率）。
9. 「在场」最小诚实版：`UNRESOLVED.md` 未解决清单 × dream 周期碰撞窗口
   × 开口权——提案只进队列/面板提案区，沉默是默认态，开口是例外。
10. 可商用边界声明：P2-8 / 审批门污点追踪 / P2-9 若延后，须在 release
    notes 如实写边界——可商用 ≠ 无限制，是「边界写清楚的可用」。

### v0.2.x · 自进化（前置先行）

11. **自进化二期**：架构稿（updater 的 verify→swap 管线与 self-build 的
    build→gate→swap 管线在架构上共存，fail-closed 验签与自修改产物天然
    互斥；node-signed self-build 作为第三类 provenance；永远保留最近一份
    release 签名二进制作兜底）已随 v0.1.0 落地 Phase 0–4，`self_evolution`
    默认关闭。二期欠账：补丁集跨节点流转与逐跳 provenance 签名链、
    `self_evolution.mode` 放开到 auto（propose ≥10 个无事故 merge 且 0 次
    swap 回退）、macOS 稳定签名身份方案、swap 后启动即崩的自动回退。
    设计稿（内部本地档）：`author/docs/plans/design-self-evolution.md`。

### 工程债（无独立版本归属）

12. 控制台前端收敛：样式层 1.6 万行、8 档宽度断点，`.btn` 在三个样式
    文件里重复定义，`.input`/`.card`/`.badge`/`.modal` 在两个里重复。
    共享 primitive 层（表单与 overlay 组件）与重复选择器的 lint 腿
    都还没做。
13. 分布式故障注入脚本化：断链、agent 中途被杀、结果回传前断链、
    重放旧 attempt、伪造 sender 这几类目前只有单测覆盖，没有可在真机上
    跑的注入器。
14. 首次运行的连通性自检：`panda init` 填完 provider 凭据后不验一次
    真实调用（1 token + RTT），凭据错误要等到第一次派任务才暴露；交互式
    凭据挽救卡片同样没做。
15. `claude_code` 适配器的凭据注入仍按适配器名硬编码（
    `internal/commander/inject.go`：只有指向 DeepSeek 端点时才注入），
    与其他适配器的 provider 判定口径不一致。
