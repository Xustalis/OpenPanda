import type { ReactNode } from "react";
import CodeBlock from "@/components/CodeBlock";
import CopyButton from "@/components/CopyButton";
import {
  IconArrowRight,
  IconArrowUpRight,
  IconBolt,
  IconChip,
  IconGithub,
  IconLayers,
  IconLock,
  IconNetwork,
  IconOrchestrate,
  IconPlug,
  IconServer,
  IconShield,
  IconSpark,
  IconTerminal,
  IconWindow,
} from "@/components/Icons";
import { Logo } from "@/components/Logo";
import MobileMenu from "@/components/MobileMenu";
import RevealController from "@/components/RevealController";
import Starfield from "@/components/Starfield";
import TerminalDemo from "@/components/TerminalDemo";

const GITHUB = "https://github.com/Xustalis/OpenPanda";

const NAV_LINKS = [
  { href: "#what", label: "它是什么", index: "01" },
  { href: "#capabilities", label: "核心能力", index: "02" },
  { href: "#architecture", label: "工作原理", index: "03" },
  { href: "#code", label: "能力卡", index: "04" },
  { href: "#cli", label: "CLI", index: "05" },
  { href: "#start", label: "快速开始", index: "06" },
];

const AGENTS = [
  "claude_code",
  "codex",
  "opencode",
  "grok_build",
  "deepseek_harness",
  "antigravity",
  "hermes",
  "pi",
  "openclaw",
  "zcode",
];

const STATS = [
  { value: "10", suffix: "+", label: "Agent 适配器" },
  { value: "4", suffix: "", label: "异构设备类别" },
  { value: "0", suffix: "", label: "云端中转" },
  { value: "~40", suffix: "MB", label: "常驻内存" },
];

const WHAT_ROWS: { title: string; body: ReactNode }[] = [
  {
    title: "组织，而不是替代。",
    body: (
      <>
        它不训练模型，也不接管你的编辑器。Claude Code、Codex、OpenCode
        仍然是它们自己，只是第一次<strong>共享同一份任务上下文、同一套身份与授权</strong>
        。
      </>
    ),
  },
  {
    title: "路由，而不是堆叠。",
    body: (
      <>
        一条指令进来，先被分类。工程任务拆成有依赖的 Plan，再按能力卡与实时负载送到合适的节点；
        <strong>机器忙、掉线或模型熔断，任务自动改道</strong>。
      </>
    ),
  },
  {
    title: "监督，而不是相信。",
    body: (
      <>
        执行中的每条命令、每次文件改动都实时回传。验收看的是
        <strong>真实命令真的跑过、产物真的存在</strong>
        ，而不是模型在最后一句说「完成了」。
      </>
    ),
  },
  {
    title: "本地，而不是租用。",
    body: (
      <>
        节点之间点对点直连，身份、信任档与审计链留在你自己的网络里。
        <strong>没有厂商运营的中转集群</strong>，也没有替你保管提示词的云。
      </>
    ),
  },
];

type Capability = {
  index: string;
  title: string;
  body: ReactNode;
  tags: string[];
  foot: string;
  icon: ReactNode;
};

const CAPABILITIES: Capability[] = [
  {
    index: "/ 01",
    title: "任务编排与派发",
    body: (
      <>
        从一句自然语言到可执行的 Plan：意图分类、子任务拆解、依赖接线、按实测容量评分派发。
        执行中持续监督，失败携带原因重派，直到通过验收或耗尽轮次预算。
      </>
    ),
    tags: ["intent", "plan", "dispatch", "retry"],
    foot: "监督循环 · 默认 5 轮",
    icon: <IconOrchestrate className="h-[19px] w-[19px]" />,
  },
  {
    index: "/ 02",
    title: "异构 Agent 调度",
    body: (
      <>
        内置十个专用适配器，覆盖 <code>claude_code</code>、<code>codex</code>、
        <code>opencode</code>、<code>grok_build</code>{" "}
        等；通用桥接器可接入任意无头 CLI。按 <code>best_at</code> /{" "}
        <code>not_for</code> 画像选人，也能把一条指令并行派给多个 Agent 回收不同结论。
      </>
    ),
    tags: [
      "claude_code",
      "codex",
      "opencode",
      "grok_build",
      "deepseek_harness",
      "antigravity",
    ],
    foot: "10 个适配器 + 通用桥接",
    icon: <IconChip className="h-[19px] w-[19px]" />,
  },
  {
    index: "/ 03",
    title: "异构设备网络",
    body: (
      <>
        Mac、Windows、Linux GPU 机器、香橙派 ARM64 在同一个坐标系里。LAN 自动发现、
        蓝牙式配对、P2P 直连与多跳中继；算力大小不同，任务照常抵达。
      </>
    ),
    tags: ["macOS", "Windows", "Linux GPU", "ARM64"],
    foot: "WS + sealed UDP · 无云端中转",
    icon: <IconNetwork className="h-[19px] w-[19px]" />,
  },
  {
    index: "/ 04",
    title: "能力卡与安全内核",
    body: (
      <>
        每个节点用 <code>capabilities.yaml</code>{" "}
        自述硬件、运行时与 Agent。tier 1 自动执行，tier 2 不可逆操作停在评审闸门；
        mesh / guarded / open 三档信任钳制内容流向，哈希链审计可逐条校验。
      </>
    ),
    tags: ["capabilities.yaml", "tier gate", "trust zone", "audit chain"],
    foot: "可逆自动 · 不可逆审批",
    icon: <IconShield className="h-[19px] w-[19px]" />,
  },
];

const OUTPUT_TYPES = [
  {
    name: "answer",
    zh: "直接回答",
    body: "不需要触碰文件或设备，分类器就地给出结论与依据。",
  },
  {
    name: "tool_call",
    zh: "单步工具调用",
    body: "查询、命令或 MCP 工具，执行结果原样回传并记入时间线。",
  },
  {
    name: "task",
    zh: "任务派发",
    body: "跨节点、多步骤的工作，进入队列交给调度器按能力卡分派。",
  },
  {
    name: "plan",
    zh: "计划编排",
    body: "有依赖的多阶段流水线，先生成 Plan，再逐段执行与验收。",
  },
];

const MINI_FEATURES: { icon: ReactNode; title: string; body: ReactNode }[] = [
  {
    icon: <IconLayers className="h-4 w-4" />,
    title: "双层记忆",
    body: (
      <>
        <code>USER.md</code> 偏好与 <code>MEMORY.md</code>{" "}
        事实严格分层，跨设备随任务同行。
      </>
    ),
  },
  {
    icon: <IconSpark className="h-4 w-4" />,
    title: "会话树",
    body: (
      <>
        <code>panda session fork --at N</code> 在任意轮次分叉，worktree
        从父分支切出。
      </>
    ),
  },
  {
    icon: <IconPlug className="h-4 w-4" />,
    title: "可嵌入",
    body: (
      <>
        <code>panda mcp</code> 把节点工具交给任何 MCP Agent；
        <code>panda rpc</code> 走 NDJSON-over-stdio。
      </>
    ),
  },
  {
    icon: <IconServer className="h-4 w-4" />,
    title: "产物回传",
    body: (
      <>
        截图、报告与构建物打包取回，<code>panda task artifact</code> 列出并解出。
      </>
    ),
  },
];

const ENTRY_ITEMS = [
  { icon: <IconTerminal className="h-3 w-3" />, name: "终端 TUI", cmd: "panda" },
  { icon: <IconWindow className="h-3 w-3" />, name: "Web 控制台", cmd: "panda web" },
  { icon: <IconBolt className="h-3 w-3" />, name: "单次指令", cmd: "panda ask" },
  { icon: <IconPlug className="h-3 w-3" />, name: "MCP / RPC", cmd: "panda mcp" },
];

const KERNEL_CHIPS = [
  "意图分类",
  "Plan 拆解",
  "容量调度",
  "监督验收",
  "能力注册表",
  "tier 授权",
];

const NODES = [
  {
    name: "darwin-arm64",
    trust: "mesh",
    agents: "claude_code · codex",
    load: "0.31",
    guarded: false,
  },
  {
    name: "linux-x64 · GPU",
    trust: "mesh",
    agents: "codex · docker",
    load: "0.08",
    guarded: false,
  },
  {
    name: "win-x64",
    trust: "guarded",
    agents: "opencode · shell",
    load: "0.12",
    guarded: true,
  },
  {
    name: "linux-arm64 · SBC",
    trust: "guarded",
    agents: "shell · gpio",
    load: "0.02",
    guarded: true,
  },
];

const LEGENDS = [
  {
    icon: <IconLayers className="h-[17px] w-[17px]" />,
    title: "控制面",
    body: "认证 WebSocket，握手内协商逐连接 AEAD。",
  },
  {
    icon: <IconNetwork className="h-[17px] w-[17px]" />,
    title: "数据面",
    body: "密封 UDP，支持 NAT 穿透与多跳中继。",
  },
  {
    icon: <IconLock className="h-[17px] w-[17px]" />,
    title: "安全门",
    body: "tier 2 审批、信任档钳制、哈希链审计。",
  },
];

const CLI_COMMANDS = [
  { cmd: "panda status", desc: "节点身份、资源档、模型与能力目录。", flag: "" },
  { cmd: "panda nodes", desc: "在线设备、信任档与实时负载。", flag: "" },
  { cmd: "panda queue", desc: "排队、执行中与待审批任务看板。", flag: "--watch" },
  { cmd: "panda web", desc: "本地 Web 控制台，自动登录并打开浏览器。", flag: "--daemon" },
  {
    cmd: "panda card rescan",
    desc: "重扫硬件与已安装 Agent CLI，更新能力卡。",
    flag: "--write",
  },
  { cmd: 'panda ask "…"', desc: "一条指令：分类后直接回答、执行或派发。", flag: "" },
  { cmd: "panda pair", desc: "两端核对同一短码，把新设备接进网络。", flag: "" },
  { cmd: "panda approve", desc: "放行 tier 2 不可逆操作，审计留痕。", flag: "<id>" },
];

const CAPABILITY_YAML = `# capabilities.yaml — 节点自描述，无中心登记表
device: edge-node-03
resource_class: Edge
arch: arm64
trust: guarded

native:
  - id: shell
    tier: 1
  - id: gpio
    tier: 2          # 不可逆动作，需要人工审批

agents:
  claude_code:
    adapter: claude_code.py
    caps: [coding, shell, file_edit]
    best_at: [refactoring, multi_file_edits]
  codex:
    adapter: codex.py
    caps: [coding, build, shell]
    cost_tier: medium
`;

const TASK_JSON = `{
  "id": "task_7f3a",
  "title": "迁移认证中间件到新接口",
  "intent": "code_change",
  "workspace": "project/panda",
  "requirements": {
    "agents": ["claude_code", "codex"],
    "hardware": ["docker"],
    "tier_max": 1
  },
  "routing": {
    "preferred": "linux-gpu",
    "fallback": "local"
  },
  "acceptance": {
    "require_command": "make test",
    "review_gate": "tier2"
  }
}
`;

const INSTALL_CMD =
  "curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh | sh";
const INIT_CMDS = ["panda init", "panda doctor"];
const LAUNCH_CMDS = [
  "panda web",
  'panda ask "看看这台机器还能接什么任务"',
  "panda pair <host:port>",
];

function SectionKicker({ index, label }: { index: string; label: string }) {
  return (
    <div className="kicker">
      <span>{index}</span>
      <span className="kicker-dim">/ {label}</span>
    </div>
  );
}

function CommandRow({ command }: { command: string }) {
  return (
    <div className="cmd-row">
      <span className="dollar">$</span>
      <code>{command}</code>
      <span className="copy-wrap">
        <CopyButton value={command} />
      </span>
    </div>
  );
}

function ExternalLink({
  href,
  children,
}: {
  href: string;
  children: ReactNode;
}) {
  return (
    <a
      className="footer-link"
      href={href}
      target="_blank"
      rel="noreferrer noopener"
    >
      {children}
      <IconArrowUpRight size={11} />
    </a>
  );
}

export default function HomePage() {
  return (
    <>
      <a className="skip-link" href="#main">
        跳到主要内容
      </a>

      <header className="site-header">
        <div className="shell header-inner">
          <a href="#top" className="brand" aria-label="OpenPanda 首页">
            <Logo className="brand-mark h-6 w-6" />
            <span>
              OpenPanda
              <span className="brand-tld hidden sm:inline">
                / ORCHESTRATION
              </span>
            </span>
          </a>

          <nav className="nav-links" aria-label="主导航">
            {NAV_LINKS.map((link) => (
              <a key={link.href} href={link.href} className="nav-link">
                {link.label}
              </a>
            ))}
          </nav>

          <div className="header-actions">
            <a
              href={GITHUB}
              target="_blank"
              rel="noreferrer noopener"
              className="btn btn-ghost btn-sm header-github"
            >
              <IconGithub size={14} />
              GitHub
            </a>
            <MobileMenu links={NAV_LINKS} github={GITHUB} />
          </div>
        </div>
      </header>

      <main id="main" className="site-main" tabIndex={-1}>
        {/* ------------------------------------------------ hero */}
        <section
          id="top"
          className="hero-section relative pb-14 pt-28 sm:pb-20 sm:pt-32 lg:pt-36"
        >
          <Starfield className="hero-stars" />
          <div className="shell">
            <div className="grid grid-cols-1 items-center gap-12 lg:grid-cols-[1.02fr_0.98fr] lg:gap-14">
              <div className="min-w-0">
                <div className="hero-in mb-6" data-delay="1">
                  <span className="hero-kicker">
                    <span className="live-dot" />
                    LOCAL-FIRST ORCHESTRATION KERNEL
                  </span>
                </div>

                <h1 className="hero-in" data-delay="2">
                  <span className="block">
                    <span className="hero-name text-[clamp(3.15rem,8.6vw,6.2rem)] leading-[0.95] tracking-[-0.03em]">
                      OpenPanda
                    </span>
                  </span>
                  <span className="hero-sub mt-4 block text-[clamp(1.55rem,3.6vw,2.9rem)] leading-[1.22] tracking-[-0.015em]">
                    编排所有设备
                    <br />
                    与 Agent 的大脑
                  </span>
                </h1>

                <p
                  className="hero-in mt-6 max-w-[35rem] text-[0.93rem] leading-[1.95] text-ink-muted sm:text-[1rem]"
                  data-delay="3"
                >
                  它不替你写代码。它决定谁去写、在哪台机器写、什么时候算写完——
                  把散落在不同终端里的 Agent，编成一支会调度、会补位、也必须通过验收的队伍。
                </p>

                <div
                  className="hero-in mt-8 flex flex-wrap gap-3"
                  data-delay="4"
                >
                  <a href="#start" className="btn btn-primary">
                    立即部署
                    <IconArrowRight className="h-4 w-4" />
                  </a>
                  <a
                    href={GITHUB}
                    target="_blank"
                    rel="noreferrer noopener"
                    className="btn btn-ghost"
                  >
                    <IconGithub size={15} />
                    在 GitHub 查看
                  </a>
                </div>

                <ul
                  className="hero-in mt-7 flex flex-wrap gap-x-5 gap-y-2 font-mono text-[9.5px] uppercase tracking-[0.12em] text-ink-faint"
                  data-delay="5"
                >
                  {["本地优先", "无云端中转", "P2P 直连", "AGPL-3.0"].map(
                    (item) => (
                      <li key={item} className="flex items-center gap-2">
                        <i className="h-1 w-1 rounded-full bg-accent/60" />
                        {item}
                      </li>
                    ),
                  )}
                </ul>
              </div>

              <div className="hero-in relative min-w-0" data-delay="4">
                <div className="relative">
                  <TerminalDemo />
                  <div className="mt-3 flex items-center justify-between gap-4 font-mono text-[8.5px] uppercase tracking-[0.12em] text-ink-faint">
                    <span>// simulated session</span>
                    <span>no cloud relay</span>
                  </div>
                </div>
              </div>
            </div>

            <div
              className="hero-in mt-14 grid grid-cols-2 gap-6 border-t border-line pt-7 sm:grid-cols-4 sm:gap-8"
              data-delay="6"
            >
              {STATS.map((stat) => (
                <div key={stat.label} className="stat">
                  <div className="stat-value">
                    {stat.value}
                    {stat.suffix && <span>{stat.suffix}</span>}
                  </div>
                  <div className="stat-label">{stat.label}</div>
                </div>
              ))}
            </div>
          </div>
        </section>

        {/* ------------------------------------------------ marquee */}
        <div className="marquee" aria-hidden="true">
          <div className="marquee-track">
            {[0, 1].map((group) => (
              <div className="marquee-group" key={group}>
                {AGENTS.map((agent) => (
                  <span className="marquee-item" key={`${group}-${agent}`}>
                    {agent}
                  </span>
                ))}
              </div>
            ))}
          </div>
        </div>

        {/* ------------------------------------------------ what it is */}
        <section id="what" className="section">
          <div className="shell grid grid-cols-1 gap-12 lg:grid-cols-[0.78fr_1.22fr] lg:gap-16">
            <div className="self-start lg:sticky lg:top-28" data-reveal>
              <SectionKicker index="01" label="WHAT IT IS" />
              <h2 className="section-title">
                不是另一个 Agent，
                <br />
                而是 Agent 之上的那一层。
              </h2>
              <p className="section-lead">
                OpenPanda 是开源、本地优先的多 Agent
                协同操作系统。它本身不执行任务，只负责把「谁来做、在哪做、做得对不对」组织成一张可执行的图。
              </p>

              <div className="mt-8 hidden border-l border-line pl-4 lg:block">
                <p className="font-mono text-[10.5px] leading-relaxed text-ink-muted">
                  <span className="text-accent">$</span> panda ask &quot;把认证中间件迁到新接口&quot;
                </p>
                <p className="mt-1 font-mono text-[9.5px] leading-relaxed text-ink-faint">
                  intent: code_change → plan: 3 stages → 2 nodes
                </p>
              </div>
            </div>

            <div>
              {WHAT_ROWS.map((row, index) => (
                <div
                  className="what-row"
                  key={row.title}
                  data-reveal
                  data-delay={String(index + 1)}
                >
                  <span className="what-index">
                    {String(index + 1).padStart(2, "0")}
                  </span>
                  <div>
                    <h3 className="what-title">{row.title}</h3>
                    <p className="what-body">{row.body}</p>
                  </div>
                </div>
              ))}
            </div>
          </div>
        </section>

        {/* ------------------------------------------------ capabilities */}
        <section id="capabilities" className="section">
          <div className="shell">
            <div
              className="flex flex-col gap-6 lg:flex-row lg:items-end lg:justify-between"
              data-reveal
            >
              <div className="max-w-2xl">
                <SectionKicker index="02" label="CAPABILITIES" />
                <h2 className="section-title">四块拼图，组成一个调度层。</h2>
                <p className="section-lead">
                  编排、调度、组网、授权，各管一件事，但共用同一份节点画像与同一条审计链。
                </p>
              </div>
              <div className="font-mono text-[9px] uppercase tracking-[0.18em] text-ink-faint">
                [ 04 MODULES / ALWAYS ON ]
              </div>
            </div>

            <div className="mt-12 grid grid-cols-1 gap-4 sm:grid-cols-2">
              {CAPABILITIES.map((capability, index) => (
                <article
                  className="glass hover-lift cap-card"
                  key={capability.title}
                  data-reveal
                  data-delay={String((index % 2) + 1)}
                >
                  <div className="cap-top">
                    <span className="icon-tile">{capability.icon}</span>
                    <span className="card-index">{capability.index}</span>
                  </div>
                  <h3 className="cap-title">{capability.title}</h3>
                  <p className="cap-body">{capability.body}</p>
                  <div className="tag-row">
                    {capability.tags.map((tag) => (
                      <span className="tag" key={tag}>
                        {tag}
                      </span>
                    ))}
                  </div>
                  <div className="cap-foot">
                    <i />
                    {capability.foot}
                  </div>
                </article>
              ))}
            </div>

            <div className="glass output-panel" data-reveal>
              <div className="output-head">
                <div>
                  <h3 className="output-title">四种输出类型</h3>
                  <p className="output-lead">
                    内核只回传四种形状，调用方永远知道下一步该等什么。
                  </p>
                </div>
                <span className="output-count">[ 04 / OUTPUTS ]</span>
              </div>
              <div className="output-grid">
                {OUTPUT_TYPES.map((output) => (
                  <div className="output-item" key={output.name}>
                    <div className="output-name">
                      {output.name}
                      <span>{output.zh}</span>
                    </div>
                    <p>{output.body}</p>
                  </div>
                ))}
              </div>
            </div>

            <div
              className="mt-10 grid grid-cols-1 gap-x-6 gap-y-5 sm:grid-cols-2 lg:grid-cols-4"
              data-reveal
            >
              {MINI_FEATURES.map((feature) => (
                <div className="mini-feature" key={feature.title}>
                  <h3>
                    <span className="text-accent">{feature.icon}</span>
                    {feature.title}
                  </h3>
                  <p>{feature.body}</p>
                </div>
              ))}
            </div>
          </div>
        </section>

        {/* ------------------------------------------------ architecture */}
        <section id="architecture" className="section">
          <div className="shell">
            <div className="max-w-3xl" data-reveal>
              <SectionKicker index="03" label="ARCHITECTURE" />
              <h2 className="section-title">一条指令，穿过三层。</h2>
              <p className="section-lead">
                入口只负责表达意图，内核负责把它变成可执行、可监督的图，节点只负责把事做完。
                任何一层都可以替换，任何一跳都不经过厂商云端。
              </p>
            </div>

            <div className="arch-wrap mt-10" data-reveal="scale">
              <div className="arch">
                <div className="arch-col">
                  <div className="arch-col-label">
                    <span>入口 / INTENT</span>
                    <span>04</span>
                  </div>
                  <div className="arch-panel">
                    <div className="entry-list">
                      {ENTRY_ITEMS.map((item) => (
                        <div className="entry-item" key={item.name}>
                          <span className="entry-icon">{item.icon}</span>
                          <span className="entry-name">{item.name}</span>
                          <code>{item.cmd}</code>
                        </div>
                      ))}
                    </div>
                  </div>
                </div>

                <div className="arch-flow" aria-hidden="true">
                  <span className="arch-flow-label">intent</span>
                </div>

                <div className="arch-col">
                  <div className="arch-col-label">
                    <span>OpenPanda 内核 / ORCHESTRATION</span>
                  </div>
                  <div className="kernel-panel">
                    <div className="kernel-mark">
                      <Logo className="h-7 w-7" />
                    </div>
                    <div className="kernel-title">OPENPANDA KERNEL</div>
                    <div className="kernel-path">意图 → 拆解 → 路由 → 监督 → 验收</div>
                    <div className="kernel-chips">
                      {KERNEL_CHIPS.map((chip) => (
                        <span className="kernel-chip" key={chip}>
                          {chip}
                        </span>
                      ))}
                    </div>
                  </div>
                </div>

                <div className="arch-flow" aria-hidden="true">
                  <span className="arch-flow-label">dispatch · verify</span>
                </div>

                <div className="arch-col">
                  <div className="arch-col-label">
                    <span>执行节点 / EXECUTION</span>
                    <span>ONLINE 04</span>
                  </div>
                  <div className="arch-panel">
                    <div className="node-list">
                      {NODES.map((node) => (
                        <div className="node-item" key={node.name}>
                          <div className="node-head">
                            <span
                              className={`node-dot ${node.guarded ? "guarded" : ""}`}
                            />
                            {node.name}
                            <span className="node-trust">{node.trust}</span>
                          </div>
                          <div className="node-meta">
                            <span className="node-agents">{node.agents}</span>
                            <span>load {node.load}</span>
                          </div>
                        </div>
                      ))}
                    </div>
                  </div>
                </div>
              </div>
            </div>

            <div className="mt-4 grid grid-cols-1 gap-3 sm:grid-cols-3" data-reveal>
              {LEGENDS.map((legend) => (
                <div className="legend-item" key={legend.title}>
                  <span className="legend-icon">{legend.icon}</span>
                  <div>
                    <h3>{legend.title}</h3>
                    <p>{legend.body}</p>
                  </div>
                </div>
              ))}
            </div>
          </div>
        </section>

        {/* ------------------------------------------------ code */}
        <section id="code" className="section">
          <div className="shell">
            <div className="max-w-3xl" data-reveal>
              <SectionKicker index="04" label="CONTRACT" />
              <h2 className="section-title">能力卡，是节点的身份证。</h2>
              <p className="section-lead">
                没有中心登记表。每个节点自己声明能做什么、不能做什么、哪些动作需要人类点头；
                调度器读这份声明，而不是猜。
              </p>
            </div>

            <div className="mt-10 grid grid-cols-1 gap-5 lg:grid-cols-2">
              <div data-reveal="left">
                <CodeBlock
                  filename="capabilities.yaml"
                  language="yaml"
                  code={CAPABILITY_YAML}
                />
                <p className="code-note">
                  <span>
                    硬件、运行时与 Agent 能力全部自描述。更新只需重扫：
                    <code>panda card rescan --write</code>。
                  </span>
                </p>
              </div>

              <div data-reveal="right" data-delay="1">
                <CodeBlock
                  filename="task.json"
                  language="json"
                  code={TASK_JSON}
                />
                <p className="code-note">
                  <span>
                    任务携带验收条件出门：要求真实命令、限制风险等级、指定首选节点与回退路径。
                  </span>
                </p>
              </div>
            </div>
          </div>
        </section>

        {/* ------------------------------------------------ cli */}
        <section id="cli" className="section">
          <div className="shell">
            <div
              className="flex flex-col gap-6 lg:flex-row lg:items-end lg:justify-between"
              data-reveal
            >
              <div className="max-w-2xl">
                <SectionKicker index="05" label="CLI" />
                <h2 className="section-title">终端是第一界面。</h2>
                <p className="section-lead">
                  所有能力都有命令行入口。脚本能做的，和你手动做的是同一件事。
                </p>
              </div>
              <div className="hidden items-center gap-2 font-mono text-[9px] uppercase tracking-[0.14em] text-ink-faint sm:flex">
                <span className="h-1.5 w-1.5 rounded-full bg-accent/70" />
                panda help · all commands
              </div>
            </div>

            <div className="mt-10 grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
              {CLI_COMMANDS.map((command, index) => (
                <div
                  className="glass hover-lift cli-card"
                  key={command.cmd}
                  data-reveal
                  data-delay={String((index % 4) + 1)}
                >
                  <div className="cli-cmd">
                    <span className="dollar">$</span>
                    <code>{command.cmd}</code>
                    {command.flag && (
                      <span className="cli-flag">{command.flag}</span>
                    )}
                  </div>
                  <p className="cli-desc">{command.desc}</p>
                  <span className="cli-arrow" aria-hidden="true">
                    <IconArrowRight size={13} />
                  </span>
                </div>
              ))}
            </div>

            <p
              className="mt-5 font-mono text-[9px] tracking-[0.12em] text-ink-faint"
              data-reveal
            >
              完整命令列表： panda help · 交互式主位： panda
            </p>
          </div>
        </section>

        {/* ------------------------------------------------ quick start */}
        <section id="start" className="section">
          <div className="shell">
            <div className="max-w-3xl" data-reveal>
              <SectionKicker index="06" label="QUICKSTART" />
              <h2 className="section-title">三条命令，从零到一张网。</h2>
              <p className="section-lead">
                单个二进制，无 CGO，macOS / Linux / Windows 直接跑。首次启动只问一个问题。
              </p>
            </div>

            <div className="mt-10 grid grid-cols-1 gap-4 lg:grid-cols-3">
              <div className="glass step-card" data-reveal data-delay="1">
                <div className="step-head">
                  <span className="step-num">01</span>
                  <span className="step-tag">INSTALL</span>
                </div>
                <h3 className="step-title">装一个二进制</h3>
                <p className="step-desc">
                  macOS / Linux 一行安装。没有后台常驻的云组件，装完即可离线使用。
                </p>
                <div className="cmd-stack">
                  <CommandRow command={INSTALL_CMD} />
                </div>
                <p className="cmd-note">
                  Windows 使用 <code>install.ps1</code>；Homebrew 与源码构建方式见仓库
                  README。
                </p>
              </div>

              <div className="glass step-card" data-reveal data-delay="2">
                <div className="step-head">
                  <span className="step-num">02</span>
                  <span className="step-tag">INIT</span>
                </div>
                <h3 className="step-title">初始化一次</h3>
                <p className="step-desc">
                  探测设备名、资源档与已装 Agent CLI，生成本机能力卡；自检确认环境可用。
                </p>
                <div className="cmd-stack">
                  {INIT_CMDS.map((command) => (
                    <CommandRow command={command} key={command} />
                  ))}
                </div>
                <p className="cmd-note">
                  交互式只问「是否现在配置模型」；加 <code>--defaults</code> 可零提问。
                </p>
              </div>

              <div className="glass step-card" data-reveal data-delay="3">
                <div className="step-head">
                  <span className="step-num">03</span>
                  <span className="step-tag">LAUNCH</span>
                </div>
                <h3 className="step-title">起飞</h3>
                <p className="step-desc">
                  打开控制台，发第一条指令；再用一个短码把下一台设备接进同一张网。
                </p>
                <div className="cmd-stack">
                  {LAUNCH_CMDS.map((command) => (
                    <CommandRow command={command} key={command} />
                  ))}
                </div>
                <p className="cmd-note">
                  第二台设备也可被 LAN 自动发现，用 <code>panda nodes admit</code>{" "}
                  按指纹准入。
                </p>
              </div>
            </div>
          </div>
        </section>

        {/* ------------------------------------------------ final CTA */}
        <section className="pb-24 pt-8 sm:pb-28">
          <div className="shell">
            <div className="cta-band corner-frame" data-reveal="scale">
              <span className="mono-label">READY WHEN YOU ARE</span>
              <h2 className="cta-title mx-auto mt-4 max-w-2xl text-[clamp(1.6rem,3.6vw,2.5rem)] leading-[1.25] tracking-[-0.02em] text-ink-strong">
                你的设备已经够多了。
                <br />
                让它们像一个整体一样工作。
              </h2>
              <p className="mx-auto mt-4 max-w-xl text-[0.88rem] leading-[1.9] text-ink-muted">
                不需要迁移工作流，也不需要把数据交给第三方。装上，配对，然后把注意力放回真正的问题。
              </p>
              <div className="mt-7 flex flex-wrap justify-center gap-3">
                <a
                  href={GITHUB}
                  target="_blank"
                  rel="noreferrer noopener"
                  className="btn btn-primary"
                >
                  <IconGithub size={15} />
                  从 GitHub 开始
                </a>
                <a href="#start" className="btn btn-ghost">
                  回到快速开始
                  <IconArrowRight className="h-4 w-4" />
                </a>
              </div>
            </div>
          </div>
        </section>
      </main>

      {/* ------------------------------------------------ footer */}
      <footer className="site-footer">
        <Starfield className="footer-stars" />
        <div className="shell pt-14 sm:pt-16">
          <div className="grid grid-cols-1 gap-10 sm:grid-cols-2 lg:grid-cols-[1.5fr_1fr_1fr_1fr]">
            <div>
              <a href="#top" className="brand" aria-label="回到顶部">
                <Logo className="brand-mark h-6 w-6" />
                <span>
                  OpenPanda
                  <span className="brand-tld">/ ORCHESTRATION</span>
                </span>
              </a>
              <p className="mt-4 max-w-xs text-[0.8rem] leading-[1.85] text-ink-muted">
                开源、本地优先的多 Agent 协同操作系统。把异构的智能体与设备，编成一张会自己调度、监督、验收的网。
              </p>
              <div className="mt-5 inline-flex items-center gap-2 font-mono text-[8.5px] uppercase tracking-[0.14em] text-ink-faint">
                <span className="live-dot h-1.5 w-1.5 rounded-full bg-accent" />
                no cloud relay · data stays home
              </div>
            </div>

            <div>
              <div className="footer-heading">产品</div>
              <ul className="space-y-2.5">
                {NAV_LINKS.map((link) => (
                  <li key={link.href}>
                    <a className="footer-link" href={link.href}>
                      {link.label}
                    </a>
                  </li>
                ))}
              </ul>
            </div>

            <div>
              <div className="footer-heading">资源</div>
              <ul className="space-y-2.5">
                <li>
                  <ExternalLink href={GITHUB}>GitHub 仓库</ExternalLink>
                </li>
                <li>
                  <ExternalLink href={`${GITHUB}/releases`}>Releases</ExternalLink>
                </li>
                <li>
                  <ExternalLink href={`${GITHUB}/tree/main/docs`}>文档</ExternalLink>
                </li>
                <li>
                  <ExternalLink
                    href={`${GITHUB}/blob/main/README.zh-CN.md`}
                  >
                    中文 README
                  </ExternalLink>
                </li>
              </ul>
            </div>

            <div>
              <div className="footer-heading">项目</div>
              <ul className="space-y-2.5">
                <li>
                  <ExternalLink href={`${GITHUB}/issues`}>Issues</ExternalLink>
                </li>
                <li>
                  <ExternalLink href={`${GITHUB}/blob/main/CONTRIBUTING.md`}>
                    Contributing
                  </ExternalLink>
                </li>
                <li>
                  <ExternalLink href={`${GITHUB}/blob/main/SECURITY.md`}>
                    Security
                  </ExternalLink>
                </li>
                <li>
                  <ExternalLink href={`${GITHUB}/blob/main/LICENSE`}>
                    AGPL-3.0
                  </ExternalLink>
                </li>
              </ul>
            </div>
          </div>

          <span className="footer-word" aria-hidden="true">
            OpenPanda
          </span>

          <div className="footer-bottom">
            <span>© 2026 OpenPanda contributors</span>
            <span>AGPL-3.0-or-later · 静态页面 · 无追踪脚本</span>
            <span className="hidden sm:inline">LOCAL-FIRST / NO CLOUD RELAY</span>
          </div>
        </div>
      </footer>

      <RevealController />
    </>
  );
}
