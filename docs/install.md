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
sh scripts/install.sh --version 0.0.3          # 安装指定版本（默认 latest）
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

`~/.local/bin/panda` 是指向真实二进制的软链（加入 PATH）。若你的 shell 路径里没有 `~/.local/bin`，脚本会提示你补一行 `export PATH`。

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
powershell -ExecutionPolicy Bypass -File .\install.ps1 -Version 0.0.3 -Yes
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

> 开机自启服务会以 `panda daemon --config <prefix>/config.yaml --card <prefix>/capabilities.yaml` 启动，因此请先 `panda init` 生成配置，否则 daemon 会因缺少 `config.yaml`（或未配置 `shared_secret`）拒绝启动。

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

- macOS / Linux：删除前缀目录与软链（并参考上文停用开机自启）：
  ```bash
  rm -rf ~/.local/share/openpanda ~/.local/bin/panda
  ```
- Windows：删除 `%LOCALAPPDATA%\OpenPanda` 并在用户 PATH 中移除对应 `bin` 目录。

## 发布一个新版本

1. 变更合入 `main`，打标签：`git tag v0.0.3 && git push origin v0.0.3`
2. `.github/workflows/release.yml` 自动跨平台构建 → 打包 `.tar.gz`/`.zip` → 生成 `checksums.txt` → 发布 GitHub Release。
3. 落地后可用：
   - 项目 README / `docs/install.md` 里的一键脚本直接装到最新版；
   - Homebrew 用户 `brew upgrade openpanda`；发布流程会生成带固定 SHA-256 的配方并同步到 tap。

## 疑难排查

- **`panda not found`**：新开终端使 PATH 生效，或手动 `export PATH="$HOME/.local/bin:$PATH"`。
- **校验失败**：可能是下载被代理/断点续传破坏，重跑即可（脚本会用全新临时目录）。
- **不支持的系统/架构**：脚本会明确报错；目前发布 `darwin/linux/windows` 的 `amd64` 与 `arm64`。
- **daemon 起不来**：先 `panda doctor` 与 `panda init`；回环监听会自动生成临时 token，但对外监听且未配置 `shared_secret` 会拒绝启动（安全约束）。
