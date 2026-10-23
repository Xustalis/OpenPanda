# 安装 OpenPanda

三条路径都能在 macOS / Linux / Windows 上得到**一致的体验**：一个 `panda` 可执行文件 + 智能体适配器（`adapters/*.py`）+ 参考执行器驱动（`drivers/panda-*`），装在用户目录、免 root。

| 平台      | 方式                         |
| ------- | -------------------------- |
| macOS   | `curl … \| sh`（或 Homebrew） |
| Linux   | `curl … \| sh`             |
| Windows | PowerShell 一键脚本            |

安装器会做五件事：**下载**对应平台/架构的 release 包 → **SHA-256 校验** → **验签**（`checksums.txt.sig`，本机 openssl 支持 Ed25519 时）→ **解压到前缀目录** → **软链/加入 PATH**；随后（交互式）询问是否**注册开机自启服务**。

## 1. macOS / Linux 一键安装

```bash
curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh | sh
```

等效的显式写法：

```bash
sh scripts/install.sh --version 0.1.0           # 安装指定版本（默认 latest，即最新稳定版；预发布版必须显式指定）
sh scripts/install.sh --prefix /opt/openpanda  # 自定义安装目录
sh scripts/install.sh --lite                   # lite 构建：无内嵌 Web 控制台/TUI（仅 Linux）
sh scripts/install.sh --yes                    # 额外注册开机自启（不询问）
sh scripts/install.sh --no-service             # 不碰开机自启
sh scripts/install.sh --force                  # 强制安装：跳过“版本低于数据目录 schema”的拒绝
```

当前稳定版是 `0.1.0`；上一支稳定版 `0.0.11` 仍可按版本号显式安装（`--version 0.0.11`），预发布版同理，`latest` 永远只指最新稳定版。

默认前缀（与 Go 的 XDG/用户目录约定对齐）：

```
${XDG_DATA_HOME:-~/.local/share}/openpanda/
├── bin/panda            # 真实二进制
├── adapters/*.py        # 智能体适配器（daemon 委派任务必需）
├── drivers/panda-*      # 参考执行器驱动（舵机/麦克风/摄像头/串口/截屏/GUI 输入）
├── config.example.yaml
└── capabilities.example-*.yaml
```

`~/.local/bin/panda` 是指向真实二进制的软链。若当前 shell 的 PATH 里还没有 `~/.local/bin`，脚本会像 `panda install` 一样把带标记的 `export PATH` 块写进 shell 启动文件（`~/.zshrc`、`~/.bashrc` 等；无任何 rc 文件时创建 `~/.profile`），新开终端即生效；`panda uninstall` 会精确移除这个标记块。

### 版本降级保护与发布验签

**降级保护**：二进制的迁移上限低于数据目录的 `user_version` 时，装上去会直接死在启动阶段（`schema version N is newer than this binary's highest migration`），数据目录随之搁浅。安装器因此会在**解包之后、覆盖前缀之前**用新二进制的 `panda version --json`（`schema` = 该版本支持的迁移上限，`db_schema` = 当前数据目录的版本）做比对，更旧的版本一律拒绝；`--force`（或 `OPENPANDA_FORCE=1`）用于有意的降级。`panda install` 与 `panda update apply` 有同一道守卫。

**发布验签**：`checksums.txt` 与安装包同源下载，SHA-256 只能防传输损坏，防不住"两个文件一起被换掉"。真正的信任根是 Ed25519 签名 `checksums.txt.sig`——自更新器（`panda update apply`）在替换二进制前**强制**验签。安装器在本机 openssl 支持 Ed25519（`pkeyutl -rawin`，OpenSSL ≥ 1.1.1）时同样验签，签名不符直接中止；macOS 自带的 LibreSSL 3.3 不支持 Ed25519，此时降级为一条明确警告（SHA-256 仍已通过）。`OPENPANDA_REQUIRE_SIG=1` 把"无法验签"也当作失败，`OPENPANDA_SKIP_SIG=1` 显式跳过。Windows 的 `install.ps1` 无法验签（.NET/PowerShell 5.1 无 Ed25519），只做 SHA-256。

### 受限网络下的安装与升级

能连 `api.github.com` 不等于能连发布资产——GitHub 把 `releases/download/…` 302 到 `release-assets.githubusercontent.com`，这个域名在部分网络里不通，于是 `panda update check` 看得见新版本、`panda update apply` 却死在下载阶段。两个工具的症状不同，处置也不同：

- **自更新器**：报 `fetch checksums: … net/http: TLS handshake timeout`。用 `OPENPANDA_UPDATE_BASE` 把下载指向镜像，值就是"到版本目录为止"的完整 URL：

  ```powershell
  $env:OPENPANDA_UPDATE_BASE = "https://<镜像>/Xustalis/OpenPanda/releases/download/v0.1.0"
  panda upgrade
  ```

  安装脚本认的 `OPENPANDA_RELEASE_BASE` 是同一语义的别名，设一次两边都吃。走代理也行，但 Go 的 `ProxyFromEnvironment` 只读 `HTTP_PROXY`/`HTTPS_PROXY` 环境变量，不读 Windows「Internet 选项」里的系统代理——Clash 一类工具停在「系统代理」模式时，`panda` 拿不到它。

- **Windows 安装脚本**：报 `curl: (35) schannel: … CRYPT_E_NO_REVOCATION_CHECK (0x80092012)`。这是本机 curl 使用 Schannel 后端、而证书吊销列表（CRL/OCSP）地址不可达，**不是**证书本身有问题；脚本在探测到该能力时自动补 `--ssl-no-revoke`，之后还有 `Invoke-WebRequest` 兜底。手工确认：

  ```powershell
  curl.exe -sS --ssl-no-revoke -o NUL -w "%{http_code}`n" https://api.github.com/repos/Xustalis/OpenPanda/releases/latest
  ```

镜像只改变传输路径，不改变信任：`checksums.txt`、Ed25519 签名与压缩包三者仍必须互相对应。

### Lite 构建：树莓派与纯命令行设备

```bash
sh scripts/install.sh --lite                  # linux-amd64/arm64/armv7
```

lite 包（`panda-<ver>-lite-linux-<arch>.tar.gz`）去掉了内嵌 Web 控制台与 Bubble Tea TUI，保留 daemon、mesh、DTN、队列与 `panda ask`/`repl`（经典行界面）——二进制更小、无前端依赖链，适合树莓派、armv7 老派（32 位 ARM 仅此目标）与跑不起/不需要 Web 界面的命令行机器。在 lite 节点上打开 `panda web` 会得到一个提示页；管理界面由网络中任一台完整构建的节点提供即可。源码构建对应 `make build-lite` 与 `build-lite-linux-{amd64,arm64,armv7}`。

## 2. macOS Homebrew

```bash
brew install Xustalis/openpanda/openpanda
```

配方 `deploy/homebrew/openpanda.rb` 从 GitHub Release 拉取二进制与适配器，自适应 Apple Silicon（arm64）与 Intel（amd64）。

## 3. Windows 一键安装

在 **PowerShell (5.1+)** 里执行：

```powershell
Set-ExecutionPolicy -Scope Process Bypass
irm https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.ps1 | iex
```

或下载后运行：

```powershell
powershell -ExecutionPolicy Bypass -File .\install.ps1 -Version 0.1.0 -Yes
powershell -ExecutionPolicy Bypass -File .\install.ps1 -Force    # 强制降级（跳过 schema 守卫）
```

安装到 `%LOCALAPPDATA%\OpenPanda\`，并把 `bin` 加入**用户 PATH**（持久化）。交互式运行时询问是否注册**登录计划任务**（`schtasks /SC ONLOGON`）在后台跑 `panda daemon`。参数与 `install.sh` 一一对应：`-Version` / `-Prefix` / `-Yes` / `-NoService` / `-Force`，环境变量 `OPENPANDA_*` 同样生效。

## 4. 从源码 / Go

```bash
# 源码构建（需要 Go 1.26+；先 make web 以嵌入 Web 控制台）
make build            # → bin/panda
go install github.com/Xustalis/OpenPanda/cmd/panda@latest
```

## 安装后

```bash
panda init      # 交互式生成 config.yaml 与能力卡
panda repl      # 进入交互命令行
panda web       # 打开内嵌 Web 控制台（回环监听、自动登录）
panda doctor    # 自检：二进制 / PATH / 配置 / 适配器 / agent 是否就绪
```

> 开机自启服务以 `panda daemon` 启动（不带 `--config`/`--card`）：daemon 与交互运行一样自动发现配置——`OPENPANDA_CONFIG_PATH` 环境变量 → 用户级配置（macOS `~/Library/Application Support/openpanda/config.yaml`，Linux `~/.config/openpanda/config.yaml`，即 `panda init` 的写入位置）→ `/etc/openpanda/config.yaml`；能力卡按工作目录与配置同目录查找。因此请先 `panda init` 生成配置，否则 daemon 会以默认配置运行且无能力卡（无法执行任务）。

## 开机自启：手动控制

- **macOS**（LaunchAgent，用户级，免 sudo）：

  ```bash
  launchctl unload ~/Library/LaunchAgents/com.openpanda.node.plist   # 停用
  launchctl load   ~/Library/LaunchAgents/com.openpanda.node.plist   # 启用
  ```

- **Linux**（systemd 用户单元）：

  ```bash
  systemctl --user disable --now openpanda.service   # 停用
  systemctl --user enable  --now openpanda.service   # 启用
  ```

- **Windows**（登录计划任务）：

  ```powershell
  schtasks /Delete /TN OpenPandaNode /F        # 停用并删除
  ```

## 卸载

统一入口是 `panda uninstall`（三个平台一致），不再手动删目录：

```bash
panda uninstall             # 扫描 → 列出删除/保留清单 → 输入 confirm 确认 → 自动备份 → 白名单删除
```

- **白名单删除**：只删 OpenPanda 自有产物（二进制、PATH 标记块、config.yaml、数据库与运行数据）；`memory/`、`projects/`、`skills/` 等用户资产默认**保留**并在清单中标注。一键脚本/self-update 安装的发行目录（`bin/`、`adapters/`、`drivers/`、示例配置）也会一并清扫——Linux 上与存储同根时只动发行文件、保留数据；Homebrew 安装则提示用 `brew uninstall openpanda`，源码 checkout（有 `go.mod`/`.git`）不会被误删。

- **自动备份**：删除前把配置与数据打包成 `~/openpanda-backup-<时间戳>.zip`，可回滚（`--no-backup` 跳过）。

- **`--dry-run`**：只打印计划，不删任何东西；**`--yes`**：脚本化运行跳过确认。

### 连用户数据一起删：`--purge`

```bash
panda uninstall --purge
```

`--purge` 在第一层确认之外还要求**二次确认**（输入 `purge`）：确认后 `memory/`、`projects/`、`skills/`、work 目录等用户级资产会连同备份一起删除（备份 zip 中包含它们，仍可解回）；拒绝则**什么都不删**。配合 `--yes` 时跳过两层确认，请谨慎使用。

### 仅备份不删除：`--backup-only`

```bash
panda uninstall --backup-only
```

只执行备份（写 `~/openpanda-backup-<时间戳>.zip`），不删除任何文件，适合升级前留档。

卸载后可用 `panda doctor` 复核环境（应提示 `panda` 不再可用）。

`panda uninstall` 会**顺带停用开机自启**：macOS 注销 LaunchAgent（`launchctl bootout`）、Linux 停用用户 systemd 单元（`systemctl --user disable --now`）、Windows 删除 `OpenPandaNode` 登录计划任务（`schtasks /Delete`）。若自启是你按上文手动注册的（例如换了服务名），它不在清理范围内，此时再用「手动控制」里的停用命令删一次。

## 发布一个新版本

1. 变更合入 `main`，确认该 commit 的 **Gate 已经跑绿**，再打标签：`git tag v0.1.0 && git push origin v0.1.0`（注意两点：CHANGELOG 必须先有该版本章节，否则 release 流水线会拒绝发布；release 流水线会向 Gate 查验**同一个 commit** 的结论，没有 push 触发的 Gate 运行记录、或结论不是 success，都会直接拒绝发布并报错）。
2. `.github/workflows/release.yml` 自动跨平台构建 → 打包 `.tar.gz`/`.zip` → 生成 `checksums.txt` → 发布 GitHub Release。
3. 预发布（带连字符的标签，如 `v0.0.9-beta`、`v0.0.8-alpha`、`v1.0.0-rc1`）会自动标记为 **pre-release**：它不会成为仓库的 Latest，`releases/latest` 仍指向最后一个稳定版，一键安装脚本也只装稳定版；同时**不会**同步 Homebrew tap。
4. 落地后可用：

   - 项目 README / `docs/install.md` 里的一键脚本直接装到最新**稳定**版（要装预发布版请显式指定，例如 `--version 1.0.0-rc1`）；

   - Homebrew 用户 `brew upgrade openpanda`；仅**正式版**会生成带固定 SHA-256 的配方并同步到 tap。

## 疑难排查

- **`panda not found`**：新开终端使 PATH 生效，或手动 `export PATH="$HOME/.local/bin:$PATH"`。

- **校验失败**：可能是下载被代理/断点续传破坏，重跑即可（脚本会用全新临时目录）。

- **不支持的系统/架构**：脚本会明确报错；目前发布 `darwin/linux/windows` 的 `amd64` 与 `arm64`，另有 lite 构建的 `linux-amd64/arm64/armv7`（32 位 ARM 仅 lite）。

- **daemon 起不来**：先 `panda doctor` 与 `panda init`；`network.shared_secret` 为空时 daemon 会自动铸造并写回配置（`panda init` 也会预先生成）——只有铸造本身失败才不起 WS 监听，节点退为单机运行（fail-closed，不接受未认证对端）。若节点此前已入过 mesh 而 secret 丢失，daemon 会高声告警而不是静默换密钥。

- **跨公网/NAT 的对端连不上**：双方都拨不通对方时，在 `network.peers` 里配置 `punch:<node-id>`（对端节点名）——打洞协调经 mesh 内已连通的节点中继，双方 UDP 端口需能出站（无需入站映射）；对称 NAT 等极端场景打不通时会持续重试，日志里有 `punch offer failed`/`udp:` 前缀可查。`network.udp_listen: "off"` 可整体关闭数据报平面。

