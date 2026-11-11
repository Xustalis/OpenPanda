// SPDX-License-Identifier: AGPL-3.0-or-later

import { useMemo, useState } from 'preact/hooks'
import {
  api,
  displayVersion,
  type SelfInfo,
  type StatsDay,
  type StatsInfo,
  type UpdateStatus,
} from '../api/client'
import { useAsync, useChangeSignal, useLocaleRerender } from '../hooks'
import { locale, t } from '../i18n'
import { ErrorState, LoadingState } from '../components/page'
import { HEATMAP_DAYS, dayKey, heatGrid, heatLevel, heatShade } from '../components/heatmap'
import { compactNumber, groupNumber } from '../format/number'
import { isSelfNodeUp } from '../format/node-status'
import { toast, toastError } from '../components/toast'

/** The profile page: who this node is, what it is running, and what it has
 *  been doing.
 *
 *  It exists because the sidebar's identity chip used to land in Settings —
 *  the one place that answers "what am I on" but not "who am I" or "have I
 *  been busy". The calendar below is Claude Desktop's usage calendar, rebuilt
 *  from its shipped renderer: 26 whole weeks ending on the current Saturday,
 *  today's column left incomplete rather than padded with fake days, and a
 *  four-step shade ramp keyed to the busiest day in the window.
 *
 *  Two deliberate departures from the reference, both about honesty:
 *  a day's shade counts messages AND queued tasks (this product's work is
 *  both, and a queue-only day would otherwise look idle), and the range
 *  control above the cards does not re-shade the calendar — the calendar is
 *  always the full 182 days, exactly as the reference behaves. */

type Range = 'all' | '30d' | '7d'

/** Days back for each range; null = everything. Mirrors the reference's
 *  {all: null, 30d: 30, 7d: 7}. */
const RANGE_DAYS: Record<Range, number | null> = { all: null, '30d': 30, '7d': 7 }
const RANGES: Range[] = ['all', '30d', '7d']

/** CHANGELOG is translated per locale; English lives at the bare filename. */
const CHANGELOG_FILE: Record<string, string> = {
  en: 'CHANGELOG.md',
  'zh-CN': 'CHANGELOG.zh-CN.md',
  ja: 'CHANGELOG.ja.md',
  es: 'CHANGELOG.es.md',
  de: 'CHANGELOG.de.md',
}

export function ProfileView() {
  useLocaleRerender()
  const change = useChangeSignal()
  const [tick, setTick] = useState(0)
  const { data: self, error } = useAsync(() => api.self(), [], change + tick)
  // Stats are a second, independent request: a node whose ledger cannot be
  // read still has a profile, so a failure here degrades the calendar to
  // "nothing recorded" instead of taking the page down with it.
  const { data: stats, error: statsError } = useAsync(() => api.stats(), [], change + tick)

  // The frame is the page, so it is up before the first byte of data: the
  // loading and error states ride inside the same card instead of flashing a
  // bare pane that snaps into a card when the request lands.
  if (error) {
    return (
      <section class="profile profile-pending" aria-label={t('profile.title')}>
        <ErrorState
          head={false}
          title={t('profile.title')}
          sub={t('profile.subtitle')}
          error={error}
          onRetry={() => setTick((v) => v + 1)}
        />
      </section>
    )
  }
  if (!self) {
    return (
      <section class="profile profile-pending" aria-label={t('profile.title')}>
        <LoadingState head={false} title={t('profile.title')} sub={t('profile.subtitle')} />
      </section>
    )
  }

  return (
    // No page header: the identity card is the masthead. The title survives
    // only as the section's accessible name, so the page is still announced
    // as "个人资料" to screen readers.
    <section class="profile" aria-label={t('profile.title')}>
      {/* Identity runs the full width as the page's masthead; below it the
          calendar takes the working column and the build card the rail. The
          calendar's column — not the viewport — decides how big a day square
          gets, which is what keeps 26 columns from turning into tiles. */}
      <div class="profile-body">
        <IdentityCard self={self} />
        <UsageCard stats={stats} error={statsError} />
        <VersionCard self={self} onChecked={() => setTick((v) => v + 1)} />
      </div>
    </section>
  )
}

/** Who this node is: the same name the sidebar chip shows, plus the machine
 *  facts that used to be buried two levels deep in Settings → System. */
function IdentityCard({ self }: { self: SelfInfo }) {
  const name = self.node_name || self.hostname || '—'
  const initial = name.slice(0, 1).toUpperCase()
  return (
    <section class="card profile-identity">
      <div class="profile-head">
        <span class="profile-avatar" aria-hidden="true">
          {initial}
        </span>
        <div class="profile-id">
          <span class="profile-name">{name}</span>
          <span class="profile-host mono">{self.hostname}</span>
        </div>
        <span class={`profile-state${isSelfNodeUp(self) ? ' on' : ''}`}>
          <span class="profile-state-dot" aria-hidden="true" />
          {t(isSelfNodeUp(self) ? 'profile.state.running' : 'profile.state.stopped')}
        </span>
      </div>
      <dl class="profile-facts">
        <div>
          <dt>{t('profile.fact.os')}</dt>
          <dd>
            {osLabel(self.os)} · {self.arch}
          </dd>
        </div>
        {self.chip && (
          <div>
            <dt>{t('profile.fact.cpu')}</dt>
            <dd>
              {self.chip} · {t('profile.fact.cores', { n: self.cpu_cores })}
            </dd>
          </div>
        )}
        {!!self.ram_gb && (
          <div>
            <dt>{t('profile.fact.memory')}</dt>
            <dd>{self.ram_gb} GB</dd>
          </div>
        )}
        {self.node_kind && (
          <div>
            <dt>{t('profile.fact.kind')}</dt>
            <dd>{t(`profile.kind.${self.node_kind}`, self.node_kind)}</dd>
          </div>
        )}
        {self.node_id && (
          <div>
            <dt>{t('profile.fact.nodeId')}</dt>
            <dd class="mono profile-node-id" title={self.node_id}>
              {self.node_id}
            </dd>
          </div>
        )}
      </dl>
    </section>
  )
}

/** The tall right-hand card: what this build is (semver, codename, ship
 *  date), whether a newer release is waiting, and a footer that says what
 *  the product is and where it lives.
 *
 *  The update *actions* (download/apply, with their progress) stay in
 *  Settings → System — this card only answers "what am I on" and offers a
 *  check. The footer is pinned to the bottom so the card's extra height
 *  reads as deliberate air rather than a half-filled box. */
function VersionCard({ self, onChecked }: { self: SelfInfo; onChecked(): void }) {
  const [checking, setChecking] = useState(false)
  const up: UpdateStatus | undefined = self.update

  async function check() {
    if (checking) return
    setChecking(true)
    try {
      const next = await api.checkUpdate()
      onChecked()
      if (next.available) {
        toast(
          t('profile.version.availableToast', {
            version: displayVersion(next.latest, next.latest_codename),
          }),
          'info',
        )
      } else {
        toast(t('profile.version.upToDateToast'), 'success')
      }
    } catch (e) {
      toastError(e)
    } finally {
      setChecking(false)
    }
  }

  return (
    <section class="card profile-version">
      <div class="ver-head">
        <span class="ver-num mono">{self.version ? `v${self.version}` : '—'}</span>
        {self.codename && <span class="ver-code">{self.codename}</span>}
      </div>
      {self.release_date && (
        <p class="ver-date">
          {t('profile.version.released', { date: formatReleaseDay(self.release_date) })}
        </p>
      )}

      {up && (
        <p class={`ver-state${up.available ? ' has-update' : ''}`}>
          <span class="ver-state-dot" aria-hidden="true" />
          {up.available
            ? t('profile.version.available', {
                version: displayVersion(up.latest, up.latest_codename),
              })
            : up.degraded || (up.stage === 'idle' && !!up.error)
              ? t('profile.version.unknown')
              : t('profile.version.upToDate')}
        </p>
      )}
      {up?.available && up.notes && <p class="ver-notes">{up.notes}</p>}

      <div class="ver-actions">
        {up && (
          <button class="btn" onClick={check} disabled={checking}>
            {checking ? t('profile.version.checking') : t('profile.version.check')}
          </button>
        )}
        <a
          class="btn ghost"
          href={`https://github.com/Xustalis/OpenPanda/blob/main/${CHANGELOG_FILE[locale()] ?? 'CHANGELOG.md'}`}
          target="_blank"
          rel="noreferrer"
        >
          {t('profile.version.log')}
        </a>
      </div>

      <div class="ver-about">
        <span class="ver-about-label">{t('profile.version.about')}</span>
        <p class="ver-about-text">{t('app.tagline')}</p>
        <p class="ver-about-links">
          <a
            href="https://github.com/Xustalis/OpenPanda/blob/main/LICENSE"
            target="_blank"
            rel="noreferrer"
          >
            {t('profile.version.license')}
          </a>
          <span class="ver-dot" aria-hidden="true">
            ·
          </span>
          <a href="https://github.com/Xustalis/OpenPanda" target="_blank" rel="noreferrer">
            {t('profile.version.repo')}
          </a>
        </p>
      </div>
    </section>
  )
}

/** The activity calendar and its six headline numbers — the reference
 *  layout: 3×2 stat cells over a bare 26-week grid, with the range control
 *  pinned to the card's top-right. */
function UsageCard({ stats, error }: { stats: StatsInfo | null; error: string | null }) {
  const [range, setRange] = useState<Range>('all')
  const weeks = useMemo(() => heatGrid(new Date()), [])

  const view = useMemo(() => (stats ? rangedView(stats, range) : null), [stats, range])
  const heat = useMemo(() => (stats ? heatWindow(stats, weeks) : null), [stats, weeks])

  return (
    <section class="card usage-card">
      <div class="usage-head">
        <h2 class="usage-title">{t('profile.usage.title')}</h2>
        <div class="segmented usage-range" role="group" aria-label={t('profile.usage.range')}>
          {RANGES.map((r) => (
            <button
              key={r}
              type="button"
              class={`seg${range === r ? ' on' : ''}`}
              aria-pressed={range === r}
              onClick={() => setRange(r)}
            >
              {t(`profile.usage.range.${r}`)}
            </button>
          ))}
        </div>
      </div>

      {error && <p class="usage-error">{t('profile.usage.unavailable')}</p>}

      {view && (
        <div class="usage-stats">
          <StatCell label={t('profile.usage.sessions')} value={groupNumber(view.sessions)} />
          <StatCell label={t('profile.usage.messages')} value={groupNumber(view.messages)} />
          <StatCell label={t('profile.usage.tokens')} value={compactNumber(view.tokens)} />
          <StatCell
            label={t('profile.usage.activeDays')}
            value={`${view.activeDays}/${view.rangeDays}`}
          />
          <StatCell
            label={t('profile.usage.peakHour')}
            value={view.peakHour === null ? '—' : `${view.peakHour}:00`}
          />
          <StatCell
            label={t('profile.usage.favorite')}
            value={view.favorite || '—'}
            small
          />
        </div>
      )}

      <div class="heat" role="img" aria-label={t('profile.usage.heatAria', { days: HEATMAP_DAYS })}>
        {weeks.map((week, wi) => (
          <div class="heat-col" key={wi}>
            {week.map((cell) => {
              if (cell.future) return <div class="heat-cell future" key={cell.date} />
              const day = heat?.byDay.get(cell.date)
              const count = day ? day.messages + day.tasks : 0
              const level = heatLevel(count, heat?.max ?? 0)
              const shade = heatShade(level)
              return (
                <div
                  key={cell.date}
                  class={`heat-cell${level > 0 ? ' on' : ''}`}
                  style={shade ? { backgroundColor: shade } : undefined}
                  data-tip={heatTip(cell.date, day)}
                />
              )
            })}
          </div>
        ))}
      </div>

      <p class="usage-caption">
        {t('profile.usage.caption', {
          days: HEATMAP_DAYS,
          active: stats?.active_days ?? 0,
        })}
      </p>
    </section>
  )
}

function StatCell({ label, value, small }: { label: string; value: string; small?: boolean }) {
  return (
    <div class="usage-stat">
      <span class="usage-stat-label">{label}</span>
      <span class={`usage-stat-value${small ? ' small' : ''}`} title={value}>
        {value}
      </span>
    </div>
  )
}

/** Totals for the selected range. The calendar itself is never filtered —
 *  only these six numbers move. */
function rangedView(stats: StatsInfo, range: Range) {
  const days = RANGE_DAYS[range]
  const cutoff = days === null ? '' : dayKey(shiftDays(new Date(), -days))
  const rows = cutoff ? stats.daily.filter((d) => d.date >= cutoff) : stats.daily

  let sessions = 0
  let messages = 0
  let tokens = 0
  const models = new Map<string, number>()
  for (const d of rows) {
    sessions += d.sessions
    messages += d.messages
    tokens += d.tokens
    for (const [model, n] of Object.entries(d.models ?? {})) {
      models.set(model, (models.get(model) ?? 0) + n)
    }
  }
  let favorite = ''
  let best = 0
  for (const [model, n] of models) {
    if (n > best) {
      best = n
      favorite = model
    }
  }

  return {
    sessions,
    messages,
    tokens,
    // Claude's own range chips report "active days / days in the range"; the
    // all-time chip divides by the whole span the node has existed for.
    rangeDays: days ?? Math.max(stats.total_days, stats.active_days),
    activeDays: rows.length,
    peakHour: stats.peak_hour,
    favorite: favorite || stats.favorite_model || '',
  }
}

/** The calendar's own window: a date→day map plus the busiest day in it,
 *  which is the denominator every shade is measured against. */
function heatWindow(stats: StatsInfo, weeks: { date: string }[][]) {
  const start = weeks[0]?.[0]?.date ?? ''
  const end = weeks[weeks.length - 1]?.[6]?.date ?? ''
  const byDay = new Map<string, StatsDay>()
  let max = 0
  for (const d of stats.daily) {
    if (start && d.date < start) continue
    if (end && d.date > end) continue
    byDay.set(d.date, d)
    max = Math.max(max, d.messages + d.tasks)
  }
  return { byDay, max }
}

/** Hover text for one square. Composed rather than interpolated so a day with
 *  only tasks does not claim "0 messages". */
function heatTip(date: string, day?: StatsDay): string {
  const parts: string[] = []
  if (day?.messages) parts.push(t('profile.usage.tipMessages', { n: groupNumber(day.messages) }))
  if (day?.tasks) parts.push(t('profile.usage.tipTasks', { n: groupNumber(day.tasks) }))
  return `${formatDay(date)} · ${parts.join(' · ') || t('profile.usage.tipNone')}`
}

/** Local midnight `n` days from `d` — negative n walks backwards. Built from
 *  the date parts so a month boundary (and DST) cannot shift the day. */
function shiftDays(d: Date, n: number): Date {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate() + n)
}

/** "2026-10-17" → "10月17日" / "Oct 17" in the console's locale. Parsed as a
 *  local calendar day: `new Date('2026-10-17')` would read it as UTC midnight
 *  and show the previous day west of Greenwich. */
function formatDay(iso: string): string {
  const d = new Date(`${iso}T00:00:00`)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleDateString(locale(), { month: 'short', day: 'numeric' })
}

/** The release date carries its year — "发布于 10月15日" leaves a reader
 *  guessing which 10月15日 a build shipped on. */
function formatReleaseDay(iso: string): string {
  const d = new Date(`${iso}T00:00:00`)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleDateString(locale(), { year: 'numeric', month: 'short', day: 'numeric' })
}

/** Go's GOOS names, spelled the way their vendors spell them. */
function osLabel(os: string): string {
  switch (os) {
    case 'windows':
      return 'Windows'
    case 'darwin':
      return 'macOS'
    case 'linux':
      return 'Linux'
    default:
      return os || '—'
  }
}
