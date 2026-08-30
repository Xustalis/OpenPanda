# 安装 OpenPanda

三条路径都能在 macOS / Linux / Windows 上得到**一致的体验**：一个 `panda` 可执行文件 + 智能体适配器（`adapters/*.py`），装在用户目录、免 root。

| 平台 | 方式 |
|---|---|
| macOS | `curl … \| sh`（或 Homebrew） |
| Linux | `curl … \| sh` |
| Windows | PowerShell 一键脚本 |

安装器会做四件事：**下载**对应平台/架构的 release 包 → **SHA-256 校验** → **解压到前缀目录** → **软链/加入 PATH**；随后（交互式）询问是否**注册开机自启服务**。

## 1. macOS / Linux 一键安装

```bash
curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh | sh
```

等效的显式写法：

```bash
sh scripts/install.sh --version 0.0.7           # 安装指定版本（默认 latest）
sh scripts/install.sh --prefix /opt/openpanda  # 自定义安装目录
sh scripts/install.sh --yes                    # 额外注册开机自启（不询问）
sh scripts/install.sh --no-service             # 不碰开机自启
```

默认前缀（与 Go 的 XDG/用户目录约定对齐）：

```
${XDG_DATA_HOME:-~/.local/share}/openpanda/
├── bin/panda            # 真实二进制
├── adapters/*.py        # 智能体适配器（daemon 委派任务必需）
├── config.example.yaml
└── capabilities.example-*.yaml
```

`~/.local/bin/panda` 是指向真实二进制的软链。若当前 shell 的 PATH 里还没有 `~/.local/bin`，脚本会像 `panda install` 一样把带标记的 `export PATH` 块写进 shell 启动文件（`~/.zshrc`、`~/.bashrc` 等；无任何 rc 文件时创建 `~/.profile`），新开终端即生效；`panda uninstall` 会精确移除这个标记块。

## 2. macOS Homebrew

```bash
brew tap Xustalis/openpanda
brew install openpanda
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
powershell -ExecutionPolicy Bypass -File .\install.ps1 -Version 0.0.7 -Yes
```

安装到 `%LOCALAPPDATA%\OpenPanda\`，并把 `bin` 加入**用户 PATH**（持久化）。交互式运行时询问是否注册**登录计划任务**（`schtasks /SC ONLOGON`）在后台跑 `panda daemon`。

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

- **白名单删除**：只删 OpenPanda 自有产物（二进制、PATH 标记块、config.yaml、数据库与运行数据）；`memory/`、`projects/`、`skills/` 等用户资产默认**保留**并在清单中标注。一键脚本/self-update 安装的发行目录（`bin/`、`adapters/`、示例配置）也会一并清扫——Linux 上与存储同根时只动发行文件、保留数据；Homebrew 安装则提示用 `brew uninstall openpanda`，源码 checkout（有 `go.mod`/`.git`）不会被误删。
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

卸载后可用 `panda doctor` 复核环境（应提示 `panda` 不再可用）。开机自启如需彻底移除，参考上文「手动控制」的停用命令。

## 发布一个新版本

1. 变更合入 `main`，打标签：`git tag v0.0.7 && git push origin v0.0.7`（注意：CHANGELOG 必须先有该版本章节，否则 release 流水线会拒绝发布）
2. `.github/workflows/release.yml` 自动跨平台构建 → 打包 `.tar.gz`/`.zip` → 生成 `checksums.txt` → 发布 GitHub Release。
3. 落地后可用：
   - 项目 README / `docs/install.md` 里的一键脚本直接装到最新版；
   - Homebrew 用户 `brew upgrade openpanda`；发布流程会生成带固定 SHA-256 的配方并同步到 tap。

## 疑难排查

- **`panda not found`**：新开终端使 PATH 生效，或手动 `export PATH="$HOME/.local/bin:$PATH"`。
- **校验失败**：可能是下载被代理/断点续传破坏，重跑即可（脚本会用全新临时目录）。
- **不支持的系统/架构**：脚本会明确报错；目前发布 `darwin/linux/windows` 的 `amd64` 与 `arm64`。
- **daemon 起不来**：先 `panda doctor` 与 `panda init`；回环监听会自动生成临时 token，但对外监听且未配置 `shared_secret` 会拒绝启动（安全约束）。
