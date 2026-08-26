# 常见问题（FAQ）

安装相关排错见 [`install.md`](install.md#疑难排查)；这里按使用场景汇总高频问题。

## 入门

**装好之后第一步做什么？**

```bash
panda init      # 交互式引导：节点命名、模型配置（生成 config.yaml + 能力卡）
panda repl      # 交互式命令行（裸 `panda` 也可以）
panda web       # 或者打开 Web 控制台（自动登录）
```

`panda doctor` 可以随时自检：二进制、PATH、配置、数据库、API key、适配器、agent CLI 是否就绪。

**`panda` 和 `panda daemon` 什么关系？**

裸 `panda` / `panda repl` 是交互式使用；`panda daemon` 是后台常驻进程，负责任务调度、节点心跳与提醒。日常单机问答用 REPL/web 即可；要接收远程委派任务或跑提醒，需要 daemon（可注册开机自启，见 [`install.md`](install.md#开机自启-手动控制)）。

## 模型配置

**支持哪些模型服务？**

任何 OpenAI 兼容或 Anthropic 兼容端点。`api_type` 填 `anthropic`（Messages 协议）或 `openai`（Chat Completions 协议）。推荐 DeepSeek（默认 `deepseek-v4-flash`，走 `https://api.deepseek.com/anthropic`）。

**报错「未配置模型 API key」？**

配置三选一：`panda init` 引导填写；`panda web` 设置页（系统 → 设置）填写并保存（保存即热生效，无需重启）；环境变量 `OPENPANDA_MODEL_API_KEY`。

**报错「API key 无效或无权限」？**

key 本身无效或无该模型权限——去服务商控制台核对，更新 `model.api_key` 后重试。

**报错「模型接口不存在」？**

`base_url` 或 `model` 名称写错。用 `panda web` 设置页的「测试连通」按钮逐项排查；DeepSeek 的 Anthropic 端点是 `https://api.deepseek.com/anthropic`（不带 `/v1`）。

**报错「模型服务限流 / 暂时不可用」？**

入口模型自带指数退避自动重试，仍失败说明持续限流或服务故障，稍后再试即可。

## 智能体（agents）

**什么是 agent 适配器？**

OpenPanda 自己不执行长程任务，而是委派给设备上安装的 agent CLI（Claude Code、Codex、OpenCode 等）；适配器（`adapters/*.py`）是统一协议的桥接脚本。`panda agents` 查看本机已装/缺失的 agent，缺失项会给出安装命令。

**适配器报「python3 not found」？**

适配器需要 Python 3；`python3 --version` 确认，macOS 可 `brew install python3`。

**`panda agents` 显示某 agent 未安装，但我不想装？**

不装也完全可用——调度器只把任务路由到声明了该 agent 的设备。能力卡里删掉对应条目即可。

**凭证（API key）如何给 agent？**

默认 `injection.model: auto`——agent 自带登录态/key 就直接用；没有自有凭证的 agent（目前只有 Claude Code 支持注入）会复用 panda 配置的模型（`model.api_key`，DeepSeek Anthropic 端点）。策略用 `panda config set injection auto|always|never` 切换。

## 任务与调度

**任务一直 `pending`？**

依次检查：daemon 是否在跑（`panda nodes` 看 running 列）；能力卡是否存在（`panda doctor` 的 adapters 行）；目标 agent 是否已安装。

**什么是 tier-2 授权？**

命令分两级：tier-1（可逆：读文件、跑构建）与 tier-2（不可逆：git push、删除）。tier-2 命令默认被拒绝——`panda ask --authorize "..."`、REPL `/authorize` 或能力卡 `tier: 1` 声明放行。被拒绝的任务会进 `review` 队列说明原因。

**任务进了 `review` 状态？**

不可逆操作或超出预算的监督循环会暂停等待人工审批：`panda queue` 查看，`panda task approve <id>` / `reject <id>` 决定，Web 控制台任务卡上有同样的按钮。

**什么是 scope drift？**

入口模型为每个任务声明允许操作的文件范围（scope）；执行中越界写文件会被拦截并转 review。如果合法操作被误拦，检查任务描述里是否明确列出了要改的文件路径。

**一件事要跨几台机器做怎么办？**

那是「计划」（plan）而不是单个任务：几个前后相接的阶段，每段路由到适合它的机器，被依赖阶段的产物会自动打包搬到下一段。两个入口——写成文件（`panda plan example` 生成模板，`panda plan run x.yaml --dry-run` 先看路由结果），或者直接一句话说出来（`panda ask "写个训练脚本，在有显卡的机器上跑，最后把结论发回来"`），入口模型判断需要换机器时会自己拆成 plan。`panda plan show <id>` 看每段状态与产物接线。

注意：计划的任何阶段都不携带 tier-2 同意，所以流水线里的不可逆动作一定停在 `review` 等你批，不会因为你授权过整句话就直接执行。

**能不能用说话的方式发任务？**

`panda voice` 是免手入口（桌宠 / 移动场景）：唤醒词 → 转写 → 走和 `panda ask` 完全相同的路径 → 打印并朗读回答，与 REPL / `ask --continue` 共用同一条持久会话（在一台设备上说，回终端接着问）。需要本地语音依赖：`pip install openwakeword faster-whisper pyaudio numpy`（默认后端不需要密钥）；缺依赖时会直接打印原因并退出。`--once` 只处理一句，`--mute` 只打印不朗读。语音入口没有 `--authorize`，不可逆命令一律进 `review`。

## 多设备

**多台设备怎么组网？**

每台设备安装后 `panda init`，在 `network` 段配置对端地址（`peers`）与共享密钥（`shared_secret`），然后各自跑 `panda daemon`。`panda nodes` 查看节点目录与在线状态。

**为什么对外监听启动被拒绝？**

安全约束：非回环监听必须配置 `shared_secret`（或 `OPENPANDA_SHARED_SECRET`），否则 daemon 拒绝启动。回环监听不受限，且 Web 面板会自动生成临时 token。

**同一台机器能跑两个节点吗？**

同一节点身份有单例锁，第二个 daemon 会诊断后退出。需要多身份时用 VM 节点（`node.kind: vm` + 显式 `node.identity`）。

## 数据与维护

**数据存在哪里？**

用户级目录，与安装位置无关：macOS `~/Library/Application Support/openpanda`，Linux `~/.local/share/openpanda`，Windows `%LOCALAPPDATA%\openpanda-data`（刻意与安装目录 `%LOCALAPPDATA%\OpenPanda` 区分，避免大小写不敏感的 NTFS 把两者折叠成同一目录）。`memory/`、`projects/`、`skills/` 是用户资产，卸载默认保留。

**如何升级？**

`panda web` → 系统 → 更新：检查新版本（附 changelog 摘要）→ 下载 → 空闲时应用。daemon 会在日志里提示新版本（无法自我应用，需打开 Web 控制台操作）。一键脚本用户也可直接重跑安装命令。

**如何彻底卸载？**

`panda uninstall` 白名单删除并自动备份；`--purge` 连用户数据一起删；`--dry-run` 只看计划不动手。

**Web 控制台打不开 / token 丢了？**

回环模式重新 `panda web` 即可（URL 自带临时 token）。对外监听需要 `OPENPANDA_PANEL_TOKEN`；SSE 事件流使用 `?token=` 查询参数鉴权。

**REPL 里会话上下文能存多久？**

持久化在 `~/.local/state/openpanda/conversation.json`，24k 字符预算自动裁剪最旧内容；`/new` 清空，`/history` 查看，`panda ask --continue` 接续。
