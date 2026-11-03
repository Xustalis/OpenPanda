# OpenPanda 官网（web-new）

OpenPanda 的中文单页产品官网：Next.js App Router + TypeScript + Tailwind CSS，纯静态导出，零后端、零外部资源依赖。

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

## 结构

- `app/page.tsx`：七个区块（Hero、它是什么、核心能力、工作原理、能力卡示例、CLI 速览、快速开始）与页脚；核心能力含四种输出类型（answer / tool_call / task / plan）
- `app/globals.css`：深色终端设计系统、背景网格/扫描线、渐显动画、响应式样式
- `components/TerminalDemo.tsx`：Hero 打字机终端（含 reduced-motion 降级）
- `components/CodeBlock.tsx`：轻量 YAML/JSON 语法高亮
- `components/RevealController.tsx`：IntersectionObserver 滚动渐显
- `components/MobileMenu.tsx`、`CopyButton.tsx`：移动端导航与复制按钮

字体全部使用系统无衬线/等宽栈，不引用外部图片或字体 CDN。
