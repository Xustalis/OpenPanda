# Security

OpenPanda 是一个运行在你自己设备上的 P2P 任务编排内核。本文说明它的信任模型、
部署红线与漏洞上报渠道。**先读这里，再把任何节点接入网络。**

## 信任模型

- **全网单一共享密钥。** 节点间总线的唯一凭据是 `shared_secret`
  （`OPENPANDA_SHARED_SECRET`）。`hello` 的身份签名是
  `HMAC-SHA256(secret, nodeID + ":" + ts)`，并绑定时间戳限制重放窗口
  （见 [`docs/protocol.md`](docs/protocol.md)）。**持有密钥的节点即完全受信任**——
  它可以伪造 `Authorized`，也就是给自己批准 tier-2（不可逆）操作。因此
  「不可逆操作必须我审批」这条约束只在**单机与可信内网**成立；网络里一旦出现
  你不完全信任的节点就不再成立。
- **传输不加密。** 总线是纯 `ws://`，HMAC 只**认证对端**、不**加密载荷**。
  任务意图、结果、工件都按明文在链路上传输。
- **审计链无密钥哈希。** `task_events` 的哈希链是无密钥 SHA-256：能写数据库
  就能重算整条链。它与共享密钥同属「密钥分发」问题，见 `docs/status.md` 已知限制
  （P2-8 / P2-9）。

由此推论：**每个节点视为同等完全信任，任一节点失陷即全网失陷。**
不要把 mesh 扩展到不完全受控的机器。

## 部署红线

在补齐 per-node 密钥 / 可验签授权（那是改信任模型、不是打补丁）之前，必须遵守：

1. **只走加密 overlay 互联。** 节点间通过 Tailscale / WireGuard 等加密网络相连，
   `listen_addr` 绑定 overlay 网卡；**绝不把总线端口直接暴露到公网或不可信局域网**。
   若某段链路确要跨公网，用反向代理终结 TLS，并把 peer 写成 `wss://host:port`。
2. **密钥只经环境变量注入。** `shared_secret` 只用 `OPENPANDA_SHARED_SECRET`
   提供，不写进 `config.yaml`，不进任何公共仓库。生成示例：`openssl rand -hex 32`。
3. **不给未知远端声明 Tier-2 能力。** `capabilities.yaml` 中的不可逆（tier-2）
   能力只授予你完全信任的节点。
4. **面板保持回环。** 控制台 / 面板（`panel_addr`）只绑 `127.0.0.1`；
   非回环绑定在未配置 `panel_token` 时会拒绝启动。不要把带 token 的自动登录
   URL 粘贴到聊天 / 工单系统。
5. **默认回环监听。** 总线默认监听回环地址；对外监听且未配置 `shared_secret`
   时会拒绝启动（安全约束，见 `docs/install.md`）。

## 漏洞上报

不要为传输认证、权限 Tier 模型或密钥脱敏层的缺陷开公开 issue。请**私下**联系
维护者上报（联系方式见仓库设置中的 security contact），并附复现步骤。
详见 [CONTRIBUTING.md](CONTRIBUTING.md) 的 “Reporting security issues”。
