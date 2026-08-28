/** JSON formatting for display.
 *
 *  Kept apart from the component that renders it so the behaviour is testable
 *  without a DOM: the test runner strips types but does not compile JSX. */

/** Indents a JSON string so it can be read, or returns it unchanged when it is
 *  not JSON — several older task rows hold plain text, and a result shown
 *  verbatim beats a result shown as an error. */
export function prettifyJson(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}

/** Flattens a payload to a single line for a collapsed summary, collapsing
 *  runs of whitespace the pretty-printer introduced. */
export function flattenJson(raw: string): string {
  return prettifyJson(raw).replace(/\s+/g, ' ').trim()
}
