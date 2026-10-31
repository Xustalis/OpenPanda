// SPDX-License-Identifier: AGPL-3.0-or-later

// Site wiring: chrome (progress, nav state), the mobile index, scroll reveals,
// counters, the FAQ accordion, copy buttons, the install switcher, pointer
// tilt, and the section parallax. The 3D starfield lives in scene.js and owns
// its own lifecycle (canvas, WebGL fallback, resize).
//
// Everything here degrades to a no-op when its node is missing — one null
// lookup used to abort this whole module and leave the page blank.

import { initHero } from './scene.js'
import { initDust } from './dust.js'
import { initLangToggle, lang, onLangChange, ui } from './i18n.js'

const $ = (id) => document.getElementById(id)
const fine = matchMedia('(pointer: fine)').matches
const calm = matchMedia('(prefers-reduced-motion: reduce)').matches

/* ── language ──────────────────────────────────────────────────────────── */
// i18n.js has already translated the document by the time this module runs
// (it applies on import); here we only wire the toggle and re-render the
// strings this file owns — the palette and its toasts.

initLangToggle()

/* ── scroll progress ───────────────────────────────────────────────────── */

const progress = $('scroll-progress')?.firstElementChild
function paintProgress() {
  if (!progress) return
  const max = document.documentElement.scrollHeight - innerHeight
  const pct = max > 0 ? Math.min(1, Math.max(0, scrollY / max)) : 0
  progress.style.width = (pct * 100).toFixed(2) + '%'
}

/* ── nav ───────────────────────────────────────────────────────────────── */

const nav = $('site-nav')
function paintNav() {
  nav?.classList.toggle('solid', scrollY > 40)
}

// rAF-throttled scroll work: one frame for progress, nav and parallax.
let scrollQueued = false
function onScroll() {
  if (scrollQueued) return
  scrollQueued = true
  requestAnimationFrame(() => {
    scrollQueued = false
    paintProgress()
    paintNav()
    paintParallax()
  })
}
addEventListener('scroll', onScroll, { passive: true })
paintProgress()
paintNav()

/* ── nav: current section ──────────────────────────────────────────────── */

const navLinks = [...document.querySelectorAll('.nav-links a')].filter((a) =>
  a.getAttribute('href')?.startsWith('#')
)
const linkFor = new Map(navLinks.map((a) => [a.getAttribute('href').slice(1), a]))

if (navLinks.length) {
  const spy = new IntersectionObserver((entries) => {
    for (const entry of entries) {
      if (!entry.isIntersecting) continue
      const link = linkFor.get(entry.target.id)
      if (!link) continue
      for (const other of navLinks) other.classList.remove('on')
      link.classList.add('on')
    }
  }, { rootMargin: '-45% 0px -50% 0px', threshold: 0 })

  for (const id of linkFor.keys()) {
    const section = $(id)
    if (section) spy.observe(section)
  }
}

/* ── mobile index ──────────────────────────────────────────────────────── */

const menuBtn = $('menu-toggle')
const menu = $('mobile-menu')

function setMenu(open) {
  if (!menu || !menuBtn) return
  menu.hidden = !open
  menuBtn.setAttribute('aria-expanded', String(open))
  document.body.style.overflow = open ? 'hidden' : ''
  // The menu is paper, the nav above it is transparent: without this the nav
  // keeps its white-on-dark colours and disappears into the sheet.
  document.body.classList.toggle('menu-open', open)
}

menuBtn?.addEventListener('click', () => setMenu(menu.hidden))
menu?.querySelectorAll('a').forEach((a) => a.addEventListener('click', () => setMenu(false)))
addEventListener('keydown', (e) => { if (e.key === 'Escape') setMenu(false) })
matchMedia('(min-width: 821px)').addEventListener('change', (e) => { if (e.matches) setMenu(false) })

/* ── scroll reveals ────────────────────────────────────────────────────── */
// .js .reveal starts at opacity:0; anything that enters the viewport gets .in.

const revealIO = new IntersectionObserver((entries) => {
  for (const entry of entries) {
    if (!entry.isIntersecting) continue
    entry.target.classList.add('in')
    revealIO.unobserve(entry.target)
    // Drop the stagger delay once the fade is done, otherwise it would also
    // delay the pointer tilt on the same element.
    entry.target.addEventListener('transitionend', () => {
      entry.target.style.transitionDelay = ''
    }, { once: true })
  }
}, { threshold: 0.12, rootMargin: '0px 0px -6% 0px' })

document.querySelectorAll('.reveal').forEach((el) => revealIO.observe(el))

// Grids come in as a sequence rather than a block.
if (!calm) {
  const groups = '.wall-grid, .cap-grid, .flow-grid, .stat-grid, .track, .scales-grid, .faq-list'
  for (const group of document.querySelectorAll(groups)) {
    [...group.children].forEach((el, i) => {
      if (!el.classList.contains('reveal')) return
      el.style.transitionDelay = Math.min(i * 70, 420) + 'ms'
    })
  }
}

/* ── counters ──────────────────────────────────────────────────────────── */
// The digits in the markup are the final values; the animation only replaces
// them once the block is in view, so a crawler or a no-JS reader sees 2,760.

function runCounter(el) {
  const to = Number(el.dataset.count)
  if (!Number.isFinite(to) || to === 0) return
  const prefix = el.dataset.prefix || ''
  const suffix = el.dataset.suffix || ''
  const dur = 1400
  const start = performance.now()

  const tick = (now) => {
    const p = Math.min(1, (now - start) / dur)
    // easeOutExpo: fast out of the gate, long settle.
    const eased = p === 1 ? 1 : 1 - Math.pow(2, -10 * p)
    el.textContent = prefix + Math.round(to * eased) + suffix
    if (p < 1) requestAnimationFrame(tick)
  }
  requestAnimationFrame(tick)
}

if (!calm) {
  const counterIO = new IntersectionObserver((entries) => {
    for (const entry of entries) {
      if (!entry.isIntersecting) continue
      counterIO.unobserve(entry.target)
      runCounter(entry.target)
    }
  }, { threshold: 0.6 })
  document.querySelectorAll('[data-count]').forEach((el) => counterIO.observe(el))
}

/* ── FAQ accordion ─────────────────────────────────────────────────────── */
// <details> is the fallback: without JS the summary toggles natively. With JS
// the body animates to its measured height and only one item stays open.

const faqs = [...document.querySelectorAll('.faq')]

// transitionend is the happy path; the timer is the guard. Without it a
// reduced-motion user (0.01ms transitions, no reliable event) or an
// interrupted toggle would leave <details> stuck mid-state.
function onHeightSettled(body, done) {
  let fired = false
  const finish = () => {
    if (fired) return
    fired = true
    body.removeEventListener('transitionend', onEnd)
    done()
  }
  const onEnd = (e) => { if (e.propertyName === 'height') finish() }
  body.addEventListener('transitionend', onEnd)
  setTimeout(finish, 520)
}

function openFaq(item, instant) {
  const body = item.querySelector('.faq-body')
  if (!body) { item.open = true; return }
  item.open = true
  if (instant || calm) { body.style.height = ''; return }
  body.style.height = '0px'
  requestAnimationFrame(() => { body.style.height = body.scrollHeight + 'px' })
  onHeightSettled(body, () => { body.style.height = '' })
}

function closeFaq(item, instant) {
  const body = item.querySelector('.faq-body')
  if (!body) { item.open = false; return }
  if (instant || calm) { item.open = false; body.style.height = ''; return }
  body.style.height = body.scrollHeight + 'px'
  requestAnimationFrame(() => { body.style.height = '0px' })
  onHeightSettled(body, () => {
    item.open = false
    body.style.height = ''
  })
}

for (const item of faqs) {
  const summary = item.querySelector('summary')
  summary?.addEventListener('click', (e) => {
    e.preventDefault()
    if (item.open) {
      closeFaq(item)
      return
    }
    // Accordion: closing the others keeps the column scannable.
    for (const other of faqs) if (other !== item && other.open) closeFaq(other)
    openFaq(item)
  })
}

/* ── copy buttons ──────────────────────────────────────────────────────── */

async function writeClipboard(text) {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    // Insecure origins and older Safari have no async clipboard.
    const ta = document.createElement('textarea')
    ta.value = text
    ta.setAttribute('readonly', '')
    ta.style.cssText = 'position:fixed;top:-1000px;opacity:0'
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand('copy')
    ta.remove()
    return ok
  }
}

document.querySelectorAll('.copy-btn').forEach((btn) => {
  btn.addEventListener('click', async () => {
    const source = $(btn.dataset.copyTarget || '')
    if (!source) return
    if (!(await writeClipboard(source.textContent.trim()))) return
    btn.classList.add('copied')
    clearTimeout(btn._copiedTimer)
    btn._copiedTimer = setTimeout(() => btn.classList.remove('copied'), 1400)
  })
})

/* ── install switcher ──────────────────────────────────────────────────── */
// Each tab carries its own command, so the markup stays the single source.

const installCmd = $('install-command')
const installPrompt = installCmd?.parentElement.querySelector('.prompt')
const installTabs = [...document.querySelectorAll('.install-tab')]

for (const tab of installTabs) {
  tab.addEventListener('click', () => {
    if (tab.classList.contains('active')) return
    for (const other of installTabs) {
      const on = other === tab
      other.classList.toggle('active', on)
      other.setAttribute('aria-selected', String(on))
    }
    if (!installCmd || !tab.dataset.cmd) return
    installCmd.textContent = tab.dataset.cmd
    if (installPrompt && tab.dataset.prompt) installPrompt.textContent = tab.dataset.prompt
  })
}

/* ── pointer tilt ──────────────────────────────────────────────────────── */
// A few degrees, only for fine pointers: enough that the diagrams feel like
// objects on the page, not enough to smear the type.

if (fine && !calm) {
  const tilts = [...document.querySelectorAll('.cap, .wall, .topo, .console-frame')]
  for (const el of tilts) {
    el.classList.add('tilt')
    const max = el.classList.contains('topo') ? 3.2 : el.classList.contains('console-frame') ? 1.8 : 2.2

    el.addEventListener('pointermove', (e) => {
      const r = el.getBoundingClientRect()
      const px = (e.clientX - r.left) / r.width
      const py = (e.clientY - r.top) / r.height
      el.classList.add('is-tilting')
      el.classList.remove('is-settling')
      el.style.setProperty('--rx', ((0.5 - py) * max * 2).toFixed(2) + 'deg')
      el.style.setProperty('--ry', ((px - 0.5) * max * 2).toFixed(2) + 'deg')
      el.style.setProperty('--mx', px * 100 + '%')
      el.style.setProperty('--my', py * 100 + '%')
    })
    el.addEventListener('pointerleave', () => {
      el.classList.remove('is-tilting')
      el.classList.add('is-settling')
      el.style.setProperty('--rx', '0deg')
      el.style.setProperty('--ry', '0deg')
      clearTimeout(el._settle)
      el._settle = setTimeout(() => el.classList.remove('is-settling'), 620)
    })
  }
}

/* ── section parallax ──────────────────────────────────────────────────── */

const scales = $('scales')
const scalesEarth = scales?.querySelector('.scales-earth')

function paintParallax() {
  if (!scalesEarth || !scales || calm) return
  const r = scales.getBoundingClientRect()
  if (r.bottom < -200 || r.top > innerHeight + 200) return
  const p = (innerHeight - r.top) / (innerHeight + r.height)
  const shift = (p - 0.5) * -110
  scalesEarth.style.transform =
    `translate3d(0, ${shift.toFixed(1)}px, 0) scale(${(1 + (p - 0.5) * 0.08).toFixed(3)})`
}

/* ── github ────────────────────────────────────────────────────────────── */

fetch('https://api.github.com/repos/Xustalis/OpenPanda')
  .then((r) => (r.ok ? r.json() : null))
  .then((repo) => {
    if (repo?.stargazers_count == null) return
    for (const id of ['gh-stars', 'gh-stars-hero']) {
      const badge = $(id)
      if (!badge) continue
      badge.textContent = `★ ${repo.stargazers_count}`
      badge.hidden = false
    }
  })
  .catch(() => { /* rate-limited or offline — the link still works */ })

/* ── 3D hero ───────────────────────────────────────────────────────────── */
// The scene is a background: it reads no UI state and reports nothing back,
// so both callbacks are inert. scene.js adds body.scene-ready itself once it
// has drawn a frame.

initHero($('orbit-canvas'), () => {}, () => '')

/* ── section dust ──────────────────────────────────────────────────────── */
// 2D canvases behind the two dark sections: a hundred drifting points each,
// paused whenever the section leaves the viewport.

initDust()

/* ── product views ─────────────────────────────────────────────────────── */
// One run, two witnesses: the console answers "what is happening", the terminal
// answers "how do I drive it". Panels toggle with [hidden] so the console keeps
// its DOM — and its tilt listeners — while the terminal is on screen.

const viewTabs = [...document.querySelectorAll('.view-tab')]
const viewPanels = [...document.querySelectorAll('.product-view [data-panel]')]

function setView(name) {
  for (const tab of viewTabs) {
    const on = tab.dataset.view === name
    tab.classList.toggle('on', on)
    tab.setAttribute('aria-selected', String(on))
  }
  for (const panel of viewPanels) panel.hidden = panel.dataset.panel !== name
}

for (const tab of viewTabs) {
  tab.addEventListener('click', () => setView(tab.dataset.view))
}

/* ── toast ─────────────────────────────────────────────────────────────── */
// Only the palette needs it: a copy action otherwise gives no feedback at all.

let toastEl = null
let toastTimer = 0
function showToast(text) {
  if (!toastEl) {
    toastEl = document.createElement('div')
    toastEl.className = 'toast'
    toastEl.setAttribute('role', 'status')
    document.body.append(toastEl)
  }
  toastEl.textContent = text
  // Force a reflow so a second toast inside the same frame still animates.
  void toastEl.offsetWidth
  toastEl.classList.add('show')
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => toastEl.classList.remove('show'), 1700)
}

/* ── ⌘K palette ────────────────────────────────────────────────────────── */
// A literal table, not a scan of the DOM: it lets each section carry the words
// people actually type ("价格", "dtn", "star") instead of only its title.
//
// Labels and hints are bilingual and the filter runs over both languages at
// once, so "install" finds 安装 in Chinese and "许可" finds Licensing in
// English — the palette is one index for the whole page, not one per locale.

const REPO = 'https://github.com/Xustalis/OpenPanda'
const INSTALL_SH =
  'curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh | sh'

const COMMANDS = [
  { kind: 'sec', label: { zh: '四堵墙', en: 'Four Walls' }, hint: { zh: '01 · 为什么需要多智能体', en: '01 · why multi-agent at all' }, kv: 'why four walls 痛点 问题 动机 motivation', go: '#why' },
  { kind: 'sec', label: { zh: '星状 → 图状', en: 'Star → Graph' }, hint: { zh: '02 · 组织方式', en: '02 · how the work is organised' }, kv: 'graph star 拓扑 架构 中心 topology architecture', go: '#graph' },
  { kind: 'sec', label: { zh: '产品', en: 'Product' }, hint: { zh: '03 · 一句话进，一张网出', en: '03 · one sentence in, a network out' }, kv: 'product console 控制台 终端 cli 演示 demo', go: '#product' },
  { kind: 'sec', label: { zh: '薄宪法', en: 'Thin Constitution' }, hint: { zh: '04 · 冻结关系，不冻结认知', en: '04 · freeze relations, not cognition' }, kv: 'constitution trust 信任 身份 授权 边界 identity authorisation boundary', go: '#constitution' },
  { kind: 'sec', label: { zh: '能力', en: 'Capabilities' }, hint: { zh: '05 · 每条边都有协议', en: '05 · every edge has a protocol' }, kv: 'capabilities 功能 dtn 审批 路由 记忆 approval routing memory', go: '#capabilities' },
  { kind: 'sec', label: { zh: '实证', en: 'Proof' }, hint: { zh: '06 · 三台真机，不是演示', en: '06 · three real machines, not a demo' }, kv: 'proof 测试 数据 真机 内存 tests memory benchmarks', go: '#proof' },
  { kind: 'sec', label: { zh: '三个尺度', en: 'Three Scales' }, hint: { zh: '07 · 桌面 / 集群 / 深空', en: '07 · desk / fleet / deep space' }, kv: 'scales space 深空 卫星 机器人 satellite robot', go: '#scales' },
  { kind: 'sec', label: { zh: '路线图', en: 'Roadmap' }, hint: { zh: '08 · 飞行计划', en: '08 · flight plan' }, kv: 'roadmap 版本 计划 release version', go: '#roadmap' },
  { kind: 'sec', label: { zh: '许可', en: 'Licensing' }, hint: { zh: '09 · 开源，但不假装免费', en: '09 · open source, not pretend-free' }, kv: 'license 价格 授权 商业 agpl 收费 purchase pricing commercial', go: '#license' },
  { kind: 'sec', label: { zh: '常见问题', en: 'FAQ' }, hint: { zh: '10 · 你可能想问的', en: '10 · things you might ask' }, kv: 'faq 问答 问题 gpu 平台 platform questions', go: '#faq' },
  { kind: 'sec', label: { zh: '安装', en: 'Install' }, hint: { zh: '11 · 三条命令入轨', en: '11 · three commands to orbit' }, kv: 'install 下载 download 开始 上手 setup', go: '#install' },
  { kind: 'act', label: { zh: '复制安装命令', en: 'Copy install command' }, hint: { zh: 'curl … install.sh | sh', en: 'curl … install.sh | sh' }, kv: 'copy 复制 curl shell unix', copy: INSTALL_SH },
  { kind: 'act', label: { zh: '复制 Homebrew 命令', en: 'Copy Homebrew command' }, hint: { zh: 'brew tap … && brew install', en: 'brew tap … && brew install' }, kv: 'brew homebrew copy 复制 macos', copy: 'brew tap Xustalis/openpanda && brew install openpanda' },
  { kind: 'link', label: { zh: 'GitHub 仓库', en: 'GitHub repository' }, hint: { zh: 'github.com/Xustalis/OpenPanda', en: 'github.com/Xustalis/OpenPanda' }, kv: 'github repo star 源码 收藏 source', url: REPO },
  { kind: 'link', label: { zh: '项目现状', en: 'Project status' }, hint: { zh: 'docs/status.md · 已知边界', en: 'docs/status.md · known boundaries' }, kv: 'status 边界 现状 未完成 boundaries unfinished', url: REPO + '/blob/main/docs/status.md' },
  { kind: 'link', label: { zh: '更新日志', en: 'Changelog' }, hint: { zh: 'CHANGELOG.md', en: 'CHANGELOG.md' }, kv: 'changelog 版本 更新 改动 changes', url: REPO + '/blob/main/CHANGELOG.md' },
  { kind: 'link', label: { zh: '版本发布', en: 'Releases' }, hint: { zh: 'releases · 安装包与校验值', en: 'releases · archives and checksums' }, kv: 'releases 下载 sha256 安装包 download', url: REPO + '/releases' },
  { kind: 'link', label: { zh: '安全策略', en: 'Security policy' }, hint: { zh: 'SECURITY.md', en: 'SECURITY.md' }, kv: 'security 漏洞 安全 报告 vulnerability report', url: REPO + '/blob/main/SECURITY.md' },
  { kind: 'link', label: { zh: '商业授权 / 合作', en: 'Commercial licence / partnership' }, hint: { zh: 'fing2024@outlook.com', en: 'fing2024@outlook.com' }, kv: 'contact 邮件 email 合作 投资 授权 partnership investment licence', url: 'mailto:fing2024@outlook.com?subject=OpenPanda' },
]

const palette = $('palette')
const palInput = $('palette-input')
const palList = $('palette-list')
const palCount = $('palette-count')
let palItems = []
let palActive = 0
let palReturn = null

function filterCommands(query) {
  const needle = query.trim().toLowerCase()
  // Empty query: the eleven sections, in reading order. Everything else is one
  // keystroke away and listing it unfiltered would bury the structure.
  if (!needle) return COMMANDS.slice(0, 11)
  const hits = []
  for (const cmd of COMMANDS) {
    const hay = [cmd.label.zh, cmd.label.en, cmd.hint.zh, cmd.hint.en, cmd.kv]
      .join(' ')
      .toLowerCase()
    const at = hay.indexOf(needle)
    if (at !== -1) hits.push({ cmd, at })
  }
  // Prefix matches first, then everything else in table order.
  hits.sort((a, b) => a.at - b.at)
  return hits.slice(0, 9).map((h) => h.cmd)
}

function renderPalette(query) {
  if (!palList) return
  const copy = ui()
  const code = lang() === 'en' ? 'en' : 'zh'
  palItems = filterCommands(query)
  palActive = 0
  palList.replaceChildren()

  if (!palItems.length) {
    const empty = document.createElement('li')
    empty.className = 'palette-empty'
    empty.textContent = copy.empty
    palList.append(empty)
  } else {
    palItems.forEach((cmd, i) => {
      const row = document.createElement('li')
      row.className = 'palette-item' + (i === 0 ? ' on' : '')
      row.id = 'pal-' + i
      row.dataset.index = String(i)
      row.setAttribute('role', 'option')
      row.setAttribute('aria-selected', String(i === 0))

      const kind = document.createElement('span')
      kind.className = 'pi-kind'
      kind.textContent = copy.kinds[cmd.kind]
      const label = document.createElement('span')
      label.className = 'pi-label'
      label.textContent = cmd.label[code]
      const hint = document.createElement('span')
      hint.className = 'pi-hint'
      hint.textContent = cmd.hint[code]

      row.append(kind, label, hint)
      palList.append(row)
    })
  }

  if (palCount) palCount.textContent = palItems.length ? copy.count(palItems.length) : ''
  if (palInput) palInput.setAttribute('aria-activedescendant', palItems.length ? 'pal-0' : '')
}

function paintActive(next) {
  const rows = palList ? [...palList.querySelectorAll('.palette-item')] : []
  if (!rows.length) return
  palActive = (next + rows.length) % rows.length
  rows.forEach((row, i) => {
    const on = i === palActive
    row.classList.toggle('on', on)
    row.setAttribute('aria-selected', String(on))
  })
  rows[palActive].scrollIntoView({ block: 'nearest' })
  palInput?.setAttribute('aria-activedescendant', rows[palActive].id)
}

function openPalette() {
  if (!palette) return
  // Two overlays at once would fight over the scroll lock and the Escape key.
  setMenu(false)
  palReturn = document.activeElement
  palette.hidden = false
  document.body.classList.add('palette-open')
  if (palInput) palInput.value = ''
  renderPalette('')
  requestAnimationFrame(() => palInput?.focus())
}

function closePalette(restoreFocus = true) {
  if (!palette || palette.hidden) return
  palette.hidden = true
  document.body.classList.remove('palette-open')
  if (restoreFocus && palReturn instanceof HTMLElement) palReturn.focus()
  palReturn = null
}

async function runCommand(cmd) {
  if (!cmd) return
  closePalette(false)

  if (cmd.go) {
    const target = document.querySelector(cmd.go)
    if (target) target.scrollIntoView({ behavior: calm ? 'auto' : 'smooth', block: 'start' })
    try { history.replaceState(null, '', cmd.go) } catch { /* file:// */ }
    return
  }
  if (cmd.url) {
    window.open(cmd.url, '_blank', 'noopener')
    return
  }
  if (cmd.copy) {
    const ok = await writeClipboard(cmd.copy)
    showToast(ok ? ui().copied : ui().copyFail)
  }
}

palInput?.addEventListener('input', () => renderPalette(palInput.value))
palInput?.addEventListener('keydown', (e) => {
  if (e.key === 'ArrowDown') { e.preventDefault(); paintActive(palActive + 1) }
  else if (e.key === 'ArrowUp') { e.preventDefault(); paintActive(palActive - 1) }
  else if (e.key === 'Enter') { e.preventDefault(); runCommand(palItems[palActive]) }
})

palList?.addEventListener('click', (e) => {
  const row = e.target.closest('.palette-item')
  if (row) runCommand(palItems[Number(row.dataset.index)])
})
palList?.addEventListener('pointerover', (e) => {
  const row = e.target.closest('.palette-item')
  if (!row) return
  const i = Number(row.dataset.index)
  if (i !== palActive) paintActive(i)
})
palette?.querySelector('[data-palette-close]')?.addEventListener('click', () => closePalette())
$('nav-k')?.addEventListener('click', openPalette)

// The palette is rendered from the table above, so it does not follow the DOM
// translation — repaint it in place when the language changes underneath it.
onLangChange(() => {
  if (palette && !palette.hidden) renderPalette(palInput?.value ?? '')
})

addEventListener('keydown', (e) => {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
    e.preventDefault()
    palette?.hidden ? openPalette() : closePalette()
    return
  }
  if (e.key === 'Escape' && palette && !palette.hidden) {
    e.preventDefault()
    closePalette()
    return
  }
  // "/" is the other convention. Never steal it from a field the user is in.
  if (e.key !== '/' || !palette?.hidden) return
  const el = e.target
  const typing =
    el instanceof HTMLElement &&
    (/^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName) || el.isContentEditable)
  if (typing || e.metaKey || e.ctrlKey || e.altKey) return
  e.preventDefault()
  openPalette()
})
