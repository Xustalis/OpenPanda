import type { Metadata, Viewport } from "next";
import "./globals.css";

const siteUrl = "https://xustalis.github.io/OpenPanda/";

export const metadata: Metadata = {
  metadataBase: new URL(siteUrl),
  title: "OpenPanda — 编排所有设备与 Agent 的大脑",
  description:
    "OpenPanda 是开源、本地优先的多 Agent 协同操作系统。把散落在各台设备上的 Claude Code、Codex、OpenCode 与异构机器，编成一张可调度、可监督、可验收的网。",
  keywords: [
    "OpenPanda",
    "多 Agent 编排",
    "本地优先",
    "Claude Code",
    "Codex",
    "OpenCode",
    "异构设备",
    "P2P",
    "开发者工具",
  ],
  authors: [{ name: "OpenPanda contributors" }],
  creator: "OpenPanda contributors",
  alternates: { canonical: "/" },
  openGraph: {
    type: "website",
    locale: "zh_CN",
    url: siteUrl,
    siteName: "OpenPanda",
    title: "OpenPanda — 编排所有设备与 Agent 的大脑",
    description:
      "本地优先的图状编排内核：意图、拆解、路由、监督、验收，全部在你自己的网络里完成。无厂商中转。",
  },
  twitter: {
    card: "summary",
    title: "OpenPanda — 编排所有设备与 Agent 的大脑",
    description:
      "把异构的 Agent 与设备，编成一张会自己调度、监督、验收的网。",
  },
  robots: { index: true, follow: true },
};

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  colorScheme: "light",
  themeColor: "#F7F4EE",
};

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="zh-CN">
      <body>
        <noscript>
          <style>{`[data-reveal]{opacity:1 !important;transform:none !important}.hero-in{opacity:1 !important}`}</style>
        </noscript>
        {children}
      </body>
    </html>
  );
}
