# Security

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
- **传输加密边界。** WebSocket 总线的帧本身不加密：hello 只认证对端。防护分三层：
  出站明文门禁默认拒绝 loopback / Tailscale（100.64.0.0/10、`fd7a:115e:a214::/48`、
  `*.ts.net`）之外的 `ws://` 拨号——其余目标必须 `wss://`（反向代理终结 TLS）、
  `punch:` 对端（UDP 平面）或显式 `network.allow_cleartext`；UDP 数据报平面
  全程 AES-256-GCM 封装（密钥由共享密钥域分离派生）；DTN bundle v2+ 载荷同样
  AES-256-GCM 加密并签名，篡改的 bundle 不会被投递。**明文链路只允许出现在已由
  底层（本机 / WireGuard / Tailscale / TLS 代理）加密的路径上。**
- **审计链已签名，锚点在库内。** `task_events` 与 `audit_log` 的哈希链逐行携带
  Ed25519 签名（P2-9）；验证时绑定本节点公钥（换钥重签会被拒绝），且已签名链
  不允许再出现未签名行（防尾部剥离）。但签名锚点在数据库本身：**从未签过名的
  历史前缀无法与「被整体重写」区分**，进一步收紧需要外部存证（导出/公证链头），
  仍在路线图上。
- **自更新支持带外签名（可选）。** 默认信任根仍是 GitHub + TLS +
  `checksums.txt` 的 SHA-256。设置 `OPENPANDA_UPDATE_PUBKEY`（Ed25519 公钥，
  hex 或 base64）后，release 必须附带 `checksums.txt.sig`——对 checksums.txt 的
  分离签名——否则更新被拒绝；`OPENPANDA_UPDATE_REPO` 可把更新指向自托管的
  发布通道。官方发布通道启用签名之前，代码侧能力已就绪。
- **技能 URL 导入有出站边界。** `panda skill install <url>` 及对应面板 / MCP
  路径只允许 https（环回地址除外），且拨号时拒绝环回 / 私网 / 链路本地等
  保留地址，防止把节点变成内网探测跳板。经 `skills.hub_url` 配置的私有 hub
  不受此限——那是管理员自己的部署选择。

由此推论：**每个节点视为同等完全信任，任一节点失陷即全网失陷。**
不要把 mesh 扩展到不完全受控的机器。

## 完整性校验

`panda audit verify`（或面板 `GET /api/audit`）校验全局审计链的哈希链与
Ed25519 签名；`panda audit verify --task <id>` 校验单个任务的事件链。两者都
绑定本节点公钥，签名缺失（在已签名链中）、签名剥离或换钥重签都会报错。

## 部署红线

在补齐 per-node 密钥分发 / 可验签准入（那是改信任模型、不是打补丁）之前，
必须遵守：

1. **只走加密 overlay 互联。** 节点间通过 Tailscale / WireGuard 等加密网络相连，
   `listen_addr` 绑定 overlay 网卡；**绝不把总线端口直接暴露到公网或不可信局域网**。
   若某段链路确要跨公网，用反向代理终结 TLS，并把 peer 写成 `wss://host:port`。
2. **密钥只经环境变量注入。** `shared_secret` 只用 `OPENPANDA_SHARED_SECRET`
   提供，不写进 `config.yaml`，不进任何公共仓库。生成示例：`openssl rand -hex 32`。
3. **不给未知远端声明 Tier-2 能力。** `capabilities.yaml` 中的不可逆（tier-2）
   能力只授予你完全信任的节点。
4. **面板保持回环。** 控制台 / 面板（`panel_addr`）只绑 `127.0.0.1`；非回环绑定
   且未配置 `panel_token` 时会生成一次性临时令牌并大声警告——`/api/*` 永不裸奔，
   但明文 HTTP 下令牌可被链路上的人嗅探，长期对外必须配置稳定 token 或 TLS
   反向代理。不要把带 token 的自动登录 URL 粘贴到聊天 / 工单系统。
5. **默认回环监听。** 总线默认监听回环地址；未配置 `shared_secret` 时监听器
   不启动，节点仅本地运行（安全约束，见 `docs/install.md`）。

## 漏洞上报

不要为传输认证、权限 Tier 模型或密钥脱敏层的缺陷开公开 issue。请**私下**联系
维护者上报（联系方式见仓库设置中的 security contact），并附复现步骤。
详见 [CONTRIBUTING.md](CONTRIBUTING.md) 的 “Reporting security issues”。
