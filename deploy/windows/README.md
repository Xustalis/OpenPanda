# Windows 部署

交叉编译产物与脚本引用的路径不同名，需在 Windows 上改名：

```powershell
# 在 Mac / Linux 上交叉编译
make build-windows-amd64        # → bin/panda-windows-amd64.exe
```

把 `panda-windows-amd64.exe` 拷到 Windows 后**改名为 `panda.exe`**，放到 `C:\panda\`。两个启动脚本都写死了这个绝对路径，改名是必须的，否则双击/静默启动会找不到二进制。

```
C:\panda\
├── panda.exe                  ← 交叉编译产物，改名后放这里
├── config.yaml                ← 本机生成，不随仓库分发
├── capabilities.yaml          ← 本机生成，不随仓库分发
├── start-panda.cmd            ← 随仓库分发：前台启动
└── start-panda-hidden.vbs     ← 随仓库分发：wscript 静默启动
```

这个目录里只有三个脚本是 tracked 的。`config.yaml` 与 `capabilities.yaml` 被
`.gitignore` 排除，是维护者本机的真实配置（含真实共享密钥与内网地址），clone
仓库拿不到它们。新机器请先跑 `panda init` 生成自己的 `config.yaml`，或手写一份；
`network.shared_secret` 自己生成随机值，不要复用示例或别人文件里的值——它保护整张
mesh，边界见 [`SECURITY.md`](../../SECURITY.md)。能力卡按这台机器实际可用的执行器写。

## 网络安全约束

OpenPanda 不再是「默认明文 WebSocket」。自 v0.0.11 起，两端在 hello 里都宣告
`sessaead` 能力且有共享密钥时，握手后会武装逐连接 AES-256-GCM（密钥由共享密钥与
两端 hello nonce 经 HKDF 派生、绑定两端身份），此后每个应用帧都密封传输；UDP
数据报平面全程 AES-256-GCM 封装，DTN bundle v2+ 载荷同样加密并签名。细节以
[`SECURITY.md`](../../SECURITY.md) 的「传输加密边界」为准。

只有协商不出会话加密的链路才走明文门禁（`cleartextOK`，握手后判定）：

- `ws://` 只允许拨向 loopback、Tailscale 网段（`100.64.0.0/10`、
  `fd7a:115e:a214::/48`、`*.ts.net`）或 `network.allow_cleartext_for` 里列出的
  网段/主机；入站明文按来源 IP 同样裁决。
- 其余目标必须写成 `wss://`（由 Caddy / nginx 之类的反向代理终结 TLS）、`punch:`
  对端（UDP 平面），或显式设置 `network.allow_cleartext`。跨公网的 Windows 节点
  按这一条配置，不要靠「可信局域网」的口头约定。

`network.shared_secret` 既是成员鉴权，也是 `sessaead` 会话密钥的派生输入，但它
只认「知道同一个密钥」，不提供对端身份绑定（那是 Ed25519 hello 的事），也不能
替代 TLS 终结。把它写进 `C:\panda\config.yaml` 后不要随目录一起分发。

> Windows 上还要注意两处与网络栈有关的差异：系统自带的 curl 走 Schannel 且强制
> 校验证书吊销，CRL/OCSP 不可达时会报 `CRYPT_E_NO_REVOCATION_CHECK (0x80092012)`，
> 安装脚本已加 `--ssl-no-revoke`；Go 的 `ProxyFromEnvironment` 不读「Internet 选项」
> 里的系统代理，需要在环境变量里显式给 `HTTPS_PROXY`。
