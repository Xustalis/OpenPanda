// SPDX-License-Identifier: AGPL-3.0-or-later

// Two languages, one document. Chinese is the markup — every translatable node
// carries data-i18n and its Chinese copy sits inside it — so the page is
// complete, indexable and readable with no JavaScript at all. English lives in
// the table below and is swapped in on demand; switching back restores the
// original innerHTML captured on first use, which is why there is no Chinese
// table to drift out of sync with the page.
//
// Keys are flat and section-scoped (s3.flow2.d) so a missing string is
// greppable. Values may contain markup (<br>, <b>, links) — they are applied
// with innerHTML, and the snapshot is taken before the first swap.

export const LOCALES = ['zh', 'en']
export const STORAGE_KEY = 'openpanda.site.lang'

/** Strings the script itself renders — the palette and its toasts. */
export const UI = {
  zh: {
    kinds: { sec: '章节', act: '行动', link: '链接' },
    empty: '没有匹配项。试试「安装」「许可」「graph」。',
    count: (n) => `${n} 项`,
    copied: '已复制到剪贴板',
    copyFail: '复制失败，请手动选择',
  },
  en: {
    kinds: { sec: 'Section', act: 'Action', link: 'Link' },
    empty: 'Nothing matches. Try “install”, “licensing”, “graph”.',
    count: (n) => `${n} ${n === 1 ? 'result' : 'results'}`,
    copied: 'Copied to clipboard',
    copyFail: 'Copy failed — select it manually',
  },
}

export const EN = {
  /* ── document metadata ─────────────────────────────────────────────── */
  'meta.title': 'OpenPanda — make heterogeneous agents and devices work as one',
  'meta.desc':
    'OpenPanda is a local-first graph orchestration kernel. It does not replace Claude Code or Codex — it wires the agents already on your machines into one network, with intent, decomposition, routing, supervision and acceptance all kept local.',
  'meta.ogDesc':
    'A local-first graph orchestration kernel: Claude Code, Codex and every device you own, wired into one network that schedules, supervises and accepts its own work. No vendor relay.',
  'meta.ogAlt': 'OpenPanda — the project title over deep space and the limb of Earth',

  skip: 'Skip to content',
  'lang.switch': 'Switch language',

  /* ── chrome ────────────────────────────────────────────────────────── */
  'nav.graph': 'Star → Graph',
  'nav.product': 'Product',
  'nav.constitution': 'Thin Constitution',
  'nav.roadmap': 'Roadmap',
  'nav.license': 'Licensing',
  'nav.faq': 'FAQ',
  'nav.install': 'Install',
  'nav.why': 'Four Walls',
  'nav.palette': 'Quick jump (⌘K)',
  'nav.menu': 'Open menu',
  'nav.primary': 'Primary navigation',
  'menu.label': 'INDEX',
  'menu.aria': 'Navigation',

  /* ── hero ──────────────────────────────────────────────────────────── */
  'hero.kicker': 'LOCAL-FIRST ORCHESTRATION KERNEL',
  'hero.title': 'Make heterogeneous agents and devices<br>work as one.',
  'hero.en': '让异构的智能体与设备，像一个整体一样工作。',
  'hero.sub':
    'You say one sentence. Understanding, decomposition, dispatch, supervision, acceptance — it handles all of it. The hands doing the work are the Claude Code and Codex you already installed, on the machines already on your desk.',
  'hero.learn': 'Read the map',
  'hero.copy': 'Copy install command',
  'hero.idx.why': 'Four Walls',
  'hero.idx.graph': 'Star → Graph',
  'hero.idx.product': 'Product',
  'hero.idx.constitution': 'Thin Constitution',
  'hero.idx.install': 'Install',
  'hero.credit': 'ESO/S. BRUNIER · CC BY 4.0 · NASA BLUE MARBLE / DNB · PUBLIC DOMAIN',
  'hero.scroll': 'SCROLL',

  /* ── adapters ribbon ───────────────────────────────────────────────── */
  'adapters.label': 'No need to replace the agents you run',
  'adapters.any': 'Any headless CLI',
  'adapters.aria': 'Supported agents and executors',

  /* ── 01 · four walls ───────────────────────────────────────────────── */
  's1.label': 'THE FOUR WALLS',
  's1.title': 'One sentence, four walls.',
  's1.lede':
    '“Fix issue #12, run the tests on the build box, send the result to my phone.” Routine for a person; physically impossible for a single agent.',
  's1.wall1.t': 'Physics',
  's1.wall1.d':
    'You write code on the laptop, run tests on the build box, notify through the phone. No model, however strong, grows a second body.',
  's1.wall2.t': 'Memory',
  's1.wall2.d':
    'Twenty task threads crammed into one context dilute its attention. Multi-agent starts with context isolation.',
  's1.wall3.t': 'Time',
  's1.wall3.d':
    'Independent branches queue up serially and their latencies add. Three parallel jobs get scheduled as three steps.',
  's1.wall4.t': 'Life and death',
  's1.wall4.d':
    'One agent crashes and the task is gone. No other node knows how far it got.',
  's1.close':
    'The question was never “should we go multi-agent”, it is how these agents are organised with each other.',

  /* ── 02 · star → graph ─────────────────────────────────────────────── */
  's2.label': 'FROM STAR TO GRAPH',
  's2.title': 'Today’s multi-agent systems<br>all orbit a centre.',
  's2.lede':
    'A MainAgent splits the task, hands out work, waits for reports. It is scheduler, message bus and single point of failure at once.',
  's2.star': 'Star',
  's2.star.hop': 'report → relay · two hops',
  's2.star.note': 'Centre = scheduler · message bus · single point of failure',
  's2.star.aria': 'Star topology: every message between the four stages is relayed by the central MainAgent',
  's2.stage.a': 'Stage A',
  's2.stage.b': 'Stage B',
  's2.stage.c': 'Stage C',
  's2.stage.d': 'Stage D',
  's2.graph': 'Graph',
  's2.graph.root': 'Origin',
  's2.graph.chip': 'chat skips the centre',
  's2.graph.sub': 'Subtask',
  's2.graph.note': 'Siblings talk directly · nodes delegate further down',
  's2.graph.aria': 'Graph topology: sibling nodes message each other directly, without a centre',
  's2.body':
    'OpenPanda gives the edges back to the nodes. Sibling tasks exchange bounded short messages directly, and a running node can delegate further down. There is no fixed centre — whoever starts the work is the centre.',
  's2.quote': '“No centre does not mean no order.”',

  /* ── 03 · product ──────────────────────────────────────────────────── */
  's3.label': 'THE PRODUCT',
  's3.title': 'Drop one sentence in,<br>a network finishes it.',
  's3.lede':
    'OpenPanda is not an agent itself. It reads intent, decomposes the work, routes by capability card to the best node, supervises execution and accepts the result; the hands are the Claude Code, Codex and machines you already have.',
  's3.tabs.aria': 'Product views',
  's3.tab.console': 'Console',
  's3.tab.term': 'Terminal',
  's3.rec.console': 'OpenPanda — Console · real screenshot',
  's3.rec.badge': 'RECORDED',
  's3.rec.term': 'panda — recorded session · zsh',
  's3.rec.tag': 'REC · 81s',
  's3.shot.console':
    'OpenPanda console task detail: task 01a1b154 is done, showing the real uptime output and the model conclusion',
  's3.shot.term':
    'OpenPanda terminal recording: panda version, panda doctor, panda queue, a panda ask task running to completion, then panda queue again',
  's3.demo.fallback':
    'This browser cannot play inline video. Download openpanda-demo.mp4 from the same directory.',
  's3.demo.link': 'Full recording · MP4 626 KB',
  's3.caption':
    'Recorded 2026-11-06 on a source build of v0.1.4. panda ask had the model run uptime locally; task 01a1b154 finished through the opencode adapter in 43 s. The console screenshot and the terminal video come from that same run.',
  's3.flow1.t': 'Read the intent',
  's3.flow1.d':
    'First decide whether this is chat, a lookup, or real work. Only the third goes further.',
  's3.flow2.t': 'Split into a task graph',
  's3.flow2.d':
    'One job becomes steps; each step’s artifact feeds the next. How it gets dispatched is not your problem.',
  's3.flow3.t': 'Route by capability card',
  's3.flow3.d':
    'Who owns that machine, who can run that tool — it is on the card. Match conditions, do not rotate a queue.',
  's3.flow4.t': 'Supervise, accept, deliver',
  's3.flow4.d':
    'Every step is judged, and a failure is re-dispatched with its cause. Irreversible actions stop and wait for your nod.',

  /* ── 04 · thin constitution ────────────────────────────────────────── */
  's4.label': 'THE THIN CONSTITUTION',
  's4.title': 'Freeze relations,<br>not cognition.',
  's4.lede':
    'Orchestration frameworks freeze how to think — the moment the model improves, the scaffold becomes a rope. MCP / A2A freezes how to talk — that layer is a commodity. OpenPanda freezes four kinds of relational fact only: identity, authorisation, boundary, lifecycle.',
  's4.table.col1': 'Framework',
  's4.table.col2': 'Freezes',
  's4.table.col3': 'When the model improves',
  's4.table.r1.freeze': 'Cognition',
  's4.table.r1.when': 'The scaffold becomes a rope; rewrite it',
  's4.table.r2.freeze': 'Syntax',
  's4.table.r2.when': 'A commodity layer; no added value',
  's4.table.r3.freeze': 'Relations',
  's4.table.r3.when': 'Every node gets smarter; the network gets stronger',
  's4.link': 'View project status',
  's4.diagram.outer': 'Agent / device',
  's4.diagram.outer.sub': 'replaceable',
  's4.diagram.mid': 'Cognition',
  's4.diagram.mid.sub': 'left to the model',
  's4.diagram.inner': 'Relations',
  's4.diagram.inner.sub': 'frozen',
  's4.diagram.caption': 'Identity · authorisation · boundary · lifecycle',
  's4.quote': '“The stronger the AI, the stronger the project — as long as only relations are frozen.”',

  /* ── 05 · capabilities ─────────────────────────────────────────────── */
  's5.label': 'CAPABILITIES',
  's5.title': 'Every edge on the graph<br>has a protocol.',
  's5.lede':
    'Not a pile of features, but a set of mechanisms that let nodes trust each other, exchange work, and carry on after the link drops.',
  's5.c1.t': 'Intent and task graph',
  's5.c1.d':
    'Multi-stage work unfolds into a DAG, artifacts wired between stages; consecutive steps on one machine stay one task, not three.',
  's5.c2.t': 'Supervision and failover',
  's5.c2.d':
    'Dead credentials inject a fallback model; a lost node has its task migrated. Re-dispatch reruns the failed step, not the whole chain.',
  's5.c3.t': 'Capability-card routing',
  's5.c3.d':
    'The card records hardware, agents and executors; hard constraints filter first, fit ranks second, and multi-hop routes price only the next hop.',
  's5.c4.t': 'Sibling message board',
  's5.c4.d':
    'Siblings under one plan or parent message each other directly; slot overwrite, three granularities, a per-round budget.',
  's5.c5.t': 'Three trust tiers',
  's5.c5.d':
    'mesh / guarded / open pinned to Ed25519 identity; weakest-hop clamping, screen content mesh-only.',
  's5.c6.t': 'Disconnection and approval',
  's5.c6.d':
    'DTN bundles store and forward, delivered when the link returns; reversible actions run, irreversible ones wait for a human.',
  's5.more':
    'Also: two-layer memory · SKILL.md · session tree · actuator / computer-use · MCP / RPC · five interface languages',

  /* ── 06 · proof ────────────────────────────────────────────────────── */
  's6.label': 'IN THE FIELD',
  's6.title': 'Three real machines,<br>not a demo.',
  's6.lede':
    'macOS ↔ Windows ↔ Linux dev board, a real mesh: multi-hop delegation, killing a node mid-run, reconnecting after a drop, message replay.',
  's6.stat1': 'real machines',
  's6.stat2': 'agent adapters',
  's6.stat3': 'automated tests',
  's6.stat4': 'resident memory',
  's6.stat5': 'vendor relay',
  's6.stat6': 'release targets',
  's6.bound.label': 'KNOWN BOUNDARIES',
  's6.boundaries':
    'message board multi-machine exchange pending · servo / camera hardware not yet fired on real devices · voice verified on the degraded path only · per-node key distribution to do',
  's6.status': 'See the full project status →',

  /* ── 07 · three scales ─────────────────────────────────────────────── */
  's7.label': 'THREE SCALES',
  's7.title': 'One kernel,<br>three scales.',
  's7.lede':
    'Deep space is not another market — it is the same problem at its limit: heterogeneous nodes, intermittent links, incomplete trust.',
  's7.today': 'Today',
  's7.today.sub': 'Your desk',
  's7.today.d': 'Laptop + build box + dev board. Verified on real hardware.',
  's7.tomorrow': 'Tomorrow',
  's7.tomorrow.sub': 'Device and robot fleets',
  's7.tomorrow.d': 'Multiple robots, IoT, drones. Peripheral drivers and capability routing already there.',
  's7.far': 'Far',
  's7.far.sub': 'Deep space',
  's7.far.d': 'Satellite constellations, far-side relay. The DTN protocol has landed.',
  's7.quote':
    '“The satellites launched early are weaker but far away; the new ones are stronger but close — they do different things, but they are one whole.”',
  's7.cite': 'OpenPanda Vision',

  /* ── 08 · roadmap ──────────────────────────────────────────────────── */
  's8.label': 'TRAJECTORY',
  's8.title': 'Flight plan.',
  's8.lede':
    'The current version is v0.1.5 Constellation; the last stable, v0.1.4, has been succeeded by it.',
  's8.m1.name': 'Apoapsis',
  's8.m1.d': 'Cross-node delegation, worktree following, actuator dispatch.',
  's8.m1.state': 'Released',
  's8.m2.name': 'Rendezvous',
  's8.m2.d': 'Session AEAD, Bluetooth-style pairing, three trust tiers, multi-hop relay, computer-use.',
  's8.m2.state': 'Stable',
  's8.m3.name': 'Constellation',
  's8.m3.d':
    'The current cut of the release-grade line: cross-network mesh, same-project parallelism, audit close-out, one integrated console, plus native dialects, the terminal roster and per-task work directories.',
  's8.m3.state': 'Current',
  's8.m4.name': 'Assistant',
  's8.m4.d':
    'Desktop shell (tray residency, global hotkeys, self-update), dense mesh layer, honest status reporting.',
  's8.m4.state': 'Planned',
  's8.note':
    'The one-liner resolves the latest stable (v0.1.5); to install a specific version, add --version to the installer script (or -Version on Windows).',

  /* ── 09 · licensing ────────────────────────────────────────────────── */
  'lic.title': 'Open source,<br>with the price in plain sight.',
  'lic.lede':
    'The code is AGPL-3.0-or-later. Personal use, study and internal company tools cost nothing. Only when you lock it inside a closed product does licensing come up — and that is written in the licence, not in a sales pitch.',
  'lic.c1.flag': 'Default path',
  'lic.c1.desc': 'Personal use, study, internal tools, further development.',
  'lic.c1.l1': 'All source and prebuilt artifacts',
  'lic.c1.l2': '11 agent adapters, growing',
  'lic.c1.l3': 'Modify freely; distribute changes under the same licence',
  'lic.c1.l4': 'GitHub Issues support',
  'lic.c1.cta': 'Start installing <span aria-hidden="true">→</span>',
  'lic.c2.price': 'Quoted per case',
  'lic.c2.desc': 'Make it part of your product without opening your product.',
  'lic.c2.l1': 'Closed-source embedding and distribution',
  'lic.c2.l2': 'Hosting a modified version (SaaS)',
  'lic.c2.l3': 'OEM / hardware preload',
  'lic.c2.l4': 'Trademark and brand licence',
  'lic.c2.cta': 'Describe your use <span aria-hidden="true">→</span>',
  'lic.c3.price': 'Annual contract',
  'lic.c3.desc': 'Beyond the licence: someone accountable for delivery.',
  'lic.c3.l1': 'Architecture review and integration plan',
  'lic.c3.l2': 'Private adapter / executor development',
  'lic.c3.l3': 'Security assessment and compliance material',
  'lic.c3.l4': 'Response times and upgrade support',
  'lic.c3.cta': 'Book a call <span aria-hidden="true">→</span>',
  'lic.note':
    'Not sure whether your use counts as “closed-source distribution”? <a href="mailto:fing2024@outlook.com?subject=OpenPanda%20licensing">Describe it in one line</a> — usually answered the same day.',

  /* ── 10 · FAQ ──────────────────────────────────────────────────────── */
  'faq.title': 'Things you might ask.',
  'faq.lede':
    'On positioning, licensing, where data goes, and whether it is usable today — ordered by how often they come up.',
  'faq.ask': 'No answer? Open an issue',
  'faq.link.docs': 'Docs and quick start',
  'faq.link.status': 'Project status and known boundaries',
  'faq.link.changelog': 'Changelog',
  'faq.q1.q': 'How is OpenPanda different from LangGraph or CrewAI?',
  'faq.q1.a':
    'Those are orchestration frameworks; they freeze how to think — how nodes, edges and state machines are written. OpenPanda freezes relational facts: identity, authorisation, boundary, lifecycle. It does not write agents for you, it wires the agents you already installed into a network that can be routed, supervised and accepted.',
  'faq.q2.q': 'Does it need a GPU or extra model quota?',
  'faq.q2.a':
    'No GPU. Routing, supervision and acceptance all use the model provider you already configured — local model or API, either works. The daemon sits at about 40MB resident when idle and barely touches the CPU.',
  'faq.q3.q': 'Does my data pass through your servers?',
  'faq.q3.a':
    'No, and there are no “our servers”. Nodes connect peer to peer, session content is AEAD-encrypted and clamped hop by hop against the three trust tiers. When the link drops, DTN store-and-forward carries it until the link returns.',
  'faq.q4.q': 'Which platforms and agents are supported?',
  'faq.q4.a':
    'Six release targets: macOS, Linux and Windows × amd64 / arm64, plus a lite-linux variant. Eleven adapters today — Claude Code, Codex, Antigravity, OpenCode, Grok Build, Hermes, OpenClaw, Pi, DeepSeek Harness, ZCode, and a generic one that takes any other headless CLI.',
  'faq.q5.q': 'Can I use it in production?',
  'faq.q5.a':
    'v0.1.5 “Constellation” is the current cut of the release-grade line (that line went release-grade at v0.1.0) and holds up day to day. A single machine, a three-machine mesh, and a real cross-network multi-hop (Mac → relay → cloud host, 3 MiB project tree hash identical) all run; self-use and internal tooling are fine, but exposing it to the public is not advised yet. What is unfinished is listed item by item in <a href="https://github.com/Xustalis/OpenPanda/blob/main/docs/status.md" target="_blank" rel="noopener">status.md</a>: UPnP/NAT-PMP port mapping, source-signature convergence for punch and directory frames, and the daemon→panel mesh-metrics IPC.',
  'faq.q6.q': 'Is it free? How does licensing work?',
  'faq.q6.a':
    'Personal, study and internal company use cost nothing. Only when you distribute or host a modified version does <a href="https://github.com/Xustalis/OpenPanda/blob/main/LICENSE" target="_blank" rel="noopener">AGPL</a> require you to open your changes; if you would rather not, that is when a commercial licence comes in — just <a href="mailto:fing2024@outlook.com">email</a>.',
  'faq.q7.q': 'How do I get involved?',
  'faq.q7.a':
    'Issues and PRs are both welcome. main carries 10 required checks and all of them run before a merge. You can also star the repo, or pick one of the known boundaries in status.md and reproduce it.',
  'faq.q8.q': 'Will there be a desktop app?',
  'faq.q8.a':
    'v0.1.5 keeps the kernel release-grade: the CLI plus the local console are already usable daily, and config, data and protocol stay forward-compatible. The desktop shell sits on the v0.1.x line — tray residency, global hotkeys, native notifications, self-update, plus the dense mesh layer.',

  /* ── 11 · install ──────────────────────────────────────────────────── */
  's9.label': 'LIFTOFF',
  's9.title': 'One command,<br>ignition.',
  's9.note':
    'The installer picks the artifact for your platform and architecture, verifies <span class="nowrap">SHA-256</span>, unpacks it and writes it into PATH. Then run <b>panda init</b> once. It installs the latest stable by default; pin a version with <b>--version</b> on macOS / Linux, or <b>-Version</b> on Windows.',
  's9.tab.unix': 'macOS / Linux',
  's9.tabs.aria': 'Install method',
  's9.copy': 'Copy',
  's9.s1': 'Initialise the node, model provider and capability card.',
  's9.s2': 'Enter the interactive TUI console.',
  's9.s3': 'One-shot: answer, tool call, or dispatch.',
  's9.license':
    '<a href="https://github.com/Xustalis/OpenPanda/blob/main/LICENSE" target="_blank" rel="noopener">AGPL-3.0-or-later</a> · <a href="https://github.com/Xustalis/OpenPanda/blob/main/COMMERCIAL.md" target="_blank" rel="noopener">Commercial licence</a> · <a href="https://github.com/Xustalis/OpenPanda/blob/main/TERMS.md" target="_blank" rel="noopener">Terms of use</a>',
  's9.star': 'View / star on GitHub ↗',
  's9.all': 'All archives and checksums →',

  /* ── footer ────────────────────────────────────────────────────────── */
  'foot.tag': 'Local-first, forever.',
  'foot.product': 'Product',
  'foot.why': 'Why a graph',
  'foot.thin': 'Thin Constitution',
  'foot.roadmap': 'Roadmap',
  'foot.faq': 'FAQ',
  'foot.install': 'Install',
  'foot.resources': 'Resources',
  'foot.docs': 'Docs',
  'foot.status': 'Status',
  'foot.releases': 'Download & verify',
  'foot.changelog': 'Changelog',
  'foot.contact': 'Community',
  'foot.security': 'Security policy',
  'foot.invest': 'Partnership / investment',
  'foot.green': 'main must be all green to merge',
  'foot.checks': '10 required checks · 6 release targets · weekly security scan',
  'foot.latest': 'Latest v0.1.5 Constellation <span aria-hidden="true">→</span>',
  'foot.credit':
    'Imagery: <a href="https://www.eso.org/public/images/eso0932a/" target="_blank" rel="noopener">ESO/S. Brunier Milky Way panorama</a> (<a href="https://creativecommons.org/licenses/by/4.0/" target="_blank" rel="noopener">CC BY 4.0</a>; cropped, colour-graded and composited with procedural stars); NASA Blue Marble, Black Marble DNB and Earth cloud imagery (public domain). Code released under AGPL-3.0-or-later; closed-source embedding and hosted modifications are licensable separately.',
  'foot.built': 'Built for machines you own.',
  'foot.top': 'Back to top',

  /* ── ⌘K palette ────────────────────────────────────────────────────── */
  'pal.aria': 'Quick jump',
  'pal.results': 'Results',
  'pal.placeholder': 'Jump to a section, open a link, copy a command…',
  'pal.select': 'select',
  'pal.open': 'open',

  /* ── site pages: docs / download / changelog / status ────────────────── */
  'cl.current': 'Current',
  'cl.first': 'Release-grade start',
  'cl.h1': 'Release notes',
  'cl.lede': 'The 0.1.x line is release-grade. Each entry below is a summary; the full changelog in five languages lives in the repository.',
  'cl.more': 'Download this release →',
  'cl.older.d': 'The 0.0.x preview line (0.0.1 through 0.0.12-preview) is recorded in full, including wire-breaking changes. Those builds are kept for users who pinned a version explicitly.',
  'cl.older.link': 'Full changelog (English / Chinese / Japanese / Spanish / German) ↗',
  'cl.older.t': 'Earlier releases',
  'cl.promise.d': 'A 0.1.x patch may add capability and fix defects, but it does not break configuration, stored data or the wire. Anything that breaks one of those three belongs in a minor (0.2.0). An unavoidable break still gets a deprecation window and a Breaking changes entry rather than arriving unannounced.',
  'cl.promise.t': 'The compatibility promise',
  'cl.unreleased.d': 'Work merged after v0.1.5 has no version number yet. The full entries live in the repository CHANGELOG.md.',
  'cl.unreleased.nav': 'In development',
  'cl.unreleased.t': 'In development (not yet released)',
  'cl.v010.a': 'Cross-network mesh: two-key identity, blind transit relay, epidemic directory sync and a K=2 rendezvous ladder.',
  'cl.v010.b': 'One project can run in parallel — --paths scopes a task, --isolate gives it a private worktree merged back three-way.',
  'cl.v010.c': 'A release-grade safety posture: destructive commands can no longer pass as reversible, strict sandbox denies reading node secrets, and an unreadable trust assertion fails closed.',
  'cl.v010.d': 'The console became a place to work: a task page that leads with its verdict, a to-do board and a roster that hires a thread\'s own team.',
  'cl.v010.t': 'v0.1.0',
  'cl.v011.a': 'Native OpenAI Responses and Google Gemini dialects, including streaming, tool calls and usage accounting.',
  'cl.v011.b': 'Node rosters and pinning reach the terminal (/team, /node, panda ask --team/--node).',
  'cl.v011.c': 'Cross-process reads and writes became atomic under file locks; UDP datagrams are now framed with the peer\'s X25519 key.',
  'cl.v011.t': 'v0.1.1',
  'cl.v012.a': 'Five permission modes ride every task spec: auto, manual, accept-edits, plan and bypass.',
  'cl.v012.b': 'panda task add --accept runs an executable acceptance contract after the agent finishes, distinguishing regressions from checks that were already red.',
  'cl.v012.c': 'The model registry is grouped by connection (one endpoint + one key = one account); autostart units gained a PATH that can actually find the agent CLIs.',
  'cl.v012.t': 'v0.1.2',
  'cl.v013.a': 'Silent one-shot agent CLIs send heartbeats so a healthy long run is no longer killed as stalled.',
  'cl.v013.b': 'A watchdog timeout exits with 124 and a "timeout" verdict instead of being blamed on the model provider and triggering a redundant rerun.',
  'cl.v013.c': 'The console event feed reconnects on wake, and X-Accel-Buffering: no keeps it live behind a reverse proxy.',
  'cl.v013.t': 'v0.1.3',
  'cl.v014.a': 'Every agent task starts with a deterministic workspace map (git state, layout, language mix, the project\'s own build/verify commands); the completion judge now sees the commands actually executed and whether verification ran.',
  'cl.v014.b': 'Up to four bounded clarification questions before a task is created, and an approval gate before large or risky tasks run: execute, read-only analysis, or cancel.',
  'cl.v014.c': 'The thread\'s rail, file viewer, artifact preview and side conversation became one dock; panda service install registers the console as a user-level service.',
  'cl.v014.d': 'Agents can drive a real headless browser through panda browser and the panda_browser_* tools; project dev servers preview live through a same-origin reverse proxy.',
  'cl.v014.t': 'v0.1.4',
  'cl.v015.t': 'v0.1.5',
  'cl.v015.a': 'network.exposure_mode is new: fresh installs default to loopback, a cloud host uses public, which forces a stable shared secret and panel token. The security docs now describe the bus\'s real default bind and carry a public-host checklist, and the console\'s light-theme colour, duplicate spinner, clipped stat cards and stray theme forks are corrected.',
  'cl.v015.b': 'A bare imperative such as "open the web console" or "start the web service" now runs locally and answers with its output — no model call, no task row, no agent. Agent model and concurrency settings, writable sandbox mode, pairing notifications and the nine-page site ship in the same release; configuration, stored data and the wire format are unchanged.',
  'cl.v015.c': 'A delegated run no longer dies with the peer\'s link, concurrent waiters on one task all receive the result, and a delegated child no longer leaks a waiter. An agent run with no supervisor and no contract is labelled unverified, and a silent success parks for review.',
  'dl.h1': 'Download OpenPanda',
  'dl.lede': 'v0.1.5 “Constellation”, released 2026-11-09. Six prebuilt targets, three lite variants, and a signed checksum file for every archive.',
  'dl.s1.brew': 'Same release channel as the tap',
  'dl.s1.d': 'The installer resolves the right archive for your platform, so these are the only commands most people need.',
  'dl.s1.t': 'One-line install',
  'dl.s1.unix': 'Defaults to the latest stable release',
  'dl.s1.win': 'Runs directly in PowerShell 5.1',
  'dl.s2.all': 'Browse every release asset on GitHub ↗',
  'dl.s2.d': 'Archives are named panda-&lt;version&gt;-&lt;os&gt;-&lt;arch&gt;.tar.gz on macOS and Linux, and panda-&lt;version&gt;-windows-&lt;arch&gt;.zip on Windows.',
  'dl.s2.note': 'The lite-linux variant ships without the embedded web console and TUI, for headless or constrained Linux hosts. It is selected with --lite on the installer.',
  'dl.s2.r1': 'Apple Silicon',
  'dl.s2.r2': 'Intel Mac',
  'dl.s2.r3': 'x86-64 servers and desktops',
  'dl.s2.r4': 'ARM boards and cloud hosts',
  'dl.s2.r5': 'PowerShell Expand-Archive',
  'dl.s2.r6': 'ARM64 Windows',
  'dl.s2.r7': 'No embedded web console / TUI',
  'dl.s2.t': 'Release targets',
  'dl.s3.a': 'checksums.txt lists the SHA-256 of every archive. The installer checks it before unpacking.',
  'dl.s3.b': 'checksums.txt.sig is a detached Ed25519 signature over that file. The installer verifies it when the local openssl supports Ed25519; the self-updater always verifies it.',
  'dl.s3.c': 'macOS ships LibreSSL 3.3, which cannot verify Ed25519. There the installer prints an explicit warning instead of silently skipping, and OPENPANDA_REQUIRE_SIG=1 turns "cannot verify" into a hard failure.',
  'dl.s3.d': 'Windows PowerShell 5.1 has no Ed25519 primitive, so install.ps1 performs the SHA-256 check only. Windows network specifics are documented in docs/install.md.',
  'dl.s3.e': 'A downgrade is refused: after unpacking, the installer compares the new binary\'s migration ceiling with the data directory schema. --force opts out deliberately.',
  'dl.s3.t': 'Verify what you downloaded',
  'dl.s4.a': 'Check for a release / apply it; apply verifies the signature before swapping the binary.',
  'dl.s4.b': 'Re-running the installer upgrades in place. Config, sessions and stored data are preserved.',
  'dl.s4.c': 'Removes the binary, the marked PATH block and the autostart unit. --purge also deletes the data directory; --backup-only archives it first.',
  'dl.s4.t': 'Upgrade & uninstall',
  'dl.s5.d': 'Pass --version 0.1.3 on macOS / Linux or -Version 0.1.3 in PowerShell to pin a release. Pre-releases must always be named explicitly — "latest" only ever means the newest stable version.',
  'dl.s5.link': 'Release history and what changed →',
  'dl.s5.t': 'Other versions',
  'docs.h1': 'Documentation',
  'docs.lede': 'Installation, commands and architecture for v0.1.5, kept on the site instead of behind a repository link.',
  'docs.s1.c1': 'Initialise the node, model provider and capability cards.',
  'docs.s1.c2': 'Drop into the interactive TUI console.',
  'docs.s1.c3': 'One-shot: an answer, a tool call or a dispatched task.',
  'docs.s1.d': 'Three commands from a fresh install to a first answer. The binary, adapters and config all live in your own user directory — no root, no daemon you have to babysit.',
  'docs.s1.note': 'panda init asks for a model provider. A local model or an API both work; no GPU is required, and the idle daemon sits at roughly 40 MB RSS.',
  'docs.s1.t': 'Quick start',
  'docs.s2.brew': 'The Homebrew tap follows the same release channel.',
  'docs.s2.d': 'All three paths install the same thing: one panda binary, the agent adapters and the example config, placed under your user directory.',
  'docs.s2.link': 'All archives, checksums and install flags →',
  'docs.s2.note': 'The installer picks the archive for your OS and architecture, checks SHA-256, verifies the Ed25519 signature where openssl supports it, unpacks, and adds the binary to PATH. Re-running the same command upgrades in place and keeps your data.',
  'docs.s2.t': 'Install & upgrade',
  'docs.s2.unix': 'One-line installer; defaults to the latest stable release.',
  'docs.s2.win': 'PowerShell 5.1, no BOM, no extra prerequisites.',
  'docs.s3.d': 'The commands used most often. panda help prints the complete registry.',
  'docs.s3.r1': 'Binary version and database schema, as JSON.',
  'docs.s3.r2': 'Diagnose config, model endpoints and network reachability.',
  'docs.s3.r3': 'List mesh peers / pair a new device.',
  'docs.s3.r4': 'Dispatch a task with an executable acceptance contract.',
  'docs.s3.r5': 'Discover model rows from configs already on this machine.',
  'docs.s3.r6': 'Register the console as a user-level service (launchd / systemd --user / logon task).',
  'docs.s3.r7': 'Check for a release / apply it with mandatory signature verification.',
  'docs.s3.r8': 'Read a file around a line in the terminal.',
  'docs.s3.r9': 'Remove the binary, the PATH marker block and the autostart unit.',
  'docs.s3.t': 'Command reference',
  'docs.s4.a1.d': 'Two Ed25519 keys per node — a stable signing key and a rotating one. Peers authorise by signature, not by IP address.',
  'docs.s4.a1.t': 'Identity',
  'docs.s4.a2.d': 'Per-connection AEAD encryption. When a link drops, DTN store-and-forward holds the bundle until the path comes back.',
  'docs.s4.a2.t': 'Transport',
  'docs.s4.a3.d': 'Three trust tiers — mesh / guarded / open — clamp permissions, screens and model credentials hop by hop according to the source.',
  'docs.s4.a3.t': 'Boundaries',
  'docs.s4.a4.d': 'A task carries a delivery contract. The completion judge sees the commands actually run and whether a declared verification entry really passed.',
  'docs.s4.a4.t': 'Acceptance',
  'docs.s4.d': 'Four invariants the kernel enforces, independent of which agent or model does the work.',
  'docs.s4.note': 'Frame formats and the wire contract live in docs/protocol.md.',
  'docs.s4.t': 'Architecture & trust',
  'docs.s5.l1': 'Install, uninstall, restricted networks (Chinese / English) ↗',
  'docs.s5.l2': 'Protocol, frames and DTN bundles ↗',
  'docs.s5.l3': 'Frequently asked questions ↗',
  'docs.s5.l4': 'Project status and known limits →',
  'docs.s5.l5': 'Full changelog →',
  'docs.s5.l6': 'Source, issues and pull requests ↗',
  'docs.s5.l7': 'Agent models, concurrency and hot reload (five languages) ↗',
  'docs.s5.t': 'Further reading',
  'hub.cl.d': 'Every change in the five 0.1.x releases, plus the compatibility promise.',
  'hub.cl.t': 'Release notes',
  'hub.dl.d': 'Six release targets, the lite variant, SHA-256 and Ed25519 signature checks.',
  'hub.dl.t': 'Download & verify',
  'hub.docs.d': 'Quick start, command reference, install flags, architecture and the trust model.',
  'hub.docs.t': 'Documentation',
  'hub.lede': 'Documentation, downloads, release notes, current boundaries, capabilities, the security model, community and the FAQ each have their own address — bookmark one or send it to a colleague.',
  'hub.st.d': 'Verified deployments, unfinished boundaries and known limitations.',
  'hub.st.t': 'Project status',
  'hub.title': 'Site map.',
  'meta.cl.desc': 'Every OpenPanda 0.1.x release, what changed, and the compatibility promise behind the release-grade version line.',
  'meta.cl.title': 'Release notes — OpenPanda',
  'meta.dl.desc': 'Prebuilt OpenPanda v0.1.5 binaries for macOS, Linux and Windows on amd64/arm64, plus SHA-256 checksums and Ed25519 release signatures.',
  'meta.dl.title': 'Download — OpenPanda v0.1.5',
  'meta.docs.desc': 'Install, command reference, architecture and trust model for the OpenPanda v0.1.5 local-first orchestration kernel.',
  'meta.docs.title': 'Documentation — OpenPanda',
  'meta.st.desc': 'What OpenPanda v0.1.5 has been verified to do, what is deliberately unfinished, and the known limitations kept in docs/status.md.',
  'meta.st.title': 'Project status — OpenPanda',
  'nav.changelog': 'Changelog',
  'nav.docs': 'Docs',
  'nav.download': 'Download',
  'nav.overview': 'Overview',
  'nav.status': 'Status',
  'pg.copy': 'Copy command',
  'pg.cta.cl': 'See what changed →',
  'pg.cta.dl': 'Download v0.1.5 →',
  'pg.cta.docs': 'Read the docs →',
  'pg.cta.st': 'Current boundaries →',
  'pg.table.what': 'Description',
  'pg.toc.aria': 'On this page',
  'pg.toc.dl': 'Download →',
  'pg.toc.on': 'ON THIS PAGE',
  'st.h1': 'Project status',
  'st.lede': 'What works today, what has not been built, and the limits we will not paper over. Mirrors the latest entry in docs/status.md.',
  'st.s1.a': 'Single-machine use and a trusted LAN of three real machines (Orange Pi / MacBook / Windows) run as designed: a request becomes a local answer, a task or a multi-stage plan, and irreversible operations stop in the approval queue.',
  'st.s1.b': 'A real cross-network multi-hop path (Mac → relay → cloud host) delivered a 3 MiB project tree with an identical hash.',
  'st.s1.c': 'main is protected by 10 required checks; releases ship six platform targets and a weekly security scan runs on a schedule.',
  'st.s1.d': 'The idle daemon holds roughly 40 MB of resident memory and uses almost no CPU.',
  'st.s1.t': 'Verified',
  'st.s2.a': 'UPnP / NAT-PMP automatic port mapping.',
  'st.s2.b': 'Converging the source signatures of punch and directory frames.',
  'st.s2.c': 'A daemon → panel IPC surface for mesh metrics; the panel is a separate sidecar process and cannot read the daemon\'s in-memory counters.',
  'st.s2.t': 'Not built yet',
  'st.s3.a': 'The trust model and sandbox still have open P2 items in the "known limitations" section of docs/status.md.',
  'st.s3.b': 'A /rewind snapshot is still built synchronously on the caller\'s goroutine, and turn numbers drift after context compaction.',
  'st.s3.c': 'Cross-node artifact preview pulls and the file viewer\'s symlink window are not closed out yet.',
  'st.s3.t': 'Known limitations',
  'st.s4.d': 'The v0.1.x line continues with a thin desktop shell — tray presence, global hotkeys, native notifications and self-update — plus a denser mesh layer. The site does not promise a specific version number for it.',
  'st.s4.t': 'What comes next',
  'st.s5.d': 'Issues and pull requests are welcome, and every PR runs the 10 required checks. Reproducing one of the boundaries above is a useful first contribution.',
  'st.s5.l1': 'Open an issue ↗',
  'st.s5.l2': 'Read docs/status.md ↗',
  'st.s5.l3': 'Browse the source ↗',
  'st.s5.t': 'Contributing',
  // ── site expansion: capabilities / security / community / FAQ ──────────
  'ft.s1.t': 'Orchestration & supervision',
  'ft.s1.d': 'It does not write your agents. It turns the ones you already have into a crew whose work can be checked. Every item below has a command or config surface.',
  'ft.s1.f1.t': 'Intent routing & decomposition',
  'ft.s1.f1.d': 'The entry model classifies a prompt as a direct answer, a tool call, a single task or a multi-stage plan. Multi-stage work is decomposed first, with artifacts wired between stages.',
  'ft.s1.f2.t': 'Supervision loop',
  'ft.s1.f2.d': 'The entry model keeps judging completion and re-dispatches with the failure reason attached when an executor dies, until done or the round budget (five by default) is spent.',
  'ft.s1.f3.t': 'Multi-model registry & failover',
  'ft.s1.f3.d': 'panda model add / switch / test manages several providers behind a health breaker. Quota exhaustion or dead credentials inject a fallback model so the task continues.',
  'ft.s1.f4.t': 'Multi-harness runs',
  'ft.s1.f4.d': 'panda task add --agents a,b --mode parallel fans one instruction out as a plan and collects the verdicts, so you read one judged result rather than four answers.',
  'ft.s1.f5.t': 'Tiered approval',
  'ft.s1.f5.d': 'Reversible operations run autonomously. Irreversible ones — git push, deletion, production database changes — suspend until a human approves via panda approve or the console.',
  'ft.s1.f6.t': 'Transparent execution & breakers',
  'ft.s1.f6.d': 'Every shell command, file edit and tool call streams live to the terminal and console with the responsible agent and model. Loop detection and retry breakers stop runaway agents.',
  'ft.s1.note': 'Since v0.1.4, completion is judged on the commands actually executed, failed tool results and whether a declared verification entry really ran. A reply claiming tests passed without evidence is not completion.',
  'ft.s2.t': 'Memory & skills',
  'ft.s2.d': 'Preferences, project facts and reusable workflows are stored separately and travel with delegated tasks.',
  'ft.s2.f1.t': 'Dual-layer memory',
  'ft.s2.f1.d': 'User preferences live in USER.md and project facts in MEMORY.md, kept strictly apart so personal habits never become repository facts.',
  'ft.s2.f2.t': 'Self-evolving skills',
  'ft.s2.f2.d': 'Successful workflows are distilled into SKILL.md playbooks that accumulate and are reused for similar tasks.',
  'ft.s2.f3.t': 'Skills Hub & discovery',
  'ft.s2.f3.d': 'panda skill hub ships an offline catalogue, panda skill import loads from a path, URL or archive, and the assistant can find and install a needed skill mid-flight.',
  'ft.s2.f4.t': 'Traveling context',
  'ft.s2.f4.d': 'Delegated tasks carry project memory and a workspace digest, so the executing node does not have to rediscover the repository.',
  'ft.s3.t': 'Multi-device collaboration',
  'ft.s3.d': 'Nodes are peers — no root, no hosted control plane. The router picks a machine by measured capacity, and DTN holds messages across link outages.',
  'ft.s3.f1.t': 'LAN discovery & pairing',
  'ft.s3.f1.d': 'Beacons surface pending nodes on the LAN and panda nodes admit confirms by fingerprint. panda pair uses a short code checked on both ends and delivers the shared secret sealed.',
  'ft.s3.f2.t': 'Capability cards',
  'ft.s3.f2.d': 'Each node declares its CPU, RAM, OS, available agents and hardware actuators. panda card shows, rescans and edits the card, with hot reload.',
  'ft.s3.f3.t': 'P2P mesh & session AEAD',
  'ft.s3.f3.d': 'Authenticated WebSockets negotiate per-connection AES-256-GCM, keys derived by HKDF from the shared secret and both hello nonces; an encrypted UDP plane carries bulk data.',
  'ft.s3.f4.t': 'Trust zones',
  'ft.s3.f4.d': 'panda nodes trust &lt;id&gt; mesh|guarded|open pins each peer, clamping what delegated work exposes and what inbound work is admitted. Screen content moves only across mesh-tier links.',
  'ft.s3.f5.t': 'Capability routing & multi-hop',
  'ft.s3.f5.d': 'The scheduler scores resource efficiency, priority, node class, wait time and link latency; Dijkstra picks the cheapest first hop on the link-state graph and gossip locates nodes two hops away.',
  'ft.s3.f6.t': 'Traveling worktrees & artifacts',
  'ft.s3.f6.d': 'File tasks carry their checkout to the executor and bring results back. panda task artifact &lt;id&gt; lists and extracts reports, screenshots and other outputs.',
  'ft.s3.note': 'During an outage, DTN bundles are stored, encrypted and signed, then delivered when the link returns. A tampered bundle is never delivered.',
  'ft.s4.t': 'Sessions & embedding',
  'ft.s4.d': 'Conversations are first-class: they can be forked, compacted and driven by other tools.',
  'ft.s4.f1.t': 'Session trees',
  'ft.s4.f1.d': 'panda session fork &lt;id&gt; --at N branches at any turn. Inside a repository the child worktree branches from the parent and inherits the code it produced.',
  'ft.s4.f2.t': 'Auto-compaction',
  'ft.s4.f2.d': 'Overflowing history folds into a model-written running digest instead of being dropped; the stored thread stays whole.',
  'ft.s4.f3.t': 'MCP & RPC surfaces',
  'ft.s4.f3.d': 'panda mcp exposes the node’s own tasks, memory and skills as an MCP stdio server; panda rpc offers NDJSON-over-stdio status and streaming ask.',
  'ft.s4.f4.t': 'Subscription OAuth',
  'ft.s4.f4.d': 'panda auth login anthropic signs in an existing subscription via PKCE with transparent token refresh, without a separate API key.',
  'ft.s5.t': 'Interfaces & runtime',
  'ft.s5.d': 'One kernel, four entry points; the binary is a static Go build with no external runtime.',
  'ft.s5.f1.t': 'Terminal TUI',
  'ft.s5.f1.d': 'A Bubble Tea full-screen console with onboarding, live progress and mid-turn steering; PANDA_CLASSIC_REPL=1 keeps the classic line REPL.',
  'ft.s5.f2.t': 'Web console',
  'ft.s5.f2.d': 'A zero-config board, live SSE events, skills and session management, automatic login; panda web --lan shares it on the LAN behind an ephemeral token.',
  'ft.s5.f3.t': 'Voice entry',
  'ft.s5.f3.d': 'panda voice runs wake → transcribe → dispatch → speak over the same persistent session as the REPL; it needs its own Python voice dependencies.',
  'ft.s5.f4.t': 'Scriptable CLI & self-update',
  'ft.s5.f4.d': 'panda ask drops straight into automation scripts; panda update check / apply stages a binary and swaps it in place only after signature verification.',
  'ft.s5.f5.t': 'Featherweight',
  'ft.s5.f5.d': 'The idle daemon holds about 40MB RSS and barely uses CPU. Storage is pure-Go SQLite, with no CGO requirement.',
  'ft.s6.t': 'Kernel layers',
  'ft.s6.d': 'Package layout taken from the source tree, ordered from entry points down to storage.',
  'ft.s6.h2': 'Responsibility',
  'ft.s6.r1.c2': 'Terminal TUI, web console, CLI and voice entry',
  'ft.s6.r2.c2': 'Intent classification, prompt construction and multi-stage decomposition',
  'ft.s6.r3.c2': 'Three-tier execution, tiered gating, breakers and anti-loop',
  'ft.s6.r4.c2': 'Dual-layer memory and self-evolving SKILL.md',
  'ft.s6.r5.c2': 'Session trees, subscription OAuth and self-update',
  'ft.s6.r6.c2': 'Capability scoring, latency-weighted routing and per-peer trust tiers',
  'ft.s6.r7.c2': 'WS and sealed-UDP transport, session AEAD, Ed25519 identity and capability cards',
  'ft.s6.r8.c2': 'Chunked resumable artifacts, sandbox, network allowlists and secret redaction',
  'ft.s6.r9.c2': 'Pure-Go SQLite in WAL mode',
  'ft.s6.note': 'The full inventory is in the capability ledger in docs/status.md. This page lists stable layers only and does not present experimental modules as shipped.',
  'ft.s7.t': 'What it replaces',
  'ft.s7.d': 'Set the usual approaches side by side and the difference lands on who holds your code and what happens when a node dies.',
  'ft.s7.h1': 'A single agent CLI',
  'ft.s7.h3': 'A hosted orchestration service',
  'ft.s7.r1.c1': 'Drives the CLIs you already pay for',
  'ft.s7.r1.c2': 'One at a time',
  'ft.s7.r1.c3': 'By hand, with scripts',
  'ft.s7.r1.c4': 'Usually its own stack',
  'ft.s7.r1.c5': 'Ten adapters plus a generic bridge',
  'ft.s7.r2.c1': 'Uses every machine you own',
  'ft.s7.r2.c2': 'No',
  'ft.s7.r2.c3': 'You script it',
  'ft.s7.r2.c4': 'Cloud VMs you rent',
  'ft.s7.r2.c5': 'Your own devices, discovered on the LAN',
  'ft.s7.r3.c1': 'A node dies mid-task',
  'ft.s7.r3.c2': 'The task dies',
  'ft.s7.r3.c3': 'The task dies',
  'ft.s7.r3.c4': 'Usually yes',
  'ft.s7.r3.c5': 'Re-dispatched with the failure reason',
  'ft.s7.r4.c1': 'Where code and prompts live',
  'ft.s7.r4.c2': 'Your machine',
  'ft.s7.r4.c3': 'Your machines',
  'ft.s7.r4.c4': 'Their servers',
  'ft.s7.r4.c5': 'Your machines, over an encrypted mesh',
  'ft.s7.r5.c1': 'What it costs',
  'ft.s7.r5.c2': 'Subscription',
  'ft.s7.r5.c3': 'Subscription',
  'ft.s7.r5.c4': 'Per-seat or per-token markup',
  'ft.s7.r5.c5': 'Your existing subscriptions',
  'ft.s7.note': 'This is a design comparison, not a benchmark. What has actually been verified — and what has not — is on the status page.',
  'sc.s1.t': 'Read this first',
  'sc.s1.b1': 'By default every peer is mesh-tier and fully trusted. One compromised node compromises the whole mesh — never extend it to machines you do not fully control.',
  'sc.s1.b2': 'The rule that irreversible actions require your approval holds only on a single machine or a trusted private network. Introduce a peer you do not fully trust and it no longer holds.',
  'sc.s1.b3': 'This page summarises SECURITY.md; it does not replace it. Read the full policy before connecting a production network — it ships in five languages.',
  'sc.s2.t': 'Trust model',
  'sc.s2.d': 'Five boundaries decide who can forge which traffic and which operations require a signature.',
  'sc.s2.f1.t': 'Membership secret & identity signatures',
  'sc.s2.f1.d': 'Bus membership is a shared_secret injected only via OPENPANDA_SHARED_SECRET; hello carries an HMAC-SHA256 bound to a timestamp and per-dial nonce. Tier-2 consent is separately Ed25519-signed and rejected if stripped or altered.',
  'sc.s2.f2.t': 'Per-connection AEAD',
  'sc.s2.f2.d': 'When both hellos advertise sessaead and a shared secret exists, AES-256-GCM is armed after the handshake with HKDF-derived keys bound to both identities and nonces, refreshed per dial. Only links that cannot negotiate it go through the cleartext gate.',
  'sc.s2.f3.t': 'Signed audit chains',
  'sc.s2.f3.d': 'The hash chains in task_events and audit_log carry an Ed25519 signature per row, verified against this node’s public key. An unsigned row appearing inside a signed chain is treated as tail stripping.',
  'sc.s2.f4.t': 'Signed self-update',
  'sc.s2.f4.d': 'Every release ships checksums.txt with a detached Ed25519 signature; the official public key is baked into the binary. A forged checksum delivered over the same channel as the archive is rejected outright.',
  'sc.s2.f5.t': 'Outbound boundary for skill URLs',
  'sc.s2.f5.d': 'panda skill install &lt;url&gt; allows only https (except loopback) and refuses loopback, private and link-local addresses at dial time, so a node cannot become an internal-network probe.',
  'sc.s2.note': 'The UDP datagram plane is always wrapped in AES-256-GCM and DTN bundle v2+ payloads are encrypted and signed too. Cleartext links are allowed only where a lower layer already encrypts the path.',
  'sc.s3.t': 'Integrity verification',
  'sc.s3.d': 'Both commands run locally offline and confirm the audit chain has not been rewritten.',
  'sc.s3.c1.h': 'Verify the global audit chain',
  'sc.s3.c2.h': 'Verify one task’s event chain',
  'sc.s3.note': 'The console GET /api/audit uses the same verification. A missing signature, a stripped signature or a re-signature after key rotation all fail.',
  'sc.s4.t': 'Deployment red lines',
  'sc.s4.d': 'Until per-node key distribution and verifiable admission land, these five are prerequisites, not suggestions.',
  'sc.s4.b1': 'Interconnect only over an encrypted overlay. Bind listen_addr to the overlay interface and never expose the bus port to the public internet or an untrusted LAN. Terminate TLS at a reverse proxy for cross-internet links and write peers as wss://.',
  'sc.s4.b2': 'Inject secrets only through the environment. Provide shared_secret solely via OPENPANDA_SHARED_SECRET — never in config.yaml or a public repository. Generate it with openssl rand -hex 32.',
  'sc.s4.b3': 'Never declare tier-2 capabilities for unknown peers. Irreversible capabilities in capabilities.yaml belong only to nodes you fully trust.',
  'sc.s4.b4': 'Keep the panel on loopback. Bind panel_addr to 127.0.0.1; a non-loopback bind without panel_token generates a one-shot token and warns loudly. Never paste an auto-login URL containing the token into chat or a ticket.',
  'sc.s4.b5': 'Listen on loopback by default. Without a shared_secret the listener does not start at all and the node runs locally only.',
  'sc.s5.t': 'Lifecycle hooks',
  'sc.s5.d': 'Hooks are an optional local extension, off by default; enabling them does not relax the execution boundary.',
  'sc.s5.b1': 'Child processes inherit only an allowlisted environment — no model credentials or peer tokens. Commands pass tier classification first; tier-2 commands are denied unless hooks.allow_tier2 explicitly allows them.',
  'sc.s5.b2': 'Task values arrive only via PANDA_HOOK_* variables. Placeholders such as {title} expand to variable references rather than literals; Windows uses cmd /V:ON delayed expansion so values never reach the command parser.',
  'sc.s5.b3': 'Every execution writes one audit row. Bind task_submitted, task_started, task_done, task_failed or task_review; inspect and dry-run with panda hooks list|test.',
  'sc.s6.t': 'Reporting vulnerabilities',
  'sc.s6.d': 'Do not open a public issue for flaws in transport authentication, the permission tier model or secret redaction.',
  'sc.s6.l1': 'Contact the maintainer privately with reproduction steps ↗',
  'sc.s6.l2': 'Full security policy (Chinese authoritative, five languages) ↗',
  'sc.s6.l3': 'Reporting process in the contributing guide ↗',
  'sc.s7.t': 'Known boundaries',
  'sc.s7.d': 'These are not small bugs to patch; they are structural limits of the current trust model. Closing them means changing the model.',
  'sc.s7.b1': 'A member holding the shared secret can still forge consent for a node whose public key the receiver has never seen — an inherent limit of multi-hop relay plus TOFU. That path logs a warning, and unpinned identities are trusted on first sight.',
  'sc.s7.b2': 'The audit chain’s signature anchor lives in the database itself, so an unsigned historical prefix cannot be distinguished from a wholesale rewrite. External notarisation is still on the roadmap.',
  'sc.s7.b3': 'Per-node key distribution and verifiable admission are not yet in place. A trust tier is a unilateral local assertion: it narrows what you expose to a peer but does not change the underlying fact that holding the shared secret makes you a member.',
  'cm.s1.t': 'Build from source',
  'cm.s1.d': 'Three commands bring up the kernel, CLI and panel. The daemon embeds the console at build time.',
  'cm.s1.c1.h': 'Run the daemon from source against config.yaml',
  'cm.s1.c2.h': 'Build the console into the go:embed directory',
  'cm.s1.c3.h': 'Build the standalone panel sidecar',
  'cm.s1.note': 'Without make web, go build embeds a placeholder page so the tree compiles on a machine with no Node. Run make web before shipping any UI change.',
  'cm.s2.t': 'Toolchain',
  'cm.s2.d': 'The three minimum versions come from CONTRIBUTING.md; builds below them are unsupported.',
  'cm.s2.h2': 'Used for',
  'cm.s2.r1.c3': 'Kernel, CLI, panel and tests',
  'cm.s2.r2.c3': 'Web console (webui/app)',
  'cm.s2.r3.c3': 'Voice sidecar and agent adapters',
  'cm.s2.note': 'Storage is pure-Go SQLite and needs no CGO; only -race on Windows needs a MinGW-w64 toolchain.',
  'cm.s3.b1': 'CI runs the same checks as parallel jobs: lint, test, race, web, adapter contract, TUI PTY and the cross-platform matrix.',
  'cm.s3.b2': 'go test -race ./... must pass. The kernel is concurrent; a race finding is a release blocker, not a warning.',
  'cm.s3.b3': 'core, scheduler and storage should stay near or above ~60% coverage where practical; every bug fix carries a regression test that fails before the fix.',
  'cm.s3.b4': 'New wire protocol or delegation behaviour gets a loopback test; see internal/core/dedup_test.go and scripts/smoke-delegate.',
  'cm.s3.t': 'Engineering gates',
  'cm.s3.d': 'A PR merges only when every gate is green. Run the same checks locally before pushing.',
  'cm.s3.c1.h': 'fmt-check + vet + build + adapter-test + test + race-focused',
  'cm.s3.c2.h': 'gate plus web-gate and both TUI PTY contracts',
  'cm.s3.c3.h': 'Quick typecheck for console edits',
  'cm.s4.t': 'Code conventions',
  'cm.s4.d': 'These are not style preferences. They are constraints bought with reproducible incidents in a concurrent system.',
  'cm.s4.f1.t': 'Errors',
  'cm.s4.f1.d': 'Wrap with %w and check with errors.Is / As. Never discard an error silently and never both log and return it from the same function.',
  'cm.s4.f2.t': 'Comments',
  'cm.s4.f2.d': 'Explain why, not what. A reader six months out needs the invariant, the trade-off or the incident reference; non-obvious concurrency decisions are documented at the site.',
  'cm.s4.f3.t': 'Concurrency',
  'cm.s4.f3.d': 'Every mutex documents what it guards. No lock is held across I/O or a channel send. Senders own closing, and a goroutine’s owner is identifiable from its spawn site.',
  'cm.s4.f4.t': 'Security',
  'cm.s4.f4.d': 'Fail closed: anything that classifies, authorizes or redacts defaults to the restrictive branch. New config keys holding secrets get the 0600 chmod-and-warn treatment; secrets never enter logs.',
  'cm.s5.t': 'Commit style',
  'cm.s5.d': 'Conventional Commits, scoped to a top-level directory, with an imperative subject specific enough to stand alone in git log --oneline.',
  'cm.s5.h0': 'Example',
  'cm.s5.r1.c2': 'A new CLI capability',
  'cm.s5.r2.c2': 'A kernel bug fix',
  'cm.s5.r3.c2': 'A web console capability',
  'cm.s5.note': 'Allowed types: feat, fix, docs, refactor, chore, test. Scopes follow the top-level layout: core, cli, web, scheduler, defense and so on.',
  'cm.s6.t': 'Web console development',
  'cm.s6.d': 'The console is Vite + Preact + TypeScript with no runtime dependencies beyond Preact.',
  'cm.s6.b1': 'All user-facing strings go through i18n. Add the key to every locale under webui/app/src/i18n/ — never ship an English-only key intending to fix it later.',
  'cm.s6.b2': 'English is the fallback, so a missing key silently renders English. That helps debugging; it is not a reason to write English-only keys.',
  'cm.s6.b3': 'The repo also has key-parity guards in internal/i18n and webui parity.test.ts; both go red on a missing key.',
  'cm.s7.t': 'Pull requests',
  'cm.s7.d': 'main is protected: changes go through a PR, every required check must pass, and the ruleset has no bypass actors.',
  'cm.s7.s1': 'Branch from main, keep the change to one topic and include a reproduction or verification path.',
  'cm.s7.s2': 'Run the gates locally; go mod tidy after dependency changes and scan for character pollution after translation edits.',
  'cm.s7.s3': 'Open the PR and wait for all 10 required checks. Merge with rebase, without a merge commit.',
  'cm.s7.note': 'Security flaws do not use this flow: report privately through the channel above and do not describe them in a public PR.',
  'cm.s8.t': 'Conduct & licensing',
  'cm.s8.d': 'Participating implies agreement with the code of conduct; code contributions also touch the CLA and the dual-licensing terms.',
  'cm.s8.l1': 'Contributor Covenant code of conduct ↗',
  'cm.s8.l2': 'Contributor License Agreement ↗',
  'cm.s8.l3': 'Licensing and trademark notes ↗',
  'cm.s8.l4': 'Community and commercial licensing ↗',
  'cm.s8.l5': 'Third-party notices ↗',
  'cm.s9.t': 'Contact & discussion',
  'cm.s9.d': 'There is no forum and no group you have to join. Public discussion stays in the repository; private matters go by email.',
  'cm.s9.l1': 'Open an issue: bugs, reproductions and ideas ↗',
  'cm.s9.l2': 'Browse open pull requests ↗',
  'cm.s9.l3': 'Partnerships, commercial licensing and security reports ↗',
  'fq.g1.t': 'Getting started',
  'fq.g1.q1': 'What should I do first after installing?',
  'fq.g1.a1': 'Run panda init to create config.yaml and the capability card — the node name comes from the hostname and the only question is whether to configure a model now. Then panda opens the TUI, or panda web opens the console. panda doctor checks PATH, config, database, adapters and agent CLIs at any time.',
  'fq.g1.q2': 'What is the difference between panda and panda daemon?',
  'fq.g1.a2': 'Plain panda is interactive; panda daemon is the resident process handling scheduling, heartbeats, reminders and inbound delegations. Note that panda web starts a full node and claims ports 7836/7837 — starting a daemon on the same machine fights for them, so pick one entry point for everyday use.',
  'fq.g1.q3': 'Do I need a GPU or extra model quota?',
  'fq.g1.a3': 'No GPU is needed. Routing, supervision and verification all use the model provider you already configured, local or API; the idle daemon holds about 40MB RSS and barely uses CPU.',
  'fq.g1.q4': 'Does my data pass through your servers?',
  'fq.g1.a4': 'No — there are no servers of ours. Nodes connect peer to peer, session content is clamped hop by hop by trust tier, and outages fall back to DTN store-and-forward. The only traffic leaving your nodes is the model calls you configured.',
  'fq.g2.t': 'Model configuration',
  'fq.g2.q1': 'Which model providers are supported?',
  'fq.g2.a1': 'Any OpenAI-compatible or Anthropic-compatible endpoint: set api_type to anthropic (Messages) or openai (Chat Completions). Since v0.1.1 the entry client also speaks OpenAI Responses and Google Gemini natively.',
  'fq.g2.q2': 'Why does it say no model API key is configured?',
  'fq.g2.a2': 'Pick one: the panda init wizard, the panda web settings page (saved changes apply live, no restart), or the OPENPANDA_MODEL_API_KEY environment variable.',
  'fq.g2.q3': 'Why does it say the model endpoint does not exist?',
  'fq.g2.a3': 'The base_url or model name is wrong. Use the Test connection button in the console settings page to isolate it; Anthropic-compatible endpoints do not carry a /v1 suffix.',
  'fq.g2.q4': 'Does a task die when the quota runs out?',
  'fq.g2.a4': 'No. The multi-model registry has a health breaker: quota exhaustion or dead credentials inject a fallback model. The entry model also retries with exponential backoff, so only sustained rate limiting stops the work.',
  'fq.g3.t': 'Agents & adapters',
  'fq.g3.q1': 'What is an agent adapter?',
  'fq.g3.a1': 'OpenPanda does not run long tasks itself; it delegates to the agent CLIs installed on the machine, and adapters/*.py are the protocol bridges. panda agents lists installed and missing agents with install commands for the latter.',
  'fq.g3.q2': 'Which agents are supported?',
  'fq.g3.a2': 'Ten bundled adapters: Claude Code, Codex, OpenCode, Grok Build, DeepSeek Harness, Antigravity, Hermes, Pi, ZCode and OpenClaw, plus a generic bridge for any headless CLI that speaks stdin/stdout.',
  'fq.g3.q3': 'Can I use it without installing every agent?',
  'fq.g3.a3': 'Yes. Missing agents are fine: the scheduler only routes work to nodes declaring that agent, and you can remove the entry from the capability card. A requires declaration is a hard filter.',
  'fq.g3.q4': 'How do agents get credentials?',
  'fq.g3.a4': 'The default injection.model is auto: agents with their own login use it, and those without reuse the model configured in panda. Switch policy with panda config set injection auto|always|never; panda agents shows which adapters support injection.',
  'fq.g4.t': 'Tasks & scheduling',
  'fq.g4.q1': 'Why is my task stuck in pending?',
  'fq.g4.a1': 'Check in order: is the daemon running (panda nodes, running column), does the capability card exist (the adapters line of panda doctor), and is the target agent installed.',
  'fq.g4.q2': 'What is tier-2 authorisation?',
  'fq.g4.a2': 'Commands come in two tiers: reversible tier-1 work (reads, builds) is allowed, while irreversible tier-2 work (git push, deletion) is denied unless you use panda ask --authorize, REPL /authorize or a capability-card declaration. Denied tasks enter the review queue with a reason.',
  'fq.g4.q3': 'Why did my task go to review?',
  'fq.g4.a3': 'An irreversible action or a supervision loop past its budget pauses for human approval. Inspect with panda queue and decide with panda approve &lt;id&gt; or reject &lt;id&gt;; the console task card has the same buttons.',
  'fq.g4.q4': 'How do I split work across several machines?',
  'fq.g4.a4': 'That is a plan, not a single task: consecutive stages route to suitable machines and dependent artifacts are packed into the next stage. panda plan example writes a template and panda plan run x.yaml --dry-run previews routing; a single sentence also lets the entry model decompose it. No plan stage carries tier-2 consent.',
  'fq.g4.q5': 'What is scope drift?',
  'fq.g4.a5': 'The entry model declares the file scope a task may touch; an out-of-scope write is blocked and moved to review. If a legitimate edit is caught, check that the task description names the paths to change explicitly.',
  'fq.g5.t': 'Sessions & interaction',
  'fq.g5.q1': 'What is a side chat?',
  'fq.g5.a1': '/side (or /btw) opens a separate conversation beside the thread: it gets the same tool surface and passes the same permission gates, but its exchanges do not write back to the main thread. It lives on the node and survives refresh, device changes and even a panel restart.',
  'fq.g5.q2': 'Can I undo file changes from a turn?',
  'fq.g5.a2': '/rewind lists the checkpoints for that session. Selecting one restores tracked files, removes untracked files created after the snapshot and truncates the transcript to that turn. Build artifacts covered by .gitignore are left alone.',
  'fq.g5.q3': 'Are file paths in replies clickable?',
  'fq.g5.a3': 'Yes. The console renders recognisable workspace paths as links that open the read-only viewer at the right line; in the terminal use panda read path:line, or /edit path:line to hand it to your editor.',
  'fq.g5.q4': 'How long is conversation context kept?',
  'fq.g5.a4': 'The terminal and web console share one session store under &lt;data&gt;/sessions/ and resume the most recently updated thread for the current project. Past a 24k-character budget, overflow folds into a model-written running digest; only if the model is unreachable is the oldest content dropped. Long threads can fork at any turn with panda session fork.',
  'fq.g6.t': 'Devices & security',
  'fq.g6.q1': 'How do I connect several devices?',
  'fq.g6.a1': 'After panda init on each device, choose either pairing (panda pair &lt;addr&gt; shows a short code; panda pair confirm on the other device checks the same code and delivers the shared secret over an encrypted channel) or LAN discovery with fingerprint admission (panda nodes lists pending devices, panda nodes admit &lt;id&gt; confirms by fingerprint, nodes verify pins the Ed25519 fingerprint).',
  'fq.g6.q2': 'Why is a non-loopback listener refused?',
  'fq.g6.a2': 'By design: a non-loopback listener requires shared_secret (or OPENPANDA_SHARED_SECRET), otherwise the daemon refuses to start. Loopback listeners are unrestricted and the web panel generates an ephemeral token automatically.',
  'fq.g6.q3': 'Can two nodes run on one machine?',
  'fq.g6.a3': 'A node identity has a singleton lock, so a second daemon diagnoses the conflict and exits. For multiple identities use a VM node with node.kind: vm and an explicit node.identity.',
  'fq.g6.q4': 'Can it see the screen and click?',
  'fq.g6.a4': 'Yes, with boundaries. Nodes with a desktop session advertise hardware:screen_capture and hardware:gui_input (tier-2, because a click cannot be unsent); headless daemons advertise neither. Screen content moves only across mesh-tier links, screenshot tasks from non-mesh sources are refused at admission, and macOS requires Screen Recording and Accessibility permissions.',
  'fq.g7.t': 'Data & maintenance',
  'fq.g7.q1': 'Where is my data stored?',
  'fq.g7.a1': 'A per-user directory independent of the install location: ~/Library/Application Support/openpanda on macOS, ~/.local/share/openpanda on Linux and %LOCALAPPDATA%\\openpanda-data on Windows. memory/, projects/ and skills/ are user assets and are kept by default on uninstall.',
  'fq.g7.q2': 'How do I upgrade?',
  'fq.g7.a2': 'Use panda update check / apply (or panda upgrade) through the signed channel, or simply rerun the one-line installer; config, sessions and data are preserved. Downgrades are refused by comparing the new binary’s migration ceiling against the data directory schema.',
  'fq.g7.q3': 'How do I uninstall completely?',
  'fq.g7.a3': 'panda uninstall deletes an allowlisted set and backs it up automatically. --purge also removes user data, --backup-only archives without deleting, and --dry-run only prints the plan.',
  'fq.g7.q4': 'The console will not open, or I lost the token?',
  'fq.g7.a4': 'On loopback, just rerun panda web — the URL carries an ephemeral token that the frontend strips from the address bar immediately. A non-loopback listener needs OPENPANDA_PANEL_TOKEN. All /api/* requests accept only an Authorization: Bearer header; a ?token= in the URL is refused.',
  'fq.s8.t': 'Still stuck',
  'fq.s8.l1': 'Read the full FAQ in the repository (five languages) ↗',
  'fq.s8.l2': 'Installation and restricted-network troubleshooting ↗',
  'fq.s8.l3': 'See project status and known limitations →',
  'dx.s1.t': 'Core concepts',
  'dx.s1.d': 'Nine terms that recur across the docs and status.md, defined once here.',
  'dx.s1.h1': 'Meaning',
  'dx.s1.r1.c2': 'A machine running panda with two Ed25519 keys and a capability card. Every node can be a dispatch root; there is no master.',
  'dx.s1.r2.c2': 'The node’s declared hardware, installed agents and actuators. The scheduler hard-filters on it; panda card edits it with hot reload.',
  'dx.s1.r3.c2': 'A bridge script in adapters/*.py normalising one agent CLI’s argv, environment and output into the protocol the kernel drives.',
  'dx.s1.r4.c2': 'mesh, guarded or open. Clamps what delegations expose to a peer and what inbound work is admitted; a unilateral local assertion.',
  'dx.s1.r5.c2': 'Two-level command classification: reversible work runs autonomously, irreversible work is denied by default or sent to human review.',
  'dx.s1.r6.c2': 'A success definition attached by panda task add --accept; the orchestrator runs the checks itself after the agent finishes and compares against a baseline.',
  'dx.s1.r7.c2': 'An encrypted, signed message unit held across an outage and delivered when the link returns; a tampered bundle is never delivered.',
  'dx.s1.r8.c2': 'Parallel tasks in one project are separated by path scope; disjoint siblings each run in an isolated worktree.',
  'dx.s1.r9.c2': 'A multi-stage pipeline that may cross devices, with artifacts wired between stages; panda plan show &lt;id&gt; reports each stage.',
  'dx.s2.t': 'Config & files',
  'dx.s2.d': 'All state lives in the user directory and a default uninstall leaves user assets alone. Paths vary by platform; commands do not depend on the install location.',
  'dx.s2.h1': 'Contents',
  'dx.s2.r1.c2': 'Node name, model providers, listen address, network and trust tiers. Never put the shared secret here.',
  'dx.s2.r2.c2': 'Available native commands, agents and hardware actuators with their tier declarations.',
  'dx.s2.r3.c2': 'Dual-layer memory: user preferences and project facts stored apart, packed with delegated tasks.',
  'dx.s2.r4.c2': 'Reusable playbooks distilled from successful workflows; panda skill browses, imports and installs them.',
  'dx.s2.r5.c2': 'Pure-Go SQLite in WAL mode holding tasks, sessions, audit chains and the node directory; version --json reports the schema level.',
  'dx.s2.r6.c2': 'Runtime scratch space in workspace mode; git repositories get an entry in .git/info/exclude without touching .gitignore.',
  'dx.s2.note': 'Full paths by platform: ~/Library/Application Support/openpanda on macOS, ~/.local/share/openpanda on Linux, %LOCALAPPDATA%\\openpanda-data on Windows.',
  'dx.s3.t': 'Adapter inventory',
  'dx.s3.d': 'Ten bundled adapters plus one generic bridge, matching the real files under adapters/. A missing agent is never selected.',
  'dx.s3.h1': 'Drives',
  'dx.s3.r1.c2': 'Claude Code',
  'dx.s3.r2.c2': 'OpenAI Codex CLI',
  'dx.s3.r3.c2': 'OpenCode',
  'dx.s3.r4.c2': 'Grok Build',
  'dx.s3.r5.c2': 'DeepSeek Harness',
  'dx.s3.r6.c2': 'Antigravity',
  'dx.s3.r7.c2': 'Hermes',
  'dx.s3.r8.c2': 'Pi',
  'dx.s3.r9.c2': 'ZCode',
  'dx.s3.r10.c2': 'OpenClaw',
  'dx.s3.r11.c2': 'Any headless CLI speaking stdin/stdout; _harness.py is the shared protocol skeleton',
  'dx.s3.note': 'panda agents lists installed and missing agents and prints install commands for the latter.',
  'dx.s4.t': 'Pairing & networking',
  'dx.s4.d': 'Run panda init on each device, then pick either path to admit the second machine.',
  'dx.s4.s1': 'One side starts and shows a short code; the other confirms the same code. The shared secret is delivered over an encrypted channel, with no copying by hand.',
  'dx.s4.s2': 'Pending devices on the LAN are discovered automatically and admitted by fingerprint; panda nodes verify pins the Ed25519 fingerprint.',
  'dx.s4.s3': 'Pin a trust tier for the peer, clamping what delegations expose to it and what inbound work is admitted.',
  'dx.s4.note': 'A non-loopback listener requires shared_secret or OPENPANDA_SHARED_SECRET, otherwise the daemon refuses to start. Read the security page before extending the mesh to machines you do not fully control.',
  'dx.s5.t': 'Troubleshooting',
  'dx.s5.d': 'Start with panda doctor: it lists config, endpoints, models, adapters and database health line by line.',
  'dx.s5.b1': 'If the model shows a configured key but cannot connect, check the endpoint line in doctor for a rejected EffectiveBaseURL. Every branch that refuses execution has a matching explanation.',
  'dx.s5.b2': 'If the database reports a schema newer than the binary, a non-release build has raised it. Back up and roll back rather than deleting the database.',
  'dx.s5.b3': 'On Windows, a revocation-check failure (0x80092012) comes from the bundled curl using Schannel, which requires reachable CRL/OCSP. Use --ssl-no-revoke.',
  'dx.s5.b4': 'Windows system proxy settings are invisible to Go’s ProxyFromEnvironment. Set HTTPS_PROXY explicitly.',
  'dx.s5.b5': 'update check succeeding while update apply fails means api.github.com is reachable but release-assets.githubusercontent.com is not. Use a mirror or OPENPANDA_UPDATE_BASE.',
  'dlx.s1.t': 'System requirements',
  'dlx.s1.d': 'The binary is a static Go build with no runtime dependencies; the toolchain below is needed only to build from source.',
  'dlx.s1.h1': 'Requirement',
  'dlx.s1.h2': 'Notes',
  'dlx.s1.r1.c2': 'amd64 and arm64',
  'dlx.s1.r1.c3': 'Six release targets, plus Linux lite variants',
  'dlx.s1.r2.c2': '~40MB RSS for the idle daemon',
  'dlx.s1.r2.c3': 'Excludes your configured model and agent CLIs',
  'dlx.s1.r3.c2': '≥ 3.10',
  'dlx.s1.r3.c3': 'Needed by adapter bridges and the voice sidecar; lite builds can skip it',
  'dlx.s1.r4.c2': '≥ 1.26.10 / ≥ 18',
  'dlx.s1.r4.c3': 'Source builds only: the kernel and the embedded web console',
  'dlx.s1.r5.c2': 'GitHub releases access at install time',
  'dlx.s1.r5.c3': 'After install, nodes talk only over your own network',
  'dlx.s2.t': 'Installer options',
  'dlx.s2.d': 'Options accepted by the one-line shell script and the PowerShell installer. latest always means the newest stable release; pre-releases must be pinned explicitly.',
  'dlx.s2.h1': 'Effect',
  'dlx.s2.r1.c2': 'install.sh pins a release; OPENPANDA_VERSION does the same',
  'dlx.s2.r2.c2': 'The equivalent switch for install.ps1',
  'dlx.s2.r3.c2': 'Overrides the install root; defaults to $XDG_DATA_HOME or ~/.local/share/openpanda',
  'dlx.s2.r4.c2': 'Selects the Linux lite build without the embedded console or TUI, for SBCs and headless boxes',
  'dlx.s2.r5.c2': 'PowerShell installer: skip confirmation, skip the logon task, intentionally allow a downgrade',
  'dlx.s2.r6.c2': 'panda uninstall options: remove user data too, archive only, or print the plan without acting',
  'dlx.s3.t': 'Offline & manual install',
  'dlx.s3.d': 'On a restricted network, copy the archive and checksum files into the internal network and unpack by hand using the same naming rules as the installer.',
  'dlx.s3.s1': 'macOS / Linux archives; Windows uses panda-&lt;version&gt;-windows-&lt;arch&gt;.zip.',
  'dlx.s3.s2': 'Check SHA-256 first, then the detached Ed25519 signature. The signature is the trust anchor when both arrive over the same channel.',
  'dlx.s3.s3': 'Unpack, add bin/ to PATH, then check version --json for both the binary version and the database schema.',
  'dlx.s3.note': 'The full restricted-network recipe — proxies, mirrors and OPENPANDA_UPDATE_BASE — is in docs/install.md, published in five languages.',
  'dlx.s4.t': 'Verify the install',
  'dlx.s4.d': 'Three read-only commands confirming the installed binary is the one you downloaded and that config and endpoints work.',
  'dlx.s4.c1.h': 'Version and database schema',
  'dlx.s4.c2.h': 'Config, endpoint, adapter and network diagnostics',
  'dlx.s4.c3.h': 'Initialise the node and capability card',
  'dlx.s4.note': 'If doctor prints a refusal such as a missing endpoint, fix the config before continuing. Every line it prints corresponds to a real execution decision.',
  'dlx.lite1': 'ARM64 boards and cloud hosts, lite build',
  'dlx.lite2': '32-bit ARM, older Raspberry Pi boards',
  'clx.s1.t': 'Versioning & channels',
  'clx.s1.d': 'One release-grade version line, with the rules written into the CHANGELOG rather than left as an understanding.',
  'clx.s1.f1.t': 'Semantics & compatibility',
  'clx.s1.f1.d': '0.1.x patches may add capability and fix defects but must not break config, stored data or the wire protocol; breaking any of them belongs to a minor release. Unavoidable breaks get a deprecation window and a Breaking changes entry.',
  'clx.s1.f2.t': 'Stable channel',
  'clx.s1.f2.d': 'latest means the newest stable release only; pre-releases have to be pinned. Installer and self-updater both carry a downgrade guard and refuse a build older than the data directory schema.',
  'clx.s1.f3.t': 'Signed releases',
  'clx.s1.f3.d': 'Each release ships checksums.txt and a detached Ed25519 signature, checksums.txt.sig. The official public key is baked into the binary, so a forged checksum on the same channel is rejected.',
  'clx.s1.f4.t': 'Five-language records',
  'clx.s1.f4.d': 'The Chinese CHANGELOG is authoritative, with English, Japanese, Spanish and German editions. A site check reconciles the 0.1.x set on this page against CHANGELOG.md release by release.',
  'clx.s2.t': 'Upgrade notes',
  'clx.s2.d': 'Most upgrades are one command. These four are the cases worth a second look.',
  'clx.s2.b1': 'Patch upgrades within 0.1.x keep config, sessions and data. Automatic migrations add any needed index or table on first start, with no manual database work.',
  'clx.s2.b2': '0.1.1 has one explicit incompatibility: its UDP datagram plane does not interoperate with a 0.1.0 node. Upgrade the whole mesh together rather than leaving one old node behind.',
  'clx.s2.b3': '0.1.2 and 0.1.3 each bring one automatic migration (contract tables, then an additive index) and add no new wire frame. Backing up the data directory first is still good practice.',
  'clx.s2.b4': 'Old self-built binaries without the baked-in release public key fail closed on update. Point OPENPANDA_UPDATE_PUBKEY at the official key to enter the signed channel.',
  'clx.s2.b5': 'Run panda version --json before and after and compare the binary version with db_schema. If they disagree, do not run any command that writes to the database.',
  'clx.s3.t': 'Verifying a release',
  'clx.s3.d': 'Do not trust “works on my machine”. Trust a hash and a signature you can recompute.',
  'clx.s3.s1': 'Check the SHA-256 of each archive; the installer does this before unpacking.',
  'clx.s3.s2': 'Verify the detached Ed25519 signature against the public key baked into the binary; the self-updater always enforces it.',
  'clx.s3.s3': 'After installing, confirm version and schema again — run the verification against the copy that is actually installed.',
  'clx.s3.note': 'macOS ships LibreSSL 3.3 without Ed25519 support; the installer warns loudly instead of silently skipping. OPENPANDA_REQUIRE_SIG=1 turns “cannot verify” into a hard failure.',
  'stx.s1.t': 'Platform matrix',
  'stx.s1.d': 'Six release targets plus the Linux lite variants, each with its own archive and SHA-256 entry.',
  'stx.s1.h2': 'Status',
  'stx.s1.r1.c3': 'Released, Apple Silicon and Intel Macs',
  'stx.s1.r2.c3': 'Released, servers, desktops and ARM boards',
  'stx.s1.r3.c3': 'Released, PowerShell 5.1 installer',
  'stx.s1.r4.c3': 'Light build without the embedded console or TUI, for headless devices',
  'stx.s1.r5.c3': 'No CGO; make build is enough, but run make web first for the console',
  'stx.s1.note': 'Windows-only failures cannot be faithfully reproduced on macOS; treat them as a test fixture that has drifted from the platform.',
  'stx.s2.t': 'How far it is verified',
  'stx.s2.d': 'Separating what has actually been exercised from what is only implemented. Sources: the verification table in docs/status.md.',
  'stx.s2.h0': 'Check',
  'stx.s2.h1': 'Result',
  'stx.s2.r1.c1': 'make gate',
  'stx.s2.r1.c2': 'fmt-check, vet, build, adapter-test, test and race-focused all green; race-focused covers core, trust, bus and storage',
  'stx.s2.r2.c1': 'make gate-all',
  'stx.s2.r2.c2': 'gate plus web-gate and both TUI PTY contracts, all green',
  'stx.s2.r3.c1': 'Web typecheck, unit tests and vite build',
  'stx.s2.r3.c2': 'exit 0 (79 frontend tests)',
  'stx.s2.r4.c1': 'Three-device hardware pipeline',
  'stx.s2.r4.c2': 'Multi-hop delegation, hard pins, a two-hop pipeline, killing a node mid-run and reconnect verified on macOS ↔ Windows ↔ Linux SBC',
  'stx.s2.r5.c1': 'Project tree over a WAN',
  'stx.s2.r5.c2': 'Mac → relay → cloud host, 3 MiB project tree with matching hashes',
  'stx.s2.r6.c1': 'Voice pipeline on hardware',
  'stx.s2.r6.c2': 'Not verified: only the degraded path that reports a missing driver and exits',
  'stx.s3.t': 'Quality gates',
  'stx.s3.d': 'main is protected: changes go through a PR, every required check must pass, and the ruleset has no bypass actors.',
  'stx.s3.f1.t': '10 required checks',
  'stx.s3.f1.d': 'PRs run lint, test, race, web, adapter contract, TUI PTY and the cross-platform matrix in parallel; pushing main triggers a further release gate.',
  'stx.s3.f2.t': 'Race detector',
  'stx.s3.f2.d': 'The kernel is concurrent: a go test -race finding is a release blocker, not a warning. Core packages stay near or above ~60% coverage where practical.',
  'stx.s3.f3.t': 'Security scanning',
  'stx.s3.f3.d': 'security.yml runs weekly and can be triggered manually. If govulncheck stays red, check for a newer patch toolchain first.',
  'stx.s3.f4.t': 'Contract tests',
  'stx.s3.f4.d': 'Fake CLIs assert adapter argv, environment, progress and timeouts; two TUI PTY contracts pin input filtering and exit behaviour.',
  'stx.s3.note': 'Release order is a hard constraint: push main, wait for that SHA’s release gate to go green, then tag. Tagging a feature branch does not run the release pipeline.',
  'stx.contribute': 'Community & contributing guide →',
  'foot.features': 'Capabilities',
  'foot.trust': 'Security & trust',
  'foot.community': 'Community & contributing',
  'foot.conduct': 'Code of conduct',
  'toc.orchestration': 'Orchestration & supervision',
  'toc.memory': 'Memory & skills',
  'toc.mesh': 'Multi-device collaboration',
  'toc.sessions': 'Sessions & embedding',
  'toc.interfaces': 'Interfaces & runtime',
  'toc.layers': 'Kernel layers',
  'toc.replaces': 'What it replaces',
  'ft.h1': 'Capabilities',
  'ft.lede': 'The landing page explains why. This one takes the kernel apart: what it does, which command drives it, and where it sits in the architecture.',
  'ft.cta1': 'Download v0.1.5 →',
  'ft.cta2': 'Read the docs →',
  'meta.ft.title': 'Capabilities — OpenPanda',
  'meta.ft.desc': 'OpenPanda capability inventory: orchestration, memory, multi-device collaboration, sessions and interfaces, with the kernel layers and a comparison table.',
  'nav.features': 'Capabilities',
  'toc.summary': 'Read this first',
  'toc.model': 'Trust model',
  'toc.audit': 'Integrity verification',
  'toc.redlines': 'Deployment red lines',
  'toc.hooks': 'Lifecycle hooks',
  'toc.report': 'Reporting vulnerabilities',
  'toc.gaps': 'Known boundaries',
  'sc.h1': 'Security & trust',
  'sc.lede': 'Trust model, deployment red lines, integrity verification and the vulnerability reporting channel. Understand the boundaries before you connect a node.',
  'sc.cta1': 'Read the docs →',
  'sc.cta2': 'See known boundaries →',
  'meta.sc.title': 'Security & trust — OpenPanda',
  'meta.sc.desc': 'The OpenPanda trust model, per-connection AEAD, signed audit chains, release signatures, deployment red lines and vulnerability reporting.',
  'toc.start': 'Build from source',
  'toc.toolchain': 'Toolchain',
  'toc.gates': 'Engineering gates',
  'toc.conventions': 'Code conventions',
  'toc.commits': 'Commit style',
  'toc.webui': 'Web console development',
  'toc.pr': 'Pull requests',
  'toc.conduct': 'Conduct & licensing',
  'toc.contact': 'Contact & discussion',
  'cm.h1': 'Community & contributing',
  'cm.lede': 'Toolchain, engineering gates, code conventions, the PR flow and the code of conduct. Start here if you want to change code; there is an entry point if you do not.',
  'cm.cta1': 'Open an issue ↗',
  'cm.cta2': 'Download the stable build →',
  'meta.cm.title': 'Community & contributing — OpenPanda',
  'meta.cm.desc': 'Building from source, engineering gates, code conventions, commit style, pull requests and the code of conduct for OpenPanda.',
  'toc.getting-started': 'Getting started',
  'toc.models': 'Model configuration',
  'toc.agents': 'Agents & adapters',
  'toc.tasks': 'Tasks & scheduling',
  'toc.sessions-ui': 'Sessions & interaction',
  'toc.devices': 'Devices & security',
  'toc.maintenance': 'Data & maintenance',
  'toc.more': 'Still stuck',
  'fq.h1': 'Frequently asked questions',
  'fq.lede': 'Twenty-nine common questions grouped by use case, drawn from docs/faq.md, SECURITY.md and the install guide rather than restating the landing page.',
  'fq.cta1': 'Read the docs →',
  'fq.cta2': 'Join the discussion →',
  'meta.fq.title': 'FAQ — OpenPanda',
  'meta.fq.desc': 'OpenPanda FAQ: installation, model configuration, adapters, task scheduling, sessions, multi-device pairing, data storage, upgrades and uninstall.',
  'toc.concepts': 'Core concepts',
  'toc.config': 'Config & files',
  'toc.adapters': 'Adapter inventory',
  'toc.network': 'Pairing & networking',
  'toc.troubleshoot': 'Troubleshooting',
  'toc.requirements': 'System requirements',
  'toc.params': 'Installer options',
  'toc.manual': 'Offline & manual install',
  'toc.after': 'Verify the install',
  'toc.policy': 'Versioning & channels',
  'toc.upgrade': 'Upgrade notes',
  'toc.verify': 'Verifying a release',
  'toc.platforms': 'Platform matrix',
  'toc.validation': 'How far it is verified',
  'toc.quality': 'Quality gates',
  'hub.ft.t': 'Capabilities',
  'hub.ft.d': 'A itemised inventory of orchestration, memory, mesh, sessions and interfaces.',
  'hub.sc.t': 'Security & trust',
  'hub.sc.d': 'Trust model, deployment red lines, integrity verification and vulnerability reporting.',
  'hub.cm.t': 'Community',
  'hub.cm.d': 'Toolchain, engineering gates, code conventions and the pull request flow.',
  'hub.fq.t': 'FAQ',
  'hub.fq.d': 'Twenty-nine common questions and answers, grouped by use case.',
  // ── CJK-labelled table cells need dictionary entries too ──────────
  'dx.s1.r1.c1': 'node',
  'dx.s1.r2.c1': 'capability card',
  'dx.s1.r3.c1': 'adapter',
  'dx.s1.r4.c1': 'trust tier',
  'dx.s1.r6.c1': 'accept',
  'dx.s1.r8.c1': 'lane',
  'dlx.s1.r2.c1': 'Memory',
  'dlx.s1.r5.c1': 'Network',
  'stx.s1.r5.c1': 'Source build',
}

const ATTRS = [
  ['data-i18n-aria', 'aria-label'],
  ['data-i18n-placeholder', 'placeholder'],
  ['data-i18n-title', 'title'],
  ['data-i18n-content', 'content'],
]

const listeners = new Set()
// Original Chinese markup, captured the first time a node is translated. The
// document is the Chinese table; this is what switching back restores.
const snapshots = new WeakMap()
let current = readLang()

function readLang() {
  if (typeof document === 'undefined') return 'zh'
  const attr = document.documentElement.getAttribute('data-lang')
  return attr === 'en' ? 'en' : 'zh'
}

export function lang() {
  return current
}

/** The script-side strings for the active language. */
export function ui() {
  return UI[current]
}

/** One English string, for callers that need a single key. */
export function t(key) {
  return current === 'en' ? EN[key] ?? '' : ''
}

export function onLangChange(fn) {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

function setContent(el, value) {
  if (value == null) return
  // <title> takes text; everything else in the document may carry markup
  // (<br>, <b>, links) and is applied as HTML.
  if (el.tagName === 'TITLE') el.textContent = value
  else el.innerHTML = value
}

function setAttr(el, attr, value) {
  if (value == null) return
  el.setAttribute(attr, value)
}

function restore(el, attr) {
  const saved = snapshots.get(el)
  if (!saved || !(attr in saved)) return
  if (attr === 'innerHTML') el.innerHTML = saved[attr]
  else if (attr === 'textContent') el.textContent = saved[attr]
  else el.setAttribute(attr, saved[attr])
}

function snapshot(el, attr) {
  let saved = snapshots.get(el)
  if (!saved) snapshots.set(el, (saved = {}))
  if (!(attr in saved)) {
    if (attr === 'innerHTML') saved[attr] = el.innerHTML
    else if (attr === 'textContent') saved[attr] = el.textContent
    else saved[attr] = el.getAttribute(attr) ?? ''
  }
}

/** Translate the whole document into `next`, in place. Idempotent. */
export function applyLang(next) {
  if (next !== 'en' && next !== 'zh') return
  current = next

  for (const el of document.querySelectorAll('[data-i18n]')) {
    const key = el.dataset.i18n
    snapshot(el, 'innerHTML')
    if (next === 'zh') restore(el, 'innerHTML')
    else setContent(el, EN[key])
  }

  for (const [dataAttr, attr] of ATTRS) {
    for (const el of document.querySelectorAll(`[${dataAttr}]`)) {
      const key = el.getAttribute(dataAttr)
      // <title> carries a key too, but its text is not an attribute.
      const target = el.tagName === 'TITLE' ? 'textContent' : attr
      snapshot(el, target)
      if (next === 'zh') restore(el, target)
      else if (target === 'textContent') setContent(el, EN[key])
      else setAttr(el, target, EN[key])
    }
  }

  const root = document.documentElement
  root.setAttribute('data-lang', next)
  root.setAttribute('lang', next === 'zh' ? 'zh-CN' : 'en')
  // The guard from the head bootstrap: the Chinese paint was held back while
  // English was being swapped in, so release it now.
  root.classList.remove('lang-pending')

  // The strapline is deliberately written in the *other* language: English on
  // the Chinese page, Chinese on the English page. Mark it so screen readers
  // switch pronunciation instead of reading it with the document's voice
  // (WCAG 3.1.2). The markup carries lang="en" for the no-JS Chinese default.
  const strap = document.querySelector('.hero-en')
  if (strap) strap.setAttribute('lang', next === 'zh' ? 'en' : 'zh-CN')

  // Social metadata stays zh_CN in both views on purpose. Crawlers do not run
  // this module, so swapping og:locale here would make the JS-rendered value
  // disagree with the raw HTML a crawler actually reads. There is one canonical
  // document, and it is Chinese — see the head comment in index.html.

  listeners.forEach((fn) => fn(next))
}

/** Switch language, persist the choice and mirror it into the URL. */
export function setLang(next, { persist = true } = {}) {
  if (next !== 'en' && next !== 'zh') return
  if (persist) {
    try {
      localStorage.setItem(STORAGE_KEY, next)
    } catch {
      // private mode — the choice just will not survive the session
    }
  }
  const url = new URL(location.href)
  // Chinese is the document's own language, so it needs no parameter; English
  // does, because that is what makes a shared link open in English.
  if (next === 'en') url.searchParams.set('lang', 'en')
  else url.searchParams.delete('lang')
  try {
    history.replaceState(null, '', url)
  } catch {
    // file:// and sandboxed frames
  }
  applyLang(next)
}

/** Wire every [data-lang-toggle] button. */
export function initLangToggle() {
  for (const btn of document.querySelectorAll('[data-lang-toggle]')) {
    btn.addEventListener('click', () => {
      setLang(current === 'zh' ? 'en' : 'zh')
      // Keep the button usable by keyboard after the swap.
      btn.focus({ preventScroll: true })
    })
  }
}

// The head bootstrap already picked a language; render it before the rest of
// the page logic runs so nothing reads a half-translated DOM.
if (typeof document !== 'undefined') {
  applyLang(current)
  // An explicit ?lang= is a choice, not a one-off: remember it so the next page
  // (or the next visit) stays in the language the link asked for.
  const asked = new URLSearchParams(location.search).get('lang')
  if (asked === 'en' || asked === 'zh') {
    try {
      localStorage.setItem(STORAGE_KEY, asked)
    } catch {
      // private mode — nothing to remember
    }
  }
}
