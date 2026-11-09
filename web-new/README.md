# OpenPanda 官网原型（web-new）

> **这份代码已不是对外发布的官网。** 线上站点是仓库根部的 [`site/`](../site/)：九页静态 HTML，
> 由 [`.github/workflows/pages.yml`](../.github/workflows/pages.yml) 部署到 GitHub Pages，
> 内容由 [`scripts/check-site.py`](../scripts/check-site.py) 逐页校验。本目录是 2026-11-02
> 的 Next.js 单页原型，保留作设计参考，不参与构建、不发布、不在 CI 里跑。改官网请改 `site/`。

OpenPanda 的中文单页产品官网原型：Next.js App Router + TypeScript + Tailwind CSS，纯静态导出，零后端、零外部资源依赖。

## 开发

```bash
npm install
npm run dev        # http://localhost:3000
```

## 静态构建

```bash
npm run build      # 输出 out/
```

`next.config.mjs` 已设置 `output: "export"`，构建产物可直接由任意静态文件服务器托管：

```bash
python3 -m http.server 4173 --directory out
```

`out/`、`node_modules/`、`.next/` 与 `tsconfig.tsbuildinfo` 都是本地构建产物，已被 gitignore。

## 结构

- `app/page.tsx`：七个区块（Hero、它是什么、核心能力、工作原理、能力卡示例、CLI 速览、快速开始）与页脚；核心能力含四种输出类型（answer / tool_call / task / plan）
- `app/globals.css`：深色终端设计系统、背景网格/扫描线、渐显动画、响应式样式
- `components/TerminalDemo.tsx`：Hero 打字机终端（含 reduced-motion 降级）
- `components/CodeBlock.tsx`：轻量 YAML/JSON 语法高亮
- `components/RevealController.tsx`：IntersectionObserver 滚动渐显
- `components/MobileMenu.tsx`、`CopyButton.tsx`：移动端导航与复制按钮

字体全部使用系统无衬线/等宽栈，不引用外部图片或字体 CDN。

## 与 `site/` 的差异

原型是单页、纯中文、需要 Next.js 构建；`site/` 是九页（概览 / 能力 / 文档 / 下载 / 更新日志 /
现状 / 安全 / 社区 / FAQ）、自包含 404、逐页静态复制的无构建站点，03 节用实机截图与录屏作为证据。
两者不共享组件或字典，原型里的内容不能当作线上页面的来源。
