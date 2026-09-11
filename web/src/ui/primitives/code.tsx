import { Fragment, useMemo, useState, type ReactNode } from 'react'
import { TonalButton, copyText, ErrorModal, saveAs } from './index'

/**
 * A bounded, scrollable, highlighted view of a config file.
 *
 * The catalog Codex needs is a thousand lines. Printed in full it turned the
 * Setup screen into a mile of JSON with the next card somewhere below the
 * horizon, so the viewer has a fixed height and scrolls — the file is there to
 * take away, not to read top to bottom.
 *
 * The highlighter is fifty lines of regex rather than a library, for the same
 * reason the charts are hand-drawn: the binary ships this UI inside it, and
 * highlight.js with three languages registered would be a large fraction of
 * what the whole bundle costs today. It only ever renders files from
 * configs/clients, which are ours, so it does not have to survive hostile or
 * malformed input — only to be right about JSON, TOML and shell.
 */

export type Lang = 'json' | 'jsonc' | 'toml' | 'sh'

/** One highlighting rule: what to match, and what it is. */
type Rule = { re: string; cls: string }

const STRING = String.raw`"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'`
const NUMBER = String.raw`\b-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?\b`

const RULES: Record<Lang, Rule[]> = {
  // Order matters: the first rule that matches at a position wins, so
  // comments and strings come before anything that could match inside one.
  json: [
    { re: String.raw`"(?:\\.|[^"\\])*"(?=\s*:)`, cls: 'key' },
    { re: STRING, cls: 'string' },
    { re: String.raw`\b(?:true|false|null)\b`, cls: 'number' },
    { re: NUMBER, cls: 'number' },
    { re: String.raw`[{}\[\],:]`, cls: 'punct' },
  ],
  jsonc: [
    { re: String.raw`//.*`, cls: 'comment' },
    { re: String.raw`"(?:\\.|[^"\\])*"(?=\s*:)`, cls: 'key' },
    { re: STRING, cls: 'string' },
    { re: String.raw`\b(?:true|false|null)\b`, cls: 'number' },
    { re: NUMBER, cls: 'number' },
    { re: String.raw`[{}\[\],:]`, cls: 'punct' },
  ],
  toml: [
    { re: String.raw`#.*`, cls: 'comment' },
    // A table header, which in TOML is the whole line.
    { re: String.raw`^\s*\[[^\]]*\]`, cls: 'key' },
    { re: String.raw`^\s*[A-Za-z0-9_.-]+(?=\s*=)`, cls: 'key' },
    { re: STRING, cls: 'string' },
    { re: String.raw`\b(?:true|false)\b`, cls: 'number' },
    { re: NUMBER, cls: 'number' },
    { re: String.raw`[=\[\],]`, cls: 'punct' },
  ],
  sh: [
    { re: String.raw`#.*`, cls: 'comment' },
    { re: String.raw`\b(?:export|source|cd|then|fi|if|else)\b`, cls: 'number' },
    { re: STRING, cls: 'string' },
    { re: String.raw`\$\{[^}]*\}|\$\w+`, cls: 'key' },
    // The name in `export NAME=value`, which is the thing being set.
    { re: String.raw`\b[A-Z_][A-Z0-9_]*(?==)`, cls: 'key' },
    { re: String.raw`[=|&;]`, cls: 'punct' },
  ],
}

const CLASS: Record<string, string> = {
  comment: 'text-[var(--color-code-comment)]',
  key: 'text-[var(--color-code-key)]',
  string: 'text-[var(--color-code-string)]',
  number: 'text-[var(--color-code-number)]',
  punct: 'text-[var(--color-code-punct)]',
}

/** One compiled regex per language, alternating over its rules in order. */
const COMPILED: Record<Lang, RegExp> = Object.fromEntries(
  (Object.keys(RULES) as Lang[]).map((lang) => [
    lang,
    new RegExp(RULES[lang].map((r) => `(${r.re})`).join('|'), 'gm'),
  ]),
) as Record<Lang, RegExp>

/**
 * Highlight one line.
 *
 * Line at a time is safe here and keeps the gutter honest: none of the three
 * languages has a string that spans lines in the files this shows, so no state
 * carries between them and a line can be rendered on its own.
 */
function highlight(line: string, lang: Lang): ReactNode {
  const re = COMPILED[lang]
  const rules = RULES[lang]
  re.lastIndex = 0

  const out: ReactNode[] = []
  let at = 0
  let m: RegExpExecArray | null
  let key = 0

  while ((m = re.exec(line)) !== null) {
    // A rule that can match empty would spin here; none does, but a zero-width
    // match would be a silent hang, so it is cheaper to step past one.
    if (m[0] === '') {
      re.lastIndex++
      continue
    }
    if (m.index > at) out.push(line.slice(at, m.index))
    // Which alternative fired tells us which rule matched.
    const which = m.slice(1).findIndex((g) => g !== undefined)
    const cls = CLASS[rules[which]?.cls ?? ''] ?? ''
    out.push(
      <span key={key++} className={cls}>
        {m[0]}
      </span>,
    )
    at = m.index + m[0].length
  }
  if (at < line.length) out.push(line.slice(at))
  return out.length > 0 ? out : line
}

/** Roughly how tall the viewer gets before it starts scrolling. */
const MAX_LINES = 22

/** What to call each language in the header. */
const LANG_LABEL: Record<Lang, string> = {
  json: 'JSON',
  jsonc: 'JSON with comments',
  toml: 'TOML',
  sh: 'shell',
}

export function CodeViewer({
  label,
  filename,
  lang,
  value,
}: {
  label: string
  filename: string
  lang: Lang
  value: string
}) {
  const [copied, setCopied] = useState(false)
  const [failed, setFailed] = useState(false)

  // Highlighted once per file rather than once per render. The catalog is a
  // thousand lines, and without this every Copy click — which sets state —
  // would re-tokenise all of them.
  const lines = useMemo(
    () => value.replace(/\n$/, '').split('\n').map((line) => highlight(line, lang)),
    [value, lang],
  )
  const scrolls = lines.length > MAX_LINES
  const gutter = String(lines.length).length

  const copy = async () => {
    if (await copyText(value)) {
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
      return
    }
    setFailed(true)
  }

  return (
    <div className="overflow-hidden rounded-[var(--radius-md3-m)] border border-outline-variant">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-outline-variant bg-surface-high px-3 py-2">
        <span className="font-mono text-xs text-on-surface">{label}</span>
        <span className="text-[11px] text-on-surface-variant">
          {LANG_LABEL[lang]} · {lines.length} lines
        </span>
        <span className="ml-auto flex gap-2">
          <TonalButton onClick={() => void copy()}>{copied ? 'Copied' : 'Copy'}</TonalButton>
          <TonalButton onClick={() => saveAs(filename, value)}>Download</TonalButton>
        </span>
      </div>

      <div
        className={`overflow-auto bg-surface-lowest ${scrolls ? 'max-h-[26rem]' : ''}`}
        tabIndex={0}
        role="region"
        aria-label={`${label}, ${lines.length} lines`}
      >
        {/* A grid rather than two scrolling columns: the gutter has to stay
            put horizontally and move vertically with the code, and one grid
            does both without a second scroll container to keep in step. */}
        <div className="grid min-w-max grid-cols-[auto_1fr] font-mono text-xs leading-5">
          {lines.map((line, i) => (
            <Fragment key={i}>
              <span
                className="sticky left-0 z-10 bg-surface-lowest px-3 text-right tabular-nums text-on-surface-variant/60 select-none"
                style={{ minWidth: `${gutter + 2}ch` }}
                aria-hidden
              >
                {i + 1}
              </span>
              <code className="pr-4 whitespace-pre text-on-surface">{line}</code>
            </Fragment>
          ))}
        </div>
      </div>

      {failed && (
        <ErrorModal
          title="Could not copy"
          onClose={() => setFailed(false)}
          message={
            <>
              The browser will only give a page the clipboard over HTTPS, and this gateway is being
              served over plain HTTP. Nothing was copied — use Download instead, which works either
              way.
            </>
          }
        />
      )}
    </div>
  )
}
