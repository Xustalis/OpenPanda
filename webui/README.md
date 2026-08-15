# webui — 旧版 web 控制面板（保留冻结）

> **状态：保留但不再优化（frozen）。** 内核守护进程（`cmd/panda`）默认**不挂载**本面板。
> 未来规划为「内核 + desktop 客户端」，web 面板将被桌面端取代。

本目录存放原 PWA 控制面板与 Web Push 相关代码，从内核里单独抽出：

- `cmd/panel/` — 独立侧车二进制：读取与内核相同的 SQLite 库，提供任务队列/详情/审批 HTTP 接口。
- `panel/` — HTTP 处理（任务列表、详情、approve/reject、push 订阅端点）。
- `push/` — Web Push（VAPID / 消息加密 / 订阅存储）。
- `web/pwa/` — PWA 静态资源（index.html、manifest、service worker、icons）。

## 为何单独抽出

- 内核形态下，界面交给 CLI 子命令（`panda status/queue/task/cancel/approve/reject/logs`），不启动 HTTP 服务。
- 代码保留以便回看或迁移到 desktop 客户端；**不做进一步优化**。

## 仍想用浏览器面板？

单独启动侧车（需在 `config.yaml` 配 `network.panel_addr` 与 `network.panel_token`）：

```bash
go build -o bin/panda-webui ./webui/cmd/panel
./bin/panda-webui --config config.yaml
```

配合 web 推送时，另需 `push.enabled: true`（见 `config.example.yaml`）。
