// SPDX-License-Identifier: AGPL-3.0-or-later

import { useLocaleRerender } from '../hooks'
import { t } from '../i18n'
import type { ControlTab } from '../nav'
import { Icon, type IconName } from '../components/icons'
import { QueueView } from './queue'
import { ProjectsView } from './projects'

// Task Centre has two destinations, not three: plans are another lens on the
// same tasks, so their old top-level tab becomes a view switch inside the task
// surface. Projects stay separate because they own directories, memory and
// approval policy rather than task state. Legacy #/plans links still work.
//
// The strip is one dimension, not two. It used to be 任务/项目 in the page head
// with a second 看板/计划 switch inside the board, which asked the reader to
// decode a hierarchy that does not exist: all three are lenses on the same
// node's work. One strip, three lenses, no nesting.
const TABS: ControlTab[] = ['queue', 'plans', 'projects']
const TAB_ICON: Record<ControlTab, IconName> = {
  queue: 'kanban',
  plans: 'route',
  projects: 'folder',
}
// The board is named 看板 here, not 任务: "任务" beside "任务中心" would name
// the page twice.
const TAB_LABEL: Record<ControlTab, string> = {
  queue: 'queue.board',
  plans: 'nav.plans',
  projects: 'nav.projects',
}

export function ControlView({
  tab,
  project,
  focusReview,
  onSelectTab,
  onOpen,
  onOpenSession,
  onNewChat,
  onOpenProject,
  onOpenQueue,
}: {
  tab: ControlTab
  project?: string | null
  focusReview?: boolean
  onSelectTab(tab: ControlTab): void
  onOpen(id: string): void
  onOpenSession(id: string): void
  onNewChat(project: string | null): void
  onOpenProject(name: string): void
  onOpenQueue(project: string): void
}) {
  useLocaleRerender()

  return (
    <section class="control-section">
      {/* One bar, same top edge as 工作空间 and a thread: the mark, the page's
          name, then the three lenses at the right. The old head was a 26px
          display title over a one-line description with a rule under it — a
          headline for a page whose whole job is a board. The name is a label,
          and the description repeated what the tabs and the columns say. */}
      <header class="control-head">
        <Icon name="list-checks" size={16} class="control-head-icon" />
        <span class="control-title">{t('nav.control')}</span>
        <div class="segmented control-tabs" role="tablist" aria-label={t('nav.control')}>
          {TABS.map((k) => {
            const selected = k === tab
            return (
              <button
                key={k}
                type="button"
                role="tab"
                aria-selected={selected}
                class={`seg${selected ? ' on' : ''}`}
                onClick={() => onSelectTab(k)}
              >
                <Icon name={TAB_ICON[k]} size={14} />
                {t(TAB_LABEL[k])}
              </button>
            )
          })}
        </div>
      </header>

      {tab === 'projects' ? (
        <ProjectsView head={false} onOpenProject={onOpenProject} onOpenQueue={onOpenQueue} />
      ) : (
        <QueueView
          mode={tab === 'plans' ? 'plans' : 'board'}
          onModeChange={(mode) => onSelectTab(mode === 'plans' ? 'plans' : 'queue')}
          project={project}
          focusReview={focusReview}
          onOpen={onOpen}
          onOpenSession={onOpenSession}
          onNewChat={onNewChat}
        />
      )}
    </section>
  )
}
