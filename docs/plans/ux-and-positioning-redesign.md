# 定位重构与 UX 整改方案

日期：2026-08-19
状态：P0 / P1 已完成（2026-08-20，commit 45ee941）；P2 待做

## 1. 定位修正（核心结论）

OpenPanda **不是「更强的编码 agent」**，而是你所有设备与 agent 的**大总管 / 指挥家**：

- **简单的事亲自动手**：回答问题、查天气、设提醒、写个小脚本、改个配置——由 OpenPanda 直接完成，不惊动任何设备或 agent。
- **复杂的事调兵遣将**：多文件改动、跑测试、编译部署、GPU 任务——调度到网络里最合适的那台设备、那个 agent（Claude Code / Codex / opencode……）去执行。

一句话：**小事亲自动手，大事调兵遣将。**

## 2. 新 slogan 与价值主张

- 中文 slogan：**小事亲自动手，大事调兵遣将。**
- 英文 slogan：**Small stuff it does itself; big stuff it dispatches.**
- 一句话价值主张：一个归于你、跨所有设备的「大总管」——简单的事它直接办，复杂的事它派给你的 agent 和设备去办。

> 之前「每一段记忆都归你管——永不上云」太空洞、无画面感，已废弃。

## 3. 分阶段任务

### P0 — 定位与文案落地
- [x] entry 系统提示词改为「大总管/指挥家 + 简单直答 / 复杂调度」
- [x] 在方案文档中锁定新 slogan（README 保持原样不动）

### P0 — 首次上手
- [x] `panda init` 交互式初始化：模型端点 + API key + 节点名 + 能力卡一键生成
- [x] 默认落地页为 chat，侧栏导航分组/折叠（渐进揭示，弱化概念轰炸）

### P1 — 产品化
- [x] 设计语言统一（projects / nodes / skills / reminders / memory / system / settings）— 共享 `PageHeader` / `ErrorState` 组件，统一页面头部与错误展示（含重试按钮）
- [x] memory 页从「调试页」产品化（渲染 / 可编辑 / 高亮）— § 分隔条目化渲染、新增条目高亮、字符计数与超限提示、就地编辑（`PUT /api/memory/{file}`）
- [x] 错误友好化 + 全局 toast + 危险操作二次确认 — 全局 ToastHost（错误手动关闭 / 成功与信息自动消失）；删除会话、拒绝技能、取消任务、删除提醒均需二次确认；五种语言文案齐全

### P2 — 打磨
- [ ] kanban 拖拽键盘 / 触屏可及
- [ ] badge 可发现性、本地化与时间格式一致性

## 4. 验收标准

- `go build ./...` 通过
- `go test ./internal/entry/` 通过
- README 的 slogan 具体、有画面感，不再抽象