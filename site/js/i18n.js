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
    'A local-first graph orchestration kernel: Claude Code, Codex and every device you own, wired into one network that schedules, supervises and accepts its own work. Zero cloud relay.',
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
  'menu.label': 'INDEX',

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
  'hero.credit': 'ESO/S. BRUNIER · MILKY WAY · NASA BLUE MARBLE / DNB · PUBLIC DOMAIN',
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
  's3.term.cmd': '"fix the failing scheduler tests, run them on the build box, send me the result"',
  's3.term.classify': 'classify intent → <b>task.graph</b>',
  's3.term.replan': 'artifact missing → re-dispatched with cause',
  's3.caption':
    'Two views of the same run: the console shows state and approvals, the terminal shows routing and acceptance. Try switching.',
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
  's6.stat5': 'cloud relay',
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
    'The current version is v0.1.1 Constellation; the last stable, v0.1.0, has been succeeded by it.',
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
    'The one-liner resolves the latest stable (v0.1.1); to install a specific version, add --version.',

  /* ── 09 · licensing ────────────────────────────────────────────────── */
  'lic.title': 'Open source,<br>with the price in plain sight.',
  'lic.lede':
    'The code is AGPL-3.0-or-later. Personal use, study and internal company tools cost nothing. Only when you lock it inside a closed product does licensing come up — and that is written in the licence, not in a sales pitch.',
  'lic.c1.flag': 'Default path',
  'lic.c1.desc': 'Personal use, study, internal tools, further development.',
  'lic.c1.l1': 'All source and prebuilt artifacts',
  'lic.c1.l2': '10 agent adapters, growing',
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
    'No GPU. Routing, supervision and acceptance all use the model provider you already configured — local model or API, either works. The daemon sits at about 20MB resident and is nearly idle on CPU.',
  'faq.q3.q': 'Does my data pass through your servers?',
  'faq.q3.a':
    'No, and there are no “our servers”. Nodes connect peer to peer, session content is AEAD-encrypted and clamped hop by hop against the three trust tiers. When the link drops, DTN store-and-forward carries it until the link returns.',
  'faq.q4.q': 'Which platforms and agents are supported?',
  'faq.q4.a':
    'Six release targets: macOS, Linux and Windows × amd64 / arm64, plus a lite-linux variant. Ten adapters today — Claude Code, Codex, Antigravity, OpenCode, Grok Build, Hermes, OpenClaw, Pi, DeepSeek Harness, and a generic one that takes any headless CLI.',
  'faq.q5.q': 'Can I use it in production?',
  'faq.q5.a':
    'v0.1.1 “Constellation” is the current cut of the release-grade line (that line went release-grade at v0.1.0) and holds up day to day. A single machine, a three-machine mesh, and a real cross-network multi-hop (Mac → relay → cloud host, 3 MiB project tree hash identical) all run; self-use and internal tooling are fine, but exposing it to the public is not advised yet. What is unfinished is listed item by item in <a href="https://github.com/Xustalis/OpenPanda/blob/main/docs/status.md" target="_blank" rel="noopener">status.md</a>: UPnP/NAT-PMP port mapping, source-signature convergence for punch and directory frames, and cross-node hello replay (protocol-level, still open).',
  'faq.q6.q': 'Is it free? How does licensing work?',
  'faq.q6.a':
    'Personal, study and internal company use cost nothing. Only when you distribute or host a modified version does <a href="https://github.com/Xustalis/OpenPanda/blob/main/LICENSE" target="_blank" rel="noopener">AGPL</a> require you to open your changes; if you would rather not, that is when a commercial licence comes in — just <a href="mailto:fing2024@outlook.com">email</a>.',
  'faq.q7.q': 'How do I get involved?',
  'faq.q7.a':
    'Issues and PRs are both welcome. main carries 10 required checks and all of them run before a merge. You can also star the repo, or pick one of the known boundaries in status.md and reproduce it.',
  'faq.q8.q': 'Will there be a desktop app?',
  'faq.q8.a':
    'v0.1.1 keeps the kernel release-grade: the CLI plus the local console are already usable daily, and config, data and protocol stay forward-compatible. The desktop shell sits on the v0.1.x line — tray residency, global hotkeys, native notifications, self-update, plus the dense mesh layer.',

  /* ── 11 · install ──────────────────────────────────────────────────── */
  's9.label': 'LIFTOFF',
  's9.title': 'One command,<br>ignition.',
  's9.note':
    'The installer picks the artifact for your platform and architecture, verifies <span class="nowrap">SHA-256</span>, unpacks it and writes it into PATH. Then run <b>panda init</b> once. It installs the latest stable by default; add <b>--version</b> to pin a specific one.',
  's9.tab.unix': 'macOS / Linux',
  's9.copy': 'Copy',
  's9.s1': 'Initialise the node, model provider and capability card.',
  's9.s2': 'Enter the interactive TUI console.',
  's9.s3': 'One-shot: answer, tool call, or dispatch.',
  's9.license': 'AGPL-3.0-or-later · commercial licence · terms of use',
  's9.star': 'View / star on GitHub ↗',
  's9.all': 'All archives and checksums ↗',

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
  'foot.releases': 'Releases',
  'foot.changelog': 'Changelog',
  'foot.contact': 'Community',
  'foot.security': 'Security policy',
  'foot.invest': 'Partnership / investment',
  'foot.green': 'main must be all green to merge',
  'foot.checks': '10 required checks · 6 release targets · weekly security scan',
  'foot.latest': 'Latest v0.1.1 Constellation <span aria-hidden="true">↗</span>',
  'foot.credit':
    'Imagery: ESO/S. Brunier Milky Way panorama (CC BY 4.0); NASA Blue Marble and Black Marble DNB city lights (public domain). Code released under AGPL-3.0-or-later; closed-source embedding and hosted modifications are licensable separately.',
  'foot.built': 'Built for machines you own.',
  'foot.top': 'Back to top',

  /* ── ⌘K palette ────────────────────────────────────────────────────── */
  'pal.aria': 'Quick jump',
  'pal.results': 'Results',
  'pal.placeholder': 'Jump to a section, open a link, copy a command…',
  'pal.select': 'select',
  'pal.open': 'open',
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

  const og = document.querySelector('meta[property="og:locale"]')
  if (og) og.setAttribute('content', next === 'zh' ? 'zh_CN' : 'en_US')
  const ogAlt = document.querySelector('meta[property="og:locale:alternate"]')
  if (ogAlt) ogAlt.setAttribute('content', next === 'zh' ? 'en_US' : 'zh_CN')

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
