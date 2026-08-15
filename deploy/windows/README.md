# Windows 部署

交叉编译产物与脚本引用的路径不同名，需在 Windows 上改名：

```powershell
# 在 Mac / Linux 上交叉编译
make build-windows-amd64        # → bin/panda-windows-amd64.exe
```

把 `panda-windows-amd64.exe` 拷到 Windows 后**改名为 `panda.exe`**，连同 `config.yaml`、`capabilities.yaml` 一起放到 `C:\panda\`：

- `config.yaml` — 本机配置（含 `network.shared_secret`；该文件已被 gitignore）
- `capabilities.yaml` — 本机能力卡
- `start-panda.cmd` — 前台启动
- `start-panda-hidden.vbs` — wscript 静默启动（隐藏窗口，计划任务/登录自启用）

两个启动脚本都引用 `C:\panda\panda.exe`，所以改名是必须的，否则双击/静默启动会找不到二进制。
