# Security

简体中文（为准）· [English](SECURITY.en.md) · [日本語](SECURITY.ja.md) · [Español](SECURITY.es.md) · [Deutsch](SECURITY.de.md)

OpenPanda 是一个运行在你自己设备上的 P2P 任务编排内核。本文说明它的信任模型、
部署红线与漏洞上报渠道。**先读这里，再把任何节点接入网络。**

## 信任模型

- **全网单一共享密钥（P2-8，已部分缓解）。** 节点间总线的唯一成员凭据是
  `shared_secret`（`OPENPANDA_SHARED_SECRET`）。`hello` 的身份签名是
  `HMAC-SHA256(secret, nodeID + ":" + ts + ":" + nonce)`，绑定时间戳与每拨号
  nonce 限制重放（见 [`docs/protocol.md`](docs/protocol.md)）。在此之上，每个
  节点持有持久化的 Ed25519 身份密钥：tier-2（不可逆）同意以
  `Ed25519(taskID, authorized, ts, ConsentDigest)` 签名授权随任务传递，执行端按
  目录中记录的来源节点公钥验签（`internal/core/nodekey.go`），签名被剥离或篡改
  一律拒绝。`panda nodes verify` 可把节点指纹人工钉住，钉住后的换钥 hello 会被
  直接拒绝。**仍存的缺口**：共享密钥成员仍可为「接收端从未见过其公钥的节点」
  伪造同意（多跳中继 + TOFU 的固有边界，该路径会打印警告），未钉住的身份按
  首次见到即信任处理。因此「不可逆操作必须我审批」这条约束只在**单机与可信
  内网**成立；网络里一旦出现你不完全信任的节点就不再成立。
- **传输加密边界。** v0.0.11 起，双方在 hello 里宣告 `sessaead` 能力且存在
  共享密钥时，连接在握手后武装逐连接 AES-256-GCM——密钥由共享密钥与两端
  hello nonce 经 HKDF 派生，绑定两端身份，逐拨号更新；此后每个应用帧密封
  传输，链路上的观察者读不到任务面也伪造不了帧。协商不出会话加密的链路
  才过明文门禁（握手后判定，`cleartextOK`）：`ws://` 只允许拨向 loopback /
  Tailscale（100.64.0.0/10、`fd7a:115e:a214::/48`、`*.ts.net`）或
  `network.allow_cleartext_for` 列出的网段/主机，其余目标必须 `wss://`
  （反向代理终结 TLS）、`punch:` 对端（UDP 平面）或显式
  `network.allow_cleartext`；入站明文同理按来源 IP 裁决。UDP 数据报平面
  全程 AES-256-GCM 封装（密钥由共享密钥域分离派生）；DTN bundle v2+ 载荷
  同样 AES-256-GCM 加密并签名，篡改的 bundle 不会被投递。**明文链路只允许
  出现在已由底层（本机 / WireGuard / Tailscale / TLS 代理）加密的路径上，
  或由 `sessaead` 在握手后自行武装。**
- **审计链已签名，锚点在库内。** `task_events` 与 `audit_log` 的哈希链逐行携带
  Ed25519 签名（P2-9）；验证时绑定本节点公钥（换钥重签会被拒绝），且已签名链
  不允许再出现未签名行（防尾部剥离）。但签名锚点在数据库本身：**从未签过名的
  历史前缀无法与「被整体重写」区分**，进一步收紧需要外部存证（导出/公证链头），
  仍在路线图上。
- **自更新经 Ed25519 分离签名验证。** 官方发布通道自 v0.0.11-preview 起为
  `checksums.txt` 附带 `checksums.txt.sig`（对校验和文件的分离签名）。官方
  发布公钥内置于每个二进制（`internal/version.ReleasePubKey` 源码默认值，
  release 打包亦可经 ldflags 轮换），与压缩包同信道送达的伪造 checksums
  会被直接拒绝：

  `a84a556bd45ee7743437d7b9b0c4f6e0df34c496f639b5a045d5de484f128560`

  没有内置公钥的旧构建（v0.0.11-preview 之前的自编译产物）默认拒绝更新：
  设 `OPENPANDA_UPDATE_PUBKEY=<上面的公钥>` 可带验签升入签名通道，或显式
  `OPENPANDA_UPDATE_UNSIGNED=1` 一次性退回 checksum-only（哈希与压缩包同
  信道，仅作逃生）。`OPENPANDA_UPDATE_REPO` 指向自托管发布通道时，
  用 `OPENPANDA_UPDATE_PUBKEY` 配置该通道自己的公钥。
- **技能 URL 导入有出站边界。** `panda skill install <url>` 及对应面板 / MCP
  路径只允许 https（环回地址除外），且拨号时拒绝环回 / 私网 / 链路本地等
  保留地址，防止把节点变成内网探测跳板。经 `skills.hub_url` 配置的私有 hub
  不受此限——那是管理员自己的部署选择。

由此推论：**默认同等完全信任——缺省下每个节点视为 mesh 档，任一节点失陷
即全网失陷。** 不要把 mesh 扩展到不完全受控的机器。v0.0.11 起可按对端
身份钉信任档（`network.peers[].trust` / `panda nodes trust`，experimental）：
`guarded` 档钳制发向它的委派暴露面并按 `network.trust.accept.guarded` 审议
它起源的工作，`open` 档只存在于目录、不收发推送态委派——信任档是本机的
单方面断言，能收窄你对某个对端的暴露，但不改变「持有共享密钥即为成员」
这一底层事实。截屏任务与它的输出工件另有更严的默认：显示内容只在 mesh
链上移动（`send.guarded.screen` 是同一所有者场景的显式开关）——执行端
认领时对 `screen_sensing` 类 actuator 运行时打标，标记随 `task_result` 逐跳
补标，产物服务侧即使面对参与者与签名 grant 也只放给 mesh 对端；信任降级
即时切断进行中的传输，不候授权缓存 TTL 期满。屏幕阶段的产物作为下游
任务的输入交接时绑定同样成立：`ArtifactRef.screen` 溯源标记由编排者从
生产者行打上，凡消费显示字节的任务自身判为 display-bound，标记沿阶段
逐级级联——伪造的标记只能收窄受众。

生命周期 hooks 是可选的本地扩展：默认关闭；子进程只继承白名单环境（不含模型凭据与对端 token），命令先过 tier 判定，被判为 tier-2 的命令默认拒绝（`hooks.allow_tier2` 显式放行），每次执行写一条审计行。任务值只通过 `PANDA_HOOK_*` 环境变量传入，模板里的 `{title}` 等占位符展开为变量引用而非字面值；Windows 上以 `cmd /V:ON` 的延迟展开传入，值不会进入命令解析器。

## 完整性校验

`panda audit verify`（或面板 `GET /api/audit`）校验全局审计链的哈希链与
Ed25519 签名；`panda audit verify --task <id>` 校验单个任务的事件链。两者都
绑定本节点公钥，签名缺失（在已签名链中）、签名剥离或换钥重签都会报错。

## 部署红线

在补齐 per-node 密钥分发 / 可验签准入（那是改信任模型、不是打补丁）之前，
必须遵守：

1. **只走加密 overlay 互联。** 节点间通过 Tailscale / WireGuard 等加密网络相连，
   `listen_addr` 绑定 overlay 网卡；**绝不把总线端口直接暴露到公网或不可信局域网**。
   `sessaead` 会话加密让可信内网的链路保密性有了底线，但它只认共享密钥、不阻止
   持有密钥者的主动攻击——暴露面仍要最小化。若某段链路确要跨公网，用反向代理
   终结 TLS，并把 peer 写成 `wss://host:port`。
2. **密钥只经环境变量注入。** `shared_secret` 只用 `OPENPANDA_SHARED_SECRET`
   提供，不写进 `config.yaml`，不进任何公共仓库。生成示例：`openssl rand -hex 32`。
3. **不给未知远端声明 Tier-2 能力。** `capabilities.yaml` 中的不可逆（tier-2）
   能力只授予你完全信任的节点。
4. **面板保持回环。** 控制台 / 面板（`panel_addr`）只绑 `127.0.0.1`；非回环绑定
   且未配置 `panel_token` 时会生成一次性临时令牌并大声警告——`/api/*` 永不裸奔，
   但明文 HTTP 下令牌可被链路上的人嗅探，长期对外必须配置稳定 token 或 TLS
   反向代理。不要把带 token 的自动登录 URL 粘贴到聊天 / 工单系统。
5. **新装默认只监听回环，对外暴露必须显式声明。** `network.exposure_mode`
   为 `loopback`（新装默认）时，总线、控制台与 UDP 数据面全部绑 `127.0.0.1`，
   LAN 发现关闭。改成 `lan` 或 `public` 之后总线才对私网 / 公网可达：UDP 数据面
   与发现随之打开，daemon 首次启动还会自动生成 `shared_secret` 并写回
   `config.yaml`，所以监听器一定会起来，不会因「未配置密钥」而留在本地。陌生
   设备能连上端口、能探到节点存在，但过不了 HMAC 成员校验、不能下发任务；持有
   该密钥即被视为全网成员，而默认信任档是 `mesh`（自动接受 `task_delegate`，
   等价于远程执行）。0.1.5 之前自己写过绑定的配置按 `legacy` 处理，端口不会
   被挪动。在有公网 IP 的机器上按「云服务器 / 公网 IP 部署」一节收窄绑定。

## 云服务器 / 公网 IP 部署

`network.exposure_mode` 是决定「谁能连到这台机器」的唯一开关，取值
`loopback`（新装默认，所有监听只在回环）、`lan`（总线在私网可达，控制台仍留
回环）、`public`（公网可达，强制稳定 `shared_secret` 与 `panel_token`，关闭
发现并拒绝明文拨号）、`legacy`（保持 0.1.5 之前写死的绑定）。加载时由它改写
`listen_addr` / `panel_addr` / `udp_listen` / `discovery_*`，所以 `panda doctor`
报告的地址与真实 socket 永远一致。已有配置若显式写过 `listen_addr` 等绑定，
会自动按 `legacy` 处理，升级不会挪动现有节点的端口。

`panel_addr` 默认 `127.0.0.1:7840`，控制台本身不对外；总线、UDP 数据面与
LAN 发现会对外。`--lan` 或 `panda service install --lan` 会把控制台也绑到
`0.0.0.0`，此时明文 HTTP 只靠一个 Bearer token 保护，token 与任务内容都能
被链路上的人嗅探，**不要直接用在公网**。

加固顺序：

1. **设 `exposure_mode`，再收窄绑定。** 云主机用 `public`（或 `lan` + overlay
   地址）；`network.listen_addr` 绑到 Tailscale / WireGuard 的 overlay 网卡
   地址；`udp_listen` 与 `discovery_addr` 设 `off`（云主机通常不需要 LAN
   发现），`discovery_auto_dial` 设 `false`。用 `ss -lntup | grep panda` 核对
   实际监听地址。
2. **安全组只放 overlay。** 入站只放行 overlay 网段与端口；7836 / 7837 /
   7840 以及预览、开发服务器的动态端口一律不对 `0.0.0.0/0` 开放。
3. **控制台走 TLS 反向代理。** `panel_addr` 保持 `127.0.0.1`，由 nginx /
   Caddy 终结 TLS 再转发；跨公线的 peer 写成 `wss://host:port`，不要用
   `ws://`。
4. **凭据与准入。** `shared_secret` 只经 `OPENPANDA_SHARED_SECRET` 注入，
   `panel_token` 配成稳定值并经环境变量提供；`panda pair` 需人工核对短配对码，
   不要用 `--yes` 跳过。非回环绑定下不要把带 token 的自动登录 URL 打进日志、
   聊天或工单。
5. **收紧信任档。** 为跨公网对端显式配置 `guarded` / `open`，不要让默认
   `mesh` 的「自动接受委派」覆盖公网链路。信任档只能收紧，不能替代密钥本身。

密钥一旦泄露，现有模型没有按设备吊销的能力：轮换 `shared_secret` 会让旧节点
全部离网。设备级授权与可验签准入属于信任模型改造，不在 0.1.x 内。

## 漏洞上报

不要为传输认证、权限 Tier 模型或密钥脱敏层的缺陷开公开 issue。请**私下**联系
维护者上报（联系方式见仓库设置中的 security contact），并附复现步骤。
详见 [CONTRIBUTING.md](CONTRIBUTING.md) 的 “Reporting security issues”。
