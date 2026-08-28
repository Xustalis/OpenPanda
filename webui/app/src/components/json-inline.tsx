import { flattenJson, prettifyJson } from '../format/json'

/** A collapsible JSON payload.
 *
 *  Task event data and task results reach the console as JSON strings whose
 *  shape depends on the producer. Rendering them inline is what made these
 *  pages read as a wall of escaping — the interesting fields were in there,
 *  buried under quotes. This shows a readable one-line summary and keeps the
 *  indented payload one click away, so a timeline stays a timeline.
 *
 *  Input that is not JSON is shown verbatim: some rows hold plain text, and
 *  showing it beats showing nothing. */
export function JsonInline({ raw, limit = 140 }: { raw: string; limit?: number }) {
  const flat = flattenJson(raw)
  return (
    <details class="raw-toggle">
      <summary class="dim">{flat.length > limit ? flat.slice(0, limit) + '…' : flat}</summary>
      <pre class="detail-raw">{prettifyJson(raw)}</pre>
    </details>
  )
}
