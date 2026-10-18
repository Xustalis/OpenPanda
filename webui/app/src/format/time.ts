// SPDX-License-Identifier: AGPL-3.0-or-later

/** Relative timestamps for list rows ("3 分钟前", "昨天").
 *
 *  Intl rather than an i18n key set: a relative-time ladder is a locale
 *  *format*, not a string — CLDR already carries the plural rules, and
 *  `numeric: 'auto'` yields the idiomatic word ("昨天" / "yesterday") where a
 *  hand-rolled key set would say "1 天前". Falls back to a short date when the
 *  stamp is unparseable, and to an empty string when the runtime has no
 *  RelativeTimeFormat (the caller renders nothing rather than a raw ISO). */

const MINUTE = 60_000
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

type Unit = 'second' | 'minute' | 'hour' | 'day'

export function relativeTime(iso: string, locale: string, now: number = Date.now()): string {
  const at = Date.parse(iso)
  if (!Number.isFinite(at)) return ''
  const age = now - at
  // A future stamp (clock skew between nodes) reads as "just now" rather than
  // "-1 分钟前".
  if (age < MINUTE) return spell(0, 'second', locale)
  if (age < HOUR) return spell(-Math.floor(age / MINUTE), 'minute', locale)
  if (age < DAY) return spell(-Math.floor(age / HOUR), 'hour', locale)
  // Past a week the ladder stops being useful — an absolute date is what a
  // reader wants for anything older.
  if (age < 7 * DAY) return spell(-Math.floor(age / DAY), 'day', locale)
  try {
    return new Date(at).toLocaleDateString(locale)
  } catch {
    return ''
  }
}

function spell(value: number, unit: Unit, locale: string): string {
  try {
    return new Intl.RelativeTimeFormat(locale, { numeric: 'auto' }).format(value, unit)
  } catch {
    return ''
  }
}
