// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it } from 'node:test'
import assert from 'node:assert/strict'

import en from './en.ts'
import zhCN from './zh-CN.ts'
import ja from './ja.ts'
import es from './es.ts'
import de from './de.ts'

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
})
