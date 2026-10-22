// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ComponentChildren } from 'preact'
import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import {
  api,
  askSessionStream,
  type AgentInfo,
  type NodeAgentDetail,
  type NodeInfo,
  type SelfInfo,
  type Session,
  type Task,
} from '../api/client'
import { useAsync, useChangeSignal, useLocaleRerender, useVisibleInterval } from '../hooks'
import { locale, t } from '../i18n'
import { isNodeConnected } from '../format/node-status'
import { relativeTime } from '../format/time'
import { navigate } from '../nav'
import { ErrorState } from '../components/page'
import { Icon } from '../components/icons'
import { StateBadge } from '../components/state-badge'
import { toast, toastError } from '../components/toast'
import { DirButton } from '../components/dir-picker'
import { ModelSelect } from '../components/model-select'
import { VoiceInput } from '../components/voice-input'
import { suggestProjectName } from '../components/dir-utils'
import { expandFileRefs } from '../components/attach'
import { OrchestrateCard } from '../components/orchestrate'
import { layerAboveOwnsEscape } from '../components/card-dismiss'
import { SessionPick } from '../components/session-pick'
import { autoGrow, editorClear, editorPlain, editorTextOf, insertTextAtCaret, range_or_append, useBrParagraphSeparator } from './sessions'
import { DeviceGlyph, type Form as FormKind } from '../components/device-glyph'
import { WorkspacePanels } from './workspace-panels'

/** 工作空间 — the fleet as panels. 节点网络 answers "what can each device do"
 *  with cards and forms; this page answers the live question, "what is running
 *  where", as one panel per machine holding one tile per agent it advertises.
 *
 *  The drawing is the point: a tile wears the CLI's own mark and says only
 *  whether that agent is working right now. Names, tasks and history live in
 *  the rail, one click away. The panda (see workspace-panels.tsx) stands on
 *  the tile that is holding work and walks to wherever the reader points it. */

type Form = FormKind

interface Device {
  id: string
  name: string
  form: Form
  /** Raw GOOS ("darwin"/"windows"/"linux"/…); empty when unreported. */
  os: string
  online: boolean
  local: boolean
  chip: string
  cores: number
  ram: number
  gpu: number
  tasks: number
  maxTasks: number
  queued: number
  rtt: number
  transport: string
  trust: string
  ver: string
  lastSeen: string
  neighbors: string[]
  /** The node's own capability card: what it says it can hand work to. */
  agents: Record<string, NodeAgentDetail>
  abilities: string[]
  kind: string
  tier: number
  /** Is this node holding a work slot right now — the question the map's
   *  hover card answers before anything else. A row's `current_tasks` is the
   *  heartbeat's own count; the ledger adds the tasks this console holds a row
   *  for, which is what makes the local machine light up immediately. */
  busy: boolean
  /** Tasks this node owns, newest first, from this console's ledger. */
  work: Task[]
}






/** Advertised form factors normalize into the six glyph classes. Unknown
 *  spellings (a future class from a newer peer) fall back to `unknown`
 *  rather than being mistaken for a desktop. */
function normalizeForm(raw: string | undefined): Form {
  switch ((raw ?? '').toLowerCase()) {
    case 'laptop':
    case 'notebook':
    case 'portable':
      return 'laptop'
    case 'desktop':
    case 'tower':
    case 'imac':
    case 'workstation':
      return 'desktop'
    case 'mini':
    case 'macmini':
    case 'mac-mini':
    case 'nuc':
      return 'mini'
    case 'server':
    case 'rack':
      return 'server'
    case 'vm':
    case 'virtual':
      return 'vm'
    default:
      return 'unknown'
  }
}

/** Last-resort hint for a peer whose row predates the form-factor field: the
 *  hostname sometimes names the family. Only ever used when the node itself
 *  said nothing — a reported class always wins. */
function guessForm(name: string, os: string): Form {
  const n = name.toLowerCase()
  if (/macbook|laptop|notebook|thinkpad|book pro|book air/.test(n)) return 'laptop'
  if (/mac ?mini|\bmini\b|\bnuc\b/.test(n)) return 'mini'
  if (/server|rack|nas/.test(n)) return 'server'
  if (os === 'darwin') return 'desktop'
  return 'unknown'
}

function osLabel(os: string): string {
  switch (os) {
    case 'darwin':
      return t('workspace.os.mac')
    case 'windows':
      return t('workspace.os.windows')
    case 'linux':
      return t('workspace.os.linux')
    default:
      return t('workspace.os.unknown')
  }
}

function formLabel(form: Form): string {
  return t(`workspace.form.${form}`)
}

/** `node_kind` is a wire value ("physical"/"vm"). A panel that says
 *  "physical" under a Chinese heading is the one place the protocol leaks
 *  into the reading surface. */
function kindLabel(kind: string): string {
  switch (kind) {
    case 'physical':
      return t('workspace.detail.kindPhysical')
    case 'vm':
      return t('workspace.form.vm')
    default:
      return kind
  }
}

/** The three answers the hover card owes: is it working, what is it called,
 *  and what hardware is inside. */
function workStateLabel(d: Device): string {
  if (!d.online) return t('workspace.offline')
  return d.busy ? t('workspace.state.working') : t('workspace.state.idle')
}



function isLocal(self: SelfInfo | null, n: NodeInfo): boolean {
  if (n.is_local) return true
  if (!self) return false
  if (self.node_id && self.node_id === n.id) return true
  if (self.node && self.node.id === n.id) return true
  const name = self.node_name || self.hostname
  return name !== '' && (n.name === name || n.id === name)
}

/** States that mean the node is actually executing: `queued` is work waiting
 *  for a slot, `review` is work parked for a human. Neither is load. */
const RUNNING_STATES = new Set(['dispatched', 'waiting_context', 'running'])
const PENDING_STATES = new Set(['submitted', 'queued'])

/** The comparable half of a node reference. A task's `owner` is a runtime id —
 *  `<name>@<identity>` for a node that carries one, `<name>-<8 hex>` for an
 *  ephemeral one — while a directory row's id is whatever the row was filed
 *  under. Comparing the normalized forms is what lets a task find its node
 *  without the console knowing which spelling the ledger used. */
function nodeKey(id: string): string {
  if (!id) return ''
  let s = id.trim().toLowerCase()
  const at = s.lastIndexOf('@')
  if (at > 0) s = s.slice(0, at)
  // Ephemeral suffix: the daemon refuses node names that look like one, so
  // stripping it can never merge two real nodes.
  if (/-[0-9a-f]{8}$/.test(s)) s = s.slice(0, -9)
  return s
}

function buildDevices(nodes: NodeInfo[], self: SelfInfo | null, tasks: Task[]): Device[] {
  const devices = nodes
    .map((n) => {
      const local = isLocal(self, n)
      const os = (local && self ? self.os : n.os) || ''
      const name = n.name || n.id
      const reported = normalizeForm(n.form_factor)
      return {
        id: n.id,
        name,
        form: reported !== 'unknown' ? reported : guessForm(name, os),
        os,
        // The row's status is the directory's view; `running` adds "this
        // process is up right now" for the local row only.
        online: isNodeConnected(n.status),
        local,
        chip: (local && self ? self.chip : '') || n.chip || '',
        cores: n.capacity?.cpu_cores ?? (local && self ? self.cpu_cores : 0),
        ram: n.capacity?.ram_gb ?? (local && self ? self.ram_gb ?? 0 : 0),
        gpu: n.resource_profile?.gpu_vram_gb ?? 0,
        tasks: n.capacity?.current_tasks ?? 0,
        maxTasks: n.capacity?.max_concurrent_tasks ?? 0,
        queued: n.capacity?.queued_tasks ?? 0,
        rtt: n.rtt_ms ?? 0,
        transport: n.transport ?? '',
        trust: n.trust ?? '',
        ver: n.ver ?? '',
        lastSeen: n.last_seen,
        neighbors: n.neighbors ?? [],
        agents: n.agents ?? {},
        abilities: n.abilities ?? [],
        kind: n.node_kind ?? '',
        tier: n.scheduler_tier ?? 0,
        busy: false,
        work: [] as Task[],
      } satisfies Device
    })
    .sort((a, b) => {
      if (a.local !== b.local) return a.local ? -1 : 1
      if (a.online !== b.online) return a.online ? -1 : 1
      return a.name.localeCompare(b.name)
    })
  // The map always starts from here. A store the node has not registered in
  // yet (a fresh database, a console that came up before the first heartbeat)
  // has no rows at all, and an empty canvas is a worse answer than "this is
  // the machine you are on" — the one device the console can always name.
  if (self && !devices.some((d) => d.local)) {
    const name = self.node_name || self.hostname || t('workspace.local')
    const form = normalizeForm(self.form_factor)
    devices.unshift({
      id: self.node_id || name,
      name,
      form: form !== 'unknown' ? form : guessForm(name, self.os),
      os: self.os,
      online: true,
      local: true,
      chip: self.chip ?? '',
      cores: self.cpu_cores,
      ram: self.ram_gb ?? 0,
      gpu: 0,
      tasks: 0,
      maxTasks: 0,
      queued: 0,
      rtt: 0,
      transport: '',
      trust: '',
      ver: self.version ?? '',
      lastSeen: new Date().toISOString(),
      neighbors: [],
      agents: self.node?.agents ?? {},
      abilities: self.node?.abilities ?? [],
      kind: self.node_kind ?? '',
      tier: self.node?.scheduler_tier ?? 0,
      busy: false,
      work: [] as Task[],
    })
  }
  attachWork(devices, tasks)
  return devices
}

/** File the ledger's tasks under the device that owns them, then derive the
 *  two facts the map is asked for: is this node working, and what has it been
 *  doing. Both ends of the match are normalized because the ledger spells a
 *  node by its runtime id and the directory by its row id. */
function attachWork(devices: Device[], tasks: Task[]): void {
  const byKey = new Map<string, Device>()
  for (const d of devices) {
    for (const raw of [d.id, d.name]) {
      const k = nodeKey(raw)
      if (k && !byKey.has(k)) byKey.set(k, d)
    }
  }
  for (const task of tasks) {
    const owner = byKey.get(nodeKey(task.owner ?? ''))
    if (!owner) continue
    owner.work.push(task)
  }
  for (const d of devices) {
    d.work.sort((a, b) => b.updated_at.localeCompare(a.updated_at))
    const live = d.work.filter((task) => RUNNING_STATES.has(task.state)).length
    d.busy = d.online && (d.tasks > 0 || live > 0)
    // A row's own queue count is authoritative when it reports one; the
    // ledger's pending rows only fill the gap for this console's own store.
    if (d.queued === 0) d.queued = d.work.filter((task) => PENDING_STATES.has(task.state)).length
  }
}

















export function WorkspaceView() {
  useLocaleRerender()
  const change = useChangeSignal()
  const [tick, setTick] = useState(0)
  const { data: nodes, error } = useAsync(() => api.nodes(), [], change + tick)
  const { data: self } = useAsync(() => api.self(), [], tick)
  // The ledger answers both questions the panel asks about a node: is it
  // holding a slot right now, and what has it been working on. Same read the
  // queue board makes, refreshed by the change feed rather than the 15s tick —
  // a task that starts or finishes must move the plate, not wait for a poll.
  const { data: tasks } = useAsync(() => api.tasks(), [], change + tick)
  // This node's drivable CLIs: the fallback when a local capability card
  // declares no agents (a fresh install has a card with hardware only).
  const { data: probedAgents } = useAsync(() => api.agents(), [], tick)
  const [selected, setSelected] = useState<string | null>(null)
  const [railOpen, setRailOpen] = useState(false)
  // The bench's own state: where the next task runs, and which thread of that
  // directory it continues (null = mint a new one). It lives up here because
  // the header's orchestration card needs the resolved project — the card
  // answers "what has this directory produced", which is the only feedback
  // this page has, since it never paints a transcript.
  const [pickedDir, setPickedDir] = useState('')
  const [dirTouched, setDirTouched] = useState(false)
  const [pickedSession, setPickedSession] = useState<Session | null>(null)
  const [orchOpen, setOrchOpen] = useState(false)

  // Heartbeats land on the directory every ~15s; poll at the same cadence so
  // a device that goes quiet fades without a manual reload. SSE covers the
  // structural changes (a node joining, a task moving).
  useVisibleInterval(() => setTick((v) => v + 1), 15000)

  // The project directory table, refreshed on the change feed: a send that
  // mints a project (a directory nothing pointed at yet) has to show up here
  // before the picker can list what it now owns.
  const { data: projects } = useAsync(() => api.projects(), [], tick)

  // Seed the bench with the active project's directory, the way the thread
  // composer's footer does: the pick is a statement first and a control
  // second, and an empty footer would make the history picker useless on
  // arrival. A directory the user chose always wins over the seed.
  const seeded = useRef(false)
  useEffect(() => {
    if (seeded.current || dirTouched || !projects) return
    seeded.current = true
    const active = (projects.detail ?? []).find((d) => d.name === projects.active)
    if (active?.work_dir) setPickedDir(active.work_dir)
  }, [projects, dirTouched])

  const project = useMemo(() => projectOfDir(projects?.detail, pickedDir), [projects, pickedDir])

  // The directory's threads, newest first. Scoped by project because a
  // session's work dir is read from its project row — the picker can only
  // offer what a send can actually continue. It follows the page's own 15s
  // tick rather than the task feed: reading every session file on each task
  // event would be a lot of I/O for a list that changes when a thread does,
  // not when a task does.
  const { data: dirSessions } = useAsync(
    () => (project ? api.sessions(project) : Promise.resolve([] as Session[])),
    [project],
    tick,
  )

  const devices = useMemo(() => buildDevices(nodes ?? [], self, tasks ?? []), [nodes, self, tasks])
  const deviceOf = useMemo(() => new Map(devices.map((d) => [d.id, d])), [devices])

  const sel = selected ? deviceOf.get(selected) ?? null : null

  const toggleRail = () => {
    const next = !railOpen
    setRailOpen(next)
    if (next) {
      setOrchOpen(false)
      // The toggle is the device *list*; a device read before the panel was
      // folded is a different question. Picking a plate opens the detail
      // directly, so nothing is lost by starting here.
      setSelected(null)
    }
  }
  const toggleOrch = (open: boolean) => {
    setOrchOpen(open)
    if (open) setRailOpen(false)
  }
  // Picking a device is a request for its facts, and those only live in the
  // rail — so a pick opens the panel it writes to. Clearing a selection
  // leaves the panel's own state alone.
  const pick = (id: string | null) => {
    setSelected(id)
    if (id) toggleOrch(false)
    if (id) setRailOpen(true)
  }
  // The card's own ×: a close button on a panel puts the panel away, it does
  // not leave a list standing in place of the facts that were being read.
  const closeRail = () => {
    setSelected(null)
    setRailOpen(false)
  }
  // Back, not close: the panel stays up and returns to the fleet list, which
  // is the one place to switch devices without hunting for a plate.
  const backToList = () => setSelected(null)

  // Escape closes the card, like the two cards on a thread: the trigger and
  // the key are the only two ways out, so a click on the map never puts the
  // list away mid-read.
  useEffect(() => {
    if (!railOpen) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || layerAboveOwnsEscape()) return
      e.stopPropagation()
      setRailOpen(false)
    }
    document.addEventListener('keydown', onKey, true)
    return () => document.removeEventListener('keydown', onKey, true)
  }, [railOpen])

  if (error)
    return (
      <ErrorState
        title={t('nav.workspace')}
        sub={t('workspace.subtitle')}
        error={error}
        onRetry={() => setTick((v) => v + 1)}
      />
    )

  return (
    <section
      class={`ws-page${orchOpen || railOpen ? ' panel-open' : ''}${railOpen && sel ? ' detail-open' : ''}`}
    >
      {/* Same top edge as a thread: a mark, the name, the key for the
          drawing, and the two controls the page has. The drawing below is the
          page — no counts, no toolbar. The orchestration card is the exception:
          this surface never shows a transcript, so the tasks a directory has
          produced are the only place a dispatched turn becomes visible. It sits
          at the far right, exactly where the chat's own card hangs. */}
      <header class="ws-head">
        <Icon name="devices" size={16} class="ws-head-icon" />
        <span class="ws-title">{t('nav.workspace')}</span>
        {/* The key reads as a subtitle rather than a footer: one line of
            glyph + name for the six machine classes the plates encode. The
            two line styles are still named by the edges' own tooltips. */}
        {/* The key for the six machine classes used to live here, one glyph
            each. The panels name themselves now (glyph + name + OS), so the
            space is better spent on the two numbers the header can actually
            answer at a glance. */}
        <span class="ws-count mono">
          {t('workspace.count', {
            devices: devices.length,
            online: devices.filter((d) => d.online).length,
          })}
        </span>
        <div class="ws-head-tools">
          {/* The rail is a card, not a column: it hangs off the same corner as
              the orchestration card beside it, over the drawing. Taking a grid
              column instead made the map narrower, which re-laid the plates out
              under the reader's cursor every time the list opened — the fleet
              moved because a list appeared. */}
          <div class="ws-rail-picker">
            <button
              type="button"
              class={`icon-btn ws-rail-toggle${railOpen ? ' on' : ''}`}
              aria-label={t('workspace.rail')}
              aria-expanded={railOpen}
              title={t('workspace.rail')}
              onClick={toggleRail}
            >
              <Icon name="panel-right" size={16} />
            </button>
            {railOpen && (
              <aside
                class={`ws-rail${sel ? ' detail' : ''}`}
                role="dialog"
                aria-label={t('workspace.rail')}
              >
                {sel ? (
                  <DeviceDetail
                    device={sel}
                    devices={deviceOf}
                    probedAgents={sel.local ? probedAgents ?? [] : []}
                    onPick={pick}
                    onBack={backToList}
                    onClose={closeRail}
                  />
                ) : (
                  <div class="ws-list">
                    {devices.map((d) => (
                      <button
                        type="button"
                        key={d.id}
                        class={`ws-row${selected === d.id ? ' sel' : ''}${d.online ? '' : ' off'}`}
                        onClick={() => pick(d.id)}
                      >
                        <svg class="ws-row-glyph" viewBox="-14 -14 28 28" aria-hidden="true">
                          <DeviceGlyph form={d.form} />
                        </svg>
                        <span class="ws-row-text">
                          <span class="ws-row-name">{d.name}</span>
                          <span class="ws-row-meta dim">
                            {osLabel(d.os)} · {formLabel(d.form)}
                          </span>
                        </span>
                        <span
                          class={`ws-row-dot${d.online ? '' : ' off'}${d.busy ? ' busy' : ''}`}
                          aria-hidden="true"
                        />
                        <span class="ws-row-rtt mono dim">{d.rtt > 0 ? `${d.rtt} ms` : ''}</span>
                      </button>
                    ))}
                  </div>
                )}
              </aside>
            )}
          </div>
          <OrchestrateCard
            key={`orch-${project || 'none'}`}
            project={project || undefined}
            open={orchOpen}
            onOpenChange={toggleOrch}
            onlineNodeCount={devices.filter((d) => d.online).length}
            selfNodeId={devices.find((d) => d.local)?.id}
          />
        </div>
      </header>

      <div class="ws-body">
        <div class="ws-main">
          <div class="ws-map">
            <WorkspacePanels
              devices={devices}
              selected={selected}
              onPick={pick}
              meta={(d) => `${osLabel(d.os)} · ${formLabel(d.form as Form)}`}
              fallbackAgents={(probedAgents ?? []).filter((a) => a.installed).map((a) => a.name)}
              localLabel={t('workspace.local')}
              onlineLabel={t('workspace.online')}
              offlineLabel={t('workspace.offline')}
              emptyLabel={t('workspace.empty')}
              idleLabel={t('workspace.tile.idle')}
              runningLabel={t('workspace.tile.running')}
              noneLabel={t('workspace.detail.agentsEmpty')}
            />
          </div>
        </div>
      </div>

      {/* The chat composer, reused whole and docked exactly where 新对话 has
          it: same box, same type, same Enter rules, same footer. The footer
          now carries both halves of the bench — the directory, and the thread
          of that directory the next task is appended to — and a send stays
          here: no thread opens, no transcript is painted, the orchestration
          card is where the dispatched work becomes visible. */}
      <WorkspaceComposer
        dir={pickedDir}
        onDirChange={(path) => {
          setPickedDir(path)
          setDirTouched(true)
          // A thread belongs to the directory it was listed under; keeping it
          // picked across a directory switch would silently append the next
          // task to a conversation from somewhere else.
          setPickedSession(null)
        }}
        project={project}
        sessions={dirSessions ?? []}
        picked={pickedSession}
        onPick={setPickedSession}
        onSent={(s) => {
          // A new thread becomes the pick: the next send continues what this
          // one started, the same rule the chat composer follows.
          setPickedSession(s)
          setTick((v) => v + 1)
          // Feedback, since there is no transcript to watch: the card that
          // answers "what did I just dispatch" is the task list.
          toggleOrch(true)
        }}
      />
    </section>
  )
}

/** Resolve a picked directory to the project whose work dir it is, creating
 *  the project when no row points at it yet — the same rule the thread
 *  composer's footer follows. */
async function projectForDir(dir: string): Promise<string> {
  const pl = await api.projects()
  const norm = (p: string) => p.replace(/[\\/]+$/, '').toLowerCase()
  const want = norm(dir)
  const hit = (pl.detail ?? []).find((d) => d.work_dir && norm(d.work_dir) === want)
  if (hit) return hit.name
  const taken = new Set(pl.projects ?? [])
  const base = suggestProjectName(dir) || 'project'
  let name = base
  for (let i = 2; taken.has(name); i++) name = `${base}-${i}`
  const created = await api.createProject({ name, work_dir: dir })
  return created.name
}

/** The read-only half of the rule above: which project already owns this
 *  directory, or '' when none does. The picker needs the answer without the
 *  side effect — opening 工作空间 on a directory that was never used must not
 *  mint a project just to say "no history here yet". */
function projectOfDir(
  detail: Array<{ name: string; work_dir?: string }> | undefined,
  dir: string,
): string {
  if (!dir) return ''
  const norm = (p: string) => p.replace(/[\\/]+$/, '').toLowerCase()
  const want = norm(dir)
  return (detail ?? []).find((d) => d.work_dir && norm(d.work_dir) === want)?.name ?? ''
}

/** The chat composer, reused whole: same box, same type, same Enter rules,
 *  same footer. What a send *does* is the difference — it is a dispatch, not
 *  a turn in the page you are looking at:
 *
 *  - the target thread is the footer's pick, or a brand-new one when nothing
 *    is picked (titled after the prompt, like 新对话);
 *  - the stream is fired and never awaited. There is no transcript here, so
 *    there is nothing to paint: the turn runs on the server and the header's
 *    task card is where its progress shows up;
 *  - nothing navigates. Not on send, not after it — staying on the map is the
 *    whole point of the surface, and leaving the page must not cancel work. */
function WorkspaceComposer(props: {
  dir: string
  onDirChange(path: string): void
  /** The project the picked directory resolves to; '' when nothing owns it. */
  project: string
  sessions: Session[]
  picked: Session | null
  onPick(session: Session | null): void
  /** Called with the thread the task was sent to, after the stream is up. */
  onSent(session: Session): void
}) {
  useLocaleRerender()
  const composer = useRef<HTMLDivElement>(null)
  const [input, setInput] = useState('')
  const [sending, setSending] = useState(false)
  const [authorize, setAuthorize] = useState(false)

  useEffect(() => {
    composer.current?.focus()
    // No abort on unmount on purpose: this is a dispatch surface, and a turn
    // that survives the page being left is the feature, not a leak.
  }, [])

  /** Dictation lands at the caret here too — the box is the same composer, so
   *  a spoken sentence and a typed one have to arrive identically. */
  function insertDictation(text: string): void {
    const el = composer.current
    if (!el) return
    insertTextAtCaret(el, text)
    requestAnimationFrame(() => {
      setInput(editorTextOf(el))
      autoGrow(el)
    })
  }

  async function send(e?: Event) {
    e?.preventDefault()
    const raw = input.trim()
    if (!raw || sending) return
    setSending(true)
    try {
      // The directory resolves first: a session's work dir is read from its
      // project row, so a directory no project points at would be silently
      // ignored. An existing project is reused; a new directory gets one
      // minted so the thread — and its tasks — are filed where the picker can
      // find them next time.
      let threadProject: string | undefined = props.project || undefined
      if (!threadProject && props.dir) {
        try {
          threadProject = await projectForDir(props.dir)
        } catch (err) {
          // The send matters more than the pick — report and fall back to the
          // default project, the same trade the thread composer makes.
          toastError(err)
        }
      }
      // @file references expand to fenced blocks before the prompt leaves —
      // the same rule the thread composer applies.
      const expanded = await expandFileRefs(raw, (p) => api.fsRead(p))
      const prompt = expanded.prompt
      const target =
        props.picked ?? (await api.createSession(prompt.slice(0, 48), threadProject))
      void askSessionStream(
        target.id,
        prompt,
        authorize,
        {
          onDelta() {},
          onReasoning() {},
          onStatus() {},
          onOperation() {},
          onResult() {},
          // The one handler that has to do something: with no transcript, a
          // turn that failed (no model, dead key, rejected prompt) would
          // otherwise vanish without a trace.
          onError(msg) {
            toast(msg, 'error')
          },
        },
        undefined,
      ).catch(toastError)
      editorClear(composer.current)
      setInput('')
      props.onSent(target)
      toast(
        t('workspace.sentToast', { title: target.title || t('sessions.untitled') }),
        'success',
      )
    } catch (err) {
      toastError(err)
    } finally {
      setSending(false)
    }
  }

  return (
    <form class="composer" onSubmit={send}>
      <div class="composer-box">
        <div
          ref={(el) => {
            composer.current = el
          }}
          class={`composer-input${editorPlain(input) === '' ? ' is-empty' : ''}`}
          contenteditable="true"
          role="textbox"
          aria-multiline="true"
          aria-label={t('sessions.placeholder')}
          data-placeholder={t('sessions.placeholder')}
          onInput={(e) => {
            useBrParagraphSeparator()
            const el = e.currentTarget as HTMLDivElement
            setInput(editorTextOf(el))
            autoGrow(el)
          }}
          onKeyDown={(e) => {
            // Shift+Enter breaks the line — the browser's own newline is a
            // literal text node Chromium does not lay out here, so the <br> is
            // inserted by hand, exactly as the thread composer does.
            if (e.key === 'Enter' && e.shiftKey && !e.isComposing) {
              e.preventDefault()
              const el = e.currentTarget as HTMLDivElement
              const br = el.ownerDocument.createElement('br')
              const sel = document.getSelection()
              if (sel && sel.rangeCount > 0) {
                const rg = sel.getRangeAt(0)
                rg.deleteContents()
                rg.insertNode(br)
                rg.setStartAfter(br)
                rg.collapse(true)
                sel.removeAllRanges()
                sel.addRange(rg)
              } else {
                el.appendChild(br)
              }
              range_or_append(br, el.ownerDocument.createTextNode('\u200b'))
              setInput(editorTextOf(el))
              autoGrow(el)
              return
            }
            // Enter sends; a composing IME keeps its Enter.
            if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && !(e.metaKey || e.ctrlKey)) {
              e.preventDefault()
              void send()
            }
            if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
              e.preventDefault()
              void send()
            }
          }}
        />
        <div class="composer-actions">
          <label class="ask-authorize" title={t('approval.stateHint')}>
            <span class="switch">
              <input
                type="checkbox"
                checked={authorize}
                onChange={(e) => setAuthorize((e.target as HTMLInputElement).checked)}
              />
              <span class="switch-track" aria-hidden="true" />
            </span>
            {t('sessions.authorize')}
          </label>
          <ModelSelect disabled={sending} />
          <VoiceInput onText={insertDictation} />
          <button class="btn primary" type="submit" disabled={!input.trim() || sending}>
            {t('sessions.send')}
          </button>
        </div>
      </div>
      {/* Both halves of the bench, in the footer's own grammar: where the task
          runs, then which thread it continues. The thread pill is the reason
          this page exists — a send appends to the picked conversation without
          ever opening it. */}
      <div class="composer-foot">
        <DirButton
          value={props.dir}
          onChange={props.onDirChange}
          label={t('sessions.chooseDir')}
          disabled={sending}
        />
        <SessionPick
          sessions={props.sessions}
          value={props.picked}
          onChange={props.onPick}
          hasDir={props.dir !== ''}
          disabled={sending}
        />
        <p class="composer-shortcut-hint">{t('sessions.sendShortcut')}</p>
      </div>
    </form>
  )
}

/** One device, in full. The map answers "who is out there"; this panel answers
 *  everything else about the one plate the reader picked — its hardware, its
 *  runtime state, the agents it declares, and the work it has actually done.
 *  Sections in reading order: identity → hardware → runtime → abilities →
 *  agents → recent work → adjacency. The last two are the reason the panel
 *  exists at all: "what can this box do" is the card's job, "what has it been
 *  doing" is only visible here. */
function DeviceDetail({
  device: d,
  devices,
  probedAgents,
  onPick,
  onBack,
  onClose,
}: {
  device: Device
  devices: Map<string, Device>
  probedAgents: AgentInfo[]
  onPick(id: string): void
  onBack(): void
  onClose(): void
}) {
  const neighbors = d.neighbors.filter((id) => devices.has(id))
  const agents = Object.entries(d.agents)
  const work = d.work.slice(0, 6)
  const cliAgents = probedAgents.filter((a) => a.installed)
  return (
    <div class="ws-detail">
      <div class="ws-detail-head">
        <button
          type="button"
          class="icon-btn ws-detail-back"
          aria-label={t('workspace.detail.back')}
          title={t('workspace.detail.back')}
          onClick={onBack}
        >
          <Icon name="arrow-left" size={16} />
        </button>
        <svg class="ws-detail-glyph" viewBox="-14 -14 28 28" aria-hidden="true">
          <DeviceGlyph form={d.form} />
        </svg>
        <span class="ws-detail-name">{d.name}</span>
        <button
          type="button"
          class="icon-btn"
          aria-label={t('common.close')}
          onClick={onClose}
        >
          ×
        </button>
      </div>
      <p class="ws-detail-id mono dim">{d.id}</p>
      <div class="ws-detail-badges">
        <span class={`badge ${d.online ? (d.busy ? 'yellow' : 'green') : ''}`}>
          {workStateLabel(d)}
        </span>
        {d.local && <span class="badge green">{t('workspace.local')}</span>}
        <span class="badge">{formLabel(d.form)}</span>
        <span class="badge">{osLabel(d.os)}</span>
        {d.trust && d.trust !== 'mesh' && <span class="badge">{d.trust}</span>}
      </div>

      <DetailSection title={t('workspace.detail.hardware')}>
        <dl class="ws-facts">
          <Fact label={t('workspace.detail.chip')} value={d.chip} />
          <Fact label={t('workspace.detail.cores')} value={d.cores > 0 ? t('nodes.cores', { n: d.cores }) : ''} />
          <Fact label={t('workspace.detail.memory')} value={d.ram > 0 ? `${d.ram} GB` : ''} />
          <Fact label={t('workspace.detail.gpu')} value={d.gpu > 0 ? `${d.gpu} GB` : ''} />
          <Fact label={t('workspace.detail.form')} value={formLabel(d.form)} />
          <Fact label={t('workspace.detail.os')} value={osLabel(d.os)} />
        </dl>
      </DetailSection>

      <DetailSection title={t('workspace.detail.runtime')}>
        <dl class="ws-facts">
          <Fact
            label={t('workspace.detail.tasks')}
            value={
              d.maxTasks > 0
                ? t('nodes.concurrency', { cur: d.tasks, max: d.maxTasks })
                : d.tasks > 0
                  ? String(d.tasks)
                  : ''
            }
          />
          <Fact label={t('workspace.detail.queued')} value={d.queued > 0 ? String(d.queued) : ''} />
          <Fact label={t('workspace.detail.kind')} value={kindLabel(d.kind)} />
          <Fact label={t('workspace.detail.tier')} value={d.tier > 0 ? String(d.tier) : ''} />
          <Fact label={t('workspace.detail.version')} value={d.ver} />
          <Fact
            label={t('workspace.detail.rtt')}
            value={d.rtt > 0 ? `${d.rtt} ms${d.transport ? ` · ${d.transport}` : ''}` : ''}
          />
          <Fact
            label={t('nodes.lastSeen')}
            value={d.lastSeen === 'never' ? t('nodes.never') : new Date(d.lastSeen).toLocaleString()}
          />
        </dl>
      </DetailSection>

      {d.abilities.length > 0 && (
        <DetailSection title={t('workspace.detail.abilities')}>
          <div class="ws-detail-chips">
            {d.abilities.slice(0, 12).map((a) => (
              <span class="ability-tag" key={a}>
                {a}
              </span>
            ))}
            {d.abilities.length > 12 && (
              <span class="ability-tag more">+{d.abilities.length - 12}</span>
            )}
          </div>
        </DetailSection>
      )}

      <DetailSection title={t('workspace.detail.agents')}>
        {agents.length > 0 ? (
          <div class="ws-agent-list">
            {agents.map(([name, a]) => (
              <div class="ws-agent" key={name}>
                <div class="ws-agent-top">
                  <span class="ws-agent-name mono">{name}</span>
                  {a.cost_tier && <span class="badge">{a.cost_tier}</span>}
                  {typeof a.tier === 'number' && a.tier > 0 && (
                    <span class="ws-agent-tier mono dim">T{a.tier}</span>
                  )}
                </div>
                {a.capabilities && a.capabilities.length > 0 && (
                  <p class="ws-agent-caps dim">{a.capabilities.join(' · ')}</p>
                )}
                {a.best_at && a.best_at.length > 0 && (
                  <p class="ws-agent-caps dim">
                    {t('nodes.bestAt')}: {a.best_at.join(' · ')}
                  </p>
                )}
              </div>
            ))}
          </div>
        ) : cliAgents.length > 0 ? (
          <>
            {/* A local card that declares no agents still has the CLIs this
                machine can drive — the probe is a better answer than "none". */}
            <p class="ws-detail-note dim">{t('workspace.detail.agentsCli')}</p>
            <div class="ws-agent-list">
              {cliAgents.map((a) => (
                <div class="ws-agent" key={a.name}>
                  <div class="ws-agent-top">
                    <span class="ws-agent-name mono">{a.binary}</span>
                    {a.version && <span class="badge">{a.version}</span>}
                  </div>
                </div>
              ))}
            </div>
          </>
        ) : (
          <p class="ws-detail-note dim">{t('workspace.detail.agentsEmpty')}</p>
        )}
      </DetailSection>

      <DetailSection title={t('workspace.detail.work')}>
        {work.length === 0 ? (
          <p class="ws-detail-note dim">{t('workspace.detail.workEmpty')}</p>
        ) : (
          <div class="ws-work-list">
            {work.map((task) => (
              <button
                type="button"
                class="ws-work"
                key={task.id}
                title={task.title}
                onClick={() => navigate({ view: 'detail', id: task.id })}
              >
                <span class="ws-work-badge">
                  <StateBadge state={task.state} />
                </span>
                <span class="ws-work-text">
                  <span class="ws-work-title">{task.title || t('sessions.untitled')}</span>
                  <span class="ws-work-meta dim">
                    {task.project ? `${task.project} · ` : ''}
                    {relativeTime(task.updated_at, locale())}
                  </span>
                </span>
              </button>
            ))}
          </div>
        )}
      </DetailSection>

      <DetailSection title={t('workspace.detail.links')}>
        {neighbors.length === 0 ? (
          <p class="ws-detail-note dim">{t('workspace.detail.none')}</p>
        ) : (
          <div class="ws-detail-chips">
            {neighbors.map((id) => (
              <button type="button" class="ws-chip" key={id} onClick={() => onPick(id)}>
                {devices.get(id)?.name ?? id}
              </button>
            ))}
          </div>
        )}
      </DetailSection>
    </div>
  )
}

/** One titled block in the device panel. The hairline above every section but
 *  the first is what makes a long fact list scannable. */
function DetailSection({ title, children }: { title: string; children: ComponentChildren }) {
  return (
    <section class="ws-detail-sec">
      <h3 class="ws-detail-sec-title">{title}</h3>
      {children}
    </section>
  )
}

/** A label-over-value fact. Empty facts draw nothing — an unreported field is
 *  missing information, not a zero. */
function Fact({ label, value }: { label: string; value: string }) {
  if (!value) return null
  return (
    <div class="ws-fact">
      <dt>{label}</dt>
      <dd class="mono">{value}</dd>
    </div>
  )
}
