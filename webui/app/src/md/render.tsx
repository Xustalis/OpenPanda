import { useState } from 'preact/hooks'
import type { JSX } from 'preact'
import { parseBlocks, type Align, type Block, type Inline, type ListItem } from './parse'
import { t } from '../i18n'

// Renderer for the block tree from ./parse. Everything below builds vnodes —
// there is no `innerHTML` anywhere, so model output can never inject markup
// and the console needs no sanitizer.

/** Render a Markdown string as vnodes. Used for every assistant reply. */
export function Markdown({ text, class: cls }: { text: string; class?: string }): JSX.Element {
  return <div class={cls ? `md ${cls}` : 'md'}>{renderBlocks(parseBlocks(text))}</div>
}

export function renderBlocks(blocks: Block[]): JSX.Element[] {
  return blocks.map((b, i) => renderBlock(b, i))
}

function renderBlock(b: Block, key: number): JSX.Element {
  switch (b.t) {
    case 'heading':
      return <Heading key={key} level={b.level} kids={b.kids} />
    case 'para':
      return <p key={key}>{renderInlines(b.kids)}</p>
    case 'code':
      return <CodeBlock key={key} lang={b.lang} text={b.text} />
    case 'list':
      return <List key={key} ordered={b.ordered} start={b.start} items={b.items} />
    case 'table':
      return <Table key={key} head={b.head} rows={b.rows} align={b.align} />
    case 'quote':
      return <blockquote key={key}>{renderBlocks(b.kids)}</blockquote>
    case 'rule':
      return <hr key={key} />
  }
}

/** Headings render one level down (h1 in a reply would outrank the page
 *  title), clamped so deep nesting still produces a valid tag. */
function Heading({ level, kids }: { level: number; kids: Inline[] }): JSX.Element {
  const kid = renderInlines(kids)
  switch (Math.min(level + 1, 6)) {
    case 2:
      return <h2>{kid}</h2>
    case 3:
      return <h3>{kid}</h3>
    case 4:
      return <h4>{kid}</h4>
    case 5:
      return <h5>{kid}</h5>
    default:
      return <h6>{kid}</h6>
  }
}

function List({
  ordered,
  start,
  items,
}: {
  ordered: boolean
  start: number
  items: ListItem[]
}): JSX.Element {
  const kids = items.map((item, i) => (
    <li key={i}>
      {renderInlines(item.kids)}
      {item.children.length > 0 && renderBlocks(item.children)}
    </li>
  ))
  if (ordered) {
    return <ol start={start === 1 ? undefined : start}>{kids}</ol>
  }
  return <ul>{kids}</ul>
}

function Table({
  head,
  rows,
  align,
}: {
  head: Inline[][]
  rows: Inline[][][]
  align: Align[]
}): JSX.Element {
  const cellAlign = (i: number) => align[i] ?? 'left'
  return (
    // Wrapped so a wide table scrolls inside the bubble instead of stretching
    // the whole transcript.
    <div class="md-table-wrap">
      <table class="md-table">
        <thead>
          <tr>
            {head.map((cell, i) => (
              <th key={i} style={{ textAlign: cellAlign(i) }}>
                {renderInlines(cell)}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, r) => (
            <tr key={r}>
              {row.map((cell, c) => (
                <td key={c} style={{ textAlign: cellAlign(c) }}>
                  {renderInlines(cell)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/** A fenced block with its language label and a copy button — the two things
 *  people actually want from code in a chat log. */
function CodeBlock({ lang, text }: { lang: string; text: string }): JSX.Element {
  const [copied, setCopied] = useState(false)
  async function copy() {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      setTimeout(() => setCopied(false), 1600)
    } catch {
      // Clipboard denied (insecure origin, permission): the text is already
      // selectable, so a failed copy needs no error of its own.
    }
  }
  return (
    <figure class="md-code">
      <figcaption class="md-code-head">
        <span class="md-code-lang">{lang || 'text'}</span>
        <button
          type="button"
          class="md-copy"
          onClick={copy}
          aria-label={t('md.copy')}
          title={t('md.copy')}
        >
          {copied ? `✓ ${t('md.copied')}` : t('md.copy')}
        </button>
      </figcaption>
      <pre>
        <code data-lang={lang || undefined}>{text}</code>
      </pre>
    </figure>
  )
}

export function renderInlines(kids: Inline[]): JSX.Element[] {
  return kids.map((k, i) => renderInline(k, i))
}

function renderInline(k: Inline, key: number): JSX.Element {
  switch (k.t) {
    case 'text':
      // Preact escapes text children, and the CSS keeps newlines, so a soft
      // line break inside a paragraph survives without a <br/> per line.
      return <span key={key}>{k.s}</span>
    case 'code':
      return <code key={key}>{k.s}</code>
    case 'bold':
      return <strong key={key}>{renderInlines(k.kids)}</strong>
    case 'italic':
      return <em key={key}>{renderInlines(k.kids)}</em>
    case 'strike':
      return <s key={key}>{renderInlines(k.kids)}</s>
    case 'link':
      return (
        // External links open in a new tab; noreferrer keeps the panel's URL
        // (which carries a token in some deployments) out of the referer.
        <a
          key={key}
          href={k.href}
          target={k.href.startsWith('#') ? undefined : '_blank'}
          rel="noopener noreferrer"
        >
          {renderInlines(k.kids)}
        </a>
      )
  }
}
