// SPDX-License-Identifier: AGPL-3.0-or-later

import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { AgentInfo } from '../api/client.ts'
import { atQuery, expandFileRefs } from './attach.ts'
import {
  clampEnabledIndex,
  extractAgentMentions,
  filterAndSortAgents,
  formatAgentCompletionHint,
  isFilePathAtToken,
  mergeMentionsIntoTeam,
  planTeamMentionEdits,
  shouldOfferAgentCompletion,
  stepEnabledIndex,
  syncInputWithTeamChange,
  syncTeamFromInput,
} from './composer-mentions.ts'

const AGENTS: AgentInfo[] = [
  {
    name: 'zcode',
    display_name: 'ZCode',
    binary: 'zcode',
    installed: false,
    best_at: ['code_generation'],
  },
  {
    name: 'claude_code',
    display_name: 'Claude Code',
    binary: 'claude',
    installed: true,
    best_at: ['multi_file_edits', 'complex_reasoning'],
  },
  {
    name: 'codex',
    display_name: 'OpenAI Codex',
    binary: 'codex',
    installed: true,
    best_at: ['running_tests', 'code_review'],
  },
  {
    name: 'opencode',
    display_name: 'OpenCode',
    binary: 'opencode',
    installed: false,
    best_at: ['quick_edits'],
  },
]

test('filterAndSortAgents puts installed agents first, then prefix and team order', () => {
  const all = filterAndSortAgents(AGENTS, '', ['codex'])
  assert.deepEqual(
    all.map((a) => a.name),
    ['codex', 'claude_code', 'zcode', 'opencode'],
  )

  const co = filterAndSortAgents(AGENTS, 'co', [])
  assert.deepEqual(
    co.map((a) => a.name),
    ['codex', 'claude_code', 'zcode', 'opencode'],
  )

  const py = filterAndSortAgents(AGENTS, 'zco', [])
  assert.deepEqual(
    py.map((a) => a.name),
    ['zcode'],
  )
})

test('formatAgentCompletionHint shows display name, roles, and install status', () => {
  const labels = {
    onDuty: 'On duty',
    notInstalled: 'Not installed',
    roleLabel: (r: string) => r,
  }
  assert.equal(
    formatAgentCompletionHint(AGENTS[1]!, labels),
    'Claude Code · code · reason · On duty',
  )
  assert.equal(
    formatAgentCompletionHint(AGENTS[0]!, labels),
    'ZCode · code · Not installed',
  )
})

test('shouldOfferAgentCompletion suppresses file paths and non-prompt command args', () => {
  assert.equal(isFilePathAtToken('src/main'), true)
  assert.equal(isFilePathAtToken('./foo'), true)
  assert.equal(isFilePathAtToken('main.go'), true)
  assert.equal(isFilePathAtToken('cod'), false)

  assert.equal(shouldOfferAgentCompletion('@cod', 4, atQuery('@cod', 4)), true)
  assert.equal(shouldOfferAgentCompletion('/plan @cod', 10, atQuery('/plan @cod', 10)), true)
  assert.equal(shouldOfferAgentCompletion('/goal @cod', 10, atQuery('/goal @cod', 10)), true)
  assert.equal(shouldOfferAgentCompletion('/read @cod', 10, atQuery('/read @cod', 10)), false)
  assert.equal(shouldOfferAgentCompletion('/export @cod', 12, atQuery('/export @cod', 12)), false)
  assert.equal(shouldOfferAgentCompletion('@src/cod', 8, atQuery('@src/cod', 8)), false)
})

test('keyboard navigation skips uninstalled disabled agents', () => {
  const items = [
    { id: 'agent:codex', disabled: false },
    { id: 'agent:zcode', disabled: true },
    { id: 'file:README.md', disabled: false },
  ]
  assert.equal(stepEnabledIndex(items, 0, 1), 2)
  assert.equal(stepEnabledIndex(items, 2, -1), 0)
  assert.equal(clampEnabledIndex([{ disabled: true }, { disabled: false }], 0), 1)
})

test('keyboard pick and send flow pins execution and keeps mention and team card aligned', async () => {
  const known = AGENTS.map((a) => a.name)
  const installed = AGENTS.filter((a) => a.installed).map((a) => a.name)

  // 1. Type `@co`, filter completions (installed first), pick `codex` with keyboard.
  let draft = 'run tests @co'
  const tok = atQuery(draft, draft.length)!
  assert.equal(shouldOfferAgentCompletion(draft, draft.length, tok), true)
  const candidates = filterAndSortAgents(AGENTS, tok.token, [])
  assert.equal(candidates[0]!.name, 'codex')
  assert.equal(candidates[0]!.installed, true)

  // Accepting `@codex` replaces the token and updates the thread roster.
  const picked = candidates[0]!
  draft = draft.slice(0, tok.start) + `@${picked.name} `
  let team = syncTeamFromInput('run tests @co', draft, [], known, installed)
  assert.equal(draft, 'run tests @codex ')
  assert.deepEqual(team, ['codex'])

  // 2. Picking another agent in the team card adds its mention; unpicking removes it.
  const nextCardTeam = ['codex', 'claude_code']
  draft = syncInputWithTeamChange(draft, team, nextCardTeam, known)
  team = nextCardTeam
  assert.equal(draft, 'run tests @codex @claude_code ')
  assert.deepEqual(extractAgentMentions(draft, known), ['codex', 'claude_code'])

  // Unpick claude_code in the card → mention is removed.
  const cardAfterRemove = ['codex']
  draft = syncInputWithTeamChange(draft, team, cardAfterRemove, known)
  team = cardAfterRemove
  assert.equal(draft, 'run tests @codex ')

  // 3. Deleting @codex in the composer updates the team card back to [].
  const afterBackspace = 'run tests @code'
  team = syncTeamFromInput(draft, afterBackspace, team, known, installed)
  assert.deepEqual(team, [])

  // 4. Re-pick @codex via mention and send: agent name is not hijacked by file expansion
  // and `team` pins execution on the wire.
  const finalDraft = '@codex check the build'
  const sendTeam = mergeMentionsIntoTeam([], finalDraft, installed)
  assert.deepEqual(sendTeam, ['codex'])
  const { prompt, attached } = await expandFileRefs(
    finalDraft,
    async (p) => ({ content: `file:${p}` }),
    (token) => known.includes(token),
  )
  assert.equal(prompt, '@codex check the build')
  assert.deepEqual(attached, [])
})

test('syncTeamFromInput preserves inherited session team when draft had no mention', () => {
  const known = AGENTS.map((a) => a.name)
  const installed = AGENTS.filter((a) => a.installed).map((a) => a.name)
  const team = syncTeamFromInput('', 'check logs', ['claude_code'], known, installed)
  assert.deepEqual(team, ['claude_code'])
})

test('planTeamMentionEdits orders removals end-to-start', () => {
  const { removals, additions } = planTeamMentionEdits(
    '@claude_code and @codex',
    ['claude_code', 'codex'],
    ['opencode'],
    ['claude_code', 'codex', 'opencode'],
  )
  assert.equal(removals.length, 2)
  assert.ok(removals[0]!.start > removals[1]!.start)
  assert.deepEqual(additions, ['@opencode'])
})
