# v0.1.6 本地测试与发布清单

[English](release-v0.1.6.en.md) · [安装](install.md) · [项目现状](status.md)

v0.1.6 是当前源码的发布目标。`panda version` 显示 0.1.6 只证明版本字段，不能证明该源码已正式发布，也不能证明常驻进程已切换。发布日期在正式发布时填写；更新日志中的日期在此之前按计划日期理解。

## 本轮发现与修正

| 问题与触发条件 | 影响与处理 |
| --- | --- |
| 直接运行 `sh scripts/package.sh` 或 `verify-release.sh`，未指定版本 | 原默认仍为 0.1.5，可能把新源码标成旧版本；现在读取 `internal/version/version.go`，参数优先于 `VERSION` 环境变量，再回退到源码 |
| 只运行 `make install-local` | 原来可能嵌入旧 Web 资源；现在强制先 Web、再 Go，随后安装 |
| 首次安装缺少 `~/.local/bin`，或 HOME 路径包含空格 | 旧安装失败；现在创建目录并引用完整路径 |
| 本地升级仅同步二进制和适配器 | 语音脚本、驱动仍可能陈旧；现在一起同步，文件原子替换，复制失败返回失败，保留个人配置、数据和自定义辅助文件 |
| 发布校验只要求六个完整包 | 三个 lite 包缺失也可能放行；现在九个目标都必须存在并有校验和 |
| 源码预填已发布日期 | 准备版本会错误显示已发布；现在保持为空，正式切版时填写 |

真实入口冒烟还复现了字面标记被误判为源码问题；明确的原样回复现已豁免，真正的实现问题仍要求源码证据。安装器测试也隔离了服务管理命令，因为临时 HOME 无法隔离当前用户的 launchctl/systemctl。

## 安装当前源码

需要 Go 1.26.11+、Node.js/npm、Python 3。先保存正在执行的工作，再运行：

```sh
make install-local
export PATH="$HOME/.local/bin:$PATH"
command -v panda
panda version
cmp bin/panda "$(command -v panda)"
panda doctor
```

已有 `~/.local/share/openpanda/bin` 时，二进制写入该目录，`~/.local/bin/panda` 保持软链；否则写入 PATH 目录。适配器、语音和驱动同时更新两个受支持布局。安装不修改 shell 启动文件，不重启进程，不改变节点配置或模型凭据。`panda doctor` 的第三方 CLI、模型连接或硬件失败要逐项判断，不应以单个失败推断所有执行器不可用。

## 切换常驻进程与回退

原子替换后，已经运行的进程仍执行旧映像。先用 `panda queue --state running` 确认空闲，同时检查 Web/TUI 是否仍有入口模型轮次或 Side 请求。`panda service restart` 只重启控制台服务；daemon 是另一个服务。

| daemon 所在平台 | 已注册服务的重启方式 |
| --- | --- |
| macOS | `launchctl kickstart -k "gui/$(id -u)/com.openpanda.node"` |
| Linux | `systemctl --user restart openpanda.service` |
| Windows PowerShell | `schtasks /End /TN OpenPandaNode` 后 `schtasks /Run /TN OpenPandaNode` |

只对实际已注册的服务执行对应命令。手动运行的进程应先正常退出，再从新路径启动。Web 启动后检查 `/api/version` 与前端页面；刷新可重新校验入口 HTML，版本号本身不能区分同版本源码。

覆盖安装前备份二进制与辅助脚本。回退时先停止服务，把备份复制到同目录临时文件后重命名回原路径，并恢复同一份辅助脚本。0.1.6 相对 0.1.5 不增加数据库迁移；不得用 0.1.4 或更早的二进制直接打开 schema v67 数据库。数据库需使用一致性备份，不能把运行中的 SQLite 主文件单独复制后当作完整快照。

## 建议按顺序测试

1. 在 Web 或 TUI 发一个简短问题，确认进度、回答及供应商实际返回的思考摘要；供应商不返回摘要时不要求出现思考文本。
2. 长回答时刷新，再停止；确认原消息、部分回答、停止状态保留，继续发下一轮不串消息。
3. 在临时项目执行小文件任务，检查原对话中的交付报告、真实步骤、文件路径和验证结果，随后刷新、复制、导出。未知或部分验证不能显示为全部通过。
4. 分别测试本机原生 Codex、Claude Code 或自定义中转。先独立确认 CLI 可用，再确认 Panda 的模型选择、端点及执行结果；保留原生配置，不要求换成官方密钥。
5. 双设备场景测试硬设备绑定、断线恢复和产物回收；测试失败时保存任务 ID、时间、节点、实际命令与脱敏日志。

## 发布门禁

```sh
make gate-all
sh scripts/check-docs.sh
make site-check
go mod tidy -diff
go build -tags lite ./...
make package
sh scripts/verify-release.sh
sh scripts/test-installers.sh
git diff --check
```

先完成 Web 构建，再运行打包或跨平台编译；不要在 Go 嵌入文件时并发替换 Vite 输出。正式发布要求签名密钥与校验签名；无密钥的本地包只能证明打包流程，不能作为已签官方发布。按仓库规则合并到 main，等待同一 SHA 的 Gate 通过，再创建 tag。此清单不代替发布授权。

## 剩余范围

本次安装流程与本机检查不能代替所有服务商、Windows/Linux 真机、真实多设备网络、语音与执行器硬件验收。共享密钥/TOFU 信任边界、跨会话同路径并行、UPnP/NAT-PMP、punch/dir 签名收敛和 daemon→独立面板的 mesh 指标 IPC 仍见 [已知限制](status.md#已知限制)。旧版本已删除的对话无法恢复。公网官网与 GitHub Release 的发布应单独确认。
