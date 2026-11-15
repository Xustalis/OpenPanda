// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import en from './en.ts'
import zhCN from './zh-CN.ts'
import ja from './ja.ts'
import es from './es.ts'
import de from './de.ts'
import { setLocale, t } from './index.ts'

// Dictionary parity guard: every locale must carry exactly the English key
// set — no missing keys (which render as raw key names) and no orphans
// (translations for keys that no longer exist anywhere).
const dictionaries: Record<string, Record<string, string>> = {
  'zh-CN': zhCN,
  ja,
  es,
  de,
}

describe('i18n parity', () => {
  const enKeys = new Set(Object.keys(en))

  for (const [name, dict] of Object.entries(dictionaries)) {
    it(`${name} covers every English key`, () => {
      const missing = [...enKeys].filter((k) => !(k in dict))
      assert.deepEqual(missing, [])
    })

    it(`${name} has no orphaned keys`, () => {
      const extra = Object.keys(dict).filter((k) => !enKeys.has(k))
      assert.deepEqual(extra, [])
    })
  }

  it('every message is a non-empty string', () => {
    for (const [name, dict] of Object.entries({ en, ...dictionaries })) {
      for (const [k, v] of Object.entries(dict)) {
        assert.ok(typeof v === 'string' && v.length > 0, `${name}.${k}`)
      }
    }
  })

  it('task collaboration references resolve in every locale', () => {
    // Comparing dictionaries alone misses a new page whose keys are absent
    // from every dictionary. Check the page's references as well.
    const source = ['team.tsx', 'workflow.tsx'].map(file => readFileSync(new URL(`../views/${file}`, import.meta.url), 'utf8')).join('\n')
    const keys = [...source.matchAll(/\bt\('([^']+)'\)/g)].map(m => m[1]!)
    keys.push(...['mine', 'spaces', 'review', 'executionQueue', 'executionPlans', 'localProjects'].map(k => `work.${k}`))
    keys.push(...['pending', 'running', 'paused', 'done', 'cancelled'].map(k => `todos.status.${k}`))
    keys.push(...['low', 'normal', 'high'].map(k => `todos.priority.${k}`))
    keys.push(...['claiming', 'leased', 'queued', 'running', 'recovering', 'lease_lost', 'waiting_context', 'waiting_login', 'awaiting_submit', 'awaiting_acceptance', 'needs_reconciliation', 'paused', 'succeeded', 'failed', 'cancelled', 'superseded', 'prepared', 'approved', 'dispatching', 'unknown', 'verified', 'rejected'].map(k => `work.state.${k}`))
    for (const [name, dict] of Object.entries({ en, ...dictionaries })) {
      for (const key of keys) assert.ok(dict[key], `${name} is missing ${key}`)
    }
  })

  it('interpolation keeps replacement patterns in the value literal', () => {
    // Pin the locale: the default comes from the host (a zh-CN Windows box
    // detects zh-CN), and these assertions read English copy.
    setLocale('en')
    // `String.replaceAll` reads `$&`, `$1` and friends in a string
    // replacement; a task title containing one must come through unchanged.
    assert.equal(t('queue.more', { n: '$&' }), '$& more…')
    assert.equal(t('queue.more', { n: "$1 $' $`" }), "$1 $' $` more…")
  })
})
