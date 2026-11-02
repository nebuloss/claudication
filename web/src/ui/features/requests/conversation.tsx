import { useState, type ReactNode } from 'react'
import { Chip, Muted, Overline } from '../../primitives'

/**
 * A body read into Anthropic Messages — the gateway's common form, which
 * every API it serves is translated through — drawn as a conversation: the
 * system prompt, then each turn by role, its text, thinking, tool calls and
 * tool results as blocks. One drawing for every API, so a Codex turn and a
 * Claude Code turn read alike.
 *
 * Long parts are folded: an agent's request is mostly its tools, its system
 * prompt and tool output, and the turn someone came to read is the last one.
 */
export function Conversation({
  value,
  start = 0,
  open = false,
}: {
  value: unknown
  /** How many turns came before these, for numbering: a chat's turn shows only what it added. */
  start?: number
  /** Every turn unfolded: a chat's turn, which is only what is new. */
  open?: boolean
}) {
  const v = obj(value)
  if (v === null) return <Muted>Nothing to show.</Muted>

  // An answer: one assistant message.
  if (Array.isArray(v.content) && !Array.isArray(v.messages)) {
    return (
      <div className="space-y-3">
        <Turn role={str(v.role) || 'assistant'} content={v.content} />
        <AnswerFacts v={v} />
      </div>
    )
  }

  const messages = Array.isArray(v.messages) ? v.messages : []
  const tools = Array.isArray(v.tools) ? v.tools : []
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2 text-xs">
        {str(v.model) !== '' && <Chip>{str(v.model)}</Chip>}
        {v.stream === true && <Chip>streamed</Chip>}
        {typeof v.max_tokens === 'number' && <Muted as="span">max {String(v.max_tokens)} tokens</Muted>}
        {obj(v.thinking) !== null && <Muted as="span">thinking {str(obj(v.thinking)?.type)}</Muted>}
      </div>
      {v.system !== undefined && v.system !== null && (
        <Fold title="System" summary={textOf(v.system)}>
          <Blocks content={v.system} />
        </Fold>
      )}
      {tools.length > 0 && (
        <Fold title={`${String(tools.length)} tools`} summary={tools.map((t) => str(obj(t)?.name)).join(', ')}>
          <div className="space-y-2">
            {tools.map((t, i) => (
              <Tool key={i} tool={t} />
            ))}
          </div>
        </Fold>
      )}
      {messages.map((m, i) => {
        const msg = obj(m)
        // Every turn but the last few folded: the latest is what was asked.
        const late = open || i >= messages.length - 2
        return (
          <Turn key={i} role={str(msg?.role)} content={msg?.content} folded={!late} index={start + i + 1} />
        )
      })}
    </div>
  )
}

function AnswerFacts({ v }: { v: Record<string, unknown> }) {
  const usage = obj(v.usage)
  return (
    <div className="flex flex-wrap gap-2 text-xs">
      {str(v.stop_reason) !== '' && <Chip>{str(v.stop_reason)}</Chip>}
      {str(v.model) !== '' && <Muted as="span">{str(v.model)}</Muted>}
      {usage !== null && (
        <Muted as="span">
          {Object.entries(usage)
            .filter(([, n]) => typeof n === 'number' && n > 0)
            .map(([k, n]) => `${k.replace(/_/g, ' ')} ${String(n)}`)
            .join(' · ')}
        </Muted>
      )}
    </div>
  )
}

const ROLE: Record<string, string> = {
  user: 'bg-surface-container',
  assistant: 'bg-secondary-container/40',
}

function Turn({
  role,
  content,
  folded = false,
  index,
}: {
  role: string
  content: unknown
  folded?: boolean
  index?: number
}) {
  const body = (
    <div className={`rounded-md3-m px-3 py-2 ${ROLE[role] ?? 'bg-surface-container'}`}>
      <Blocks content={content} />
    </div>
  )
  const title = `${index !== undefined ? `${String(index)} · ` : ''}${role || 'turn'}`
  if (folded) {
    return (
      <Fold title={title} summary={textOf(content)}>
        {body}
      </Fold>
    )
  }
  return (
    <div className="space-y-1">
      <Overline as="div">{title}</Overline>
      {body}
    </div>
  )
}

function Blocks({ content }: { content: unknown }) {
  if (typeof content === 'string') return <Prose text={content} />
  if (!Array.isArray(content)) return <Raw value={content} />
  return (
    <div className="space-y-2">
      {content.map((b, i) => (
        <Block key={i} block={b} />
      ))}
    </div>
  )
}

function Block({ block }: { block: unknown }) {
  const b = obj(block)
  if (b === null) return <Raw value={block} />
  switch (str(b.type)) {
    case 'text':
      return <Prose text={str(b.text)} />
    case 'thinking':
      return (
        <Fold title="Thinking" summary={str(b.thinking)}>
          <Prose text={str(b.thinking)} muted />
        </Fold>
      )
    case 'redacted_thinking':
      return <Muted>Thinking, redacted by the model&rsquo;s provider.</Muted>
    case 'tool_use':
    case 'server_tool_use':
      return (
        <Fold title={`Calls ${str(b.name)}`} summary={json(b.input)} open>
          <Raw value={b.input} />
        </Fold>
      )
    case 'tool_result': {
      const error = b.is_error === true
      return (
        <Fold title={error ? 'Tool result, an error' : 'Tool result'} summary={textOf(b.content)}>
          <Blocks content={b.content ?? ''} />
        </Fold>
      )
    }
    case 'image':
    case 'document': {
      const source = obj(b.source)
      return (
        <Muted>
          {str(b.type) === 'image' ? 'An image' : 'A document'}
          {source !== null && ` (${str(source.media_type) || str(source.type)})`}
        </Muted>
      )
    }
    default:
      return (
        <Fold title={str(b.type) || 'block'} summary={json(b)}>
          <Raw value={b} />
        </Fold>
      )
  }
}

function Tool({ tool }: { tool: unknown }) {
  const t = obj(tool)
  if (t === null) return <Raw value={tool} />
  return (
    <Fold title={str(t.name) || str(t.type)} summary={str(t.description)}>
      <div className="space-y-2">
        {str(t.description) !== '' && <Prose text={str(t.description)} muted />}
        {t.input_schema !== undefined && <Raw value={t.input_schema} />}
      </div>
    </Fold>
  )
}

/**
 * A part folded behind its title and the start of what it says. Its own
 * state, so opening one turn leaves the rest as they were.
 */
function Fold({
  title,
  summary,
  open = false,
  children,
}: {
  title: string
  summary: string
  open?: boolean
  children: ReactNode
}) {
  const [shown, setShown] = useState(open)
  return (
    <div className="rounded-md3-m border border-outline-variant">
      <button
        type="button"
        onClick={() => setShown(!shown)}
        aria-expanded={shown}
        className="state-layer flex w-full min-w-0 items-baseline gap-2 rounded-md3-m px-3 py-1.5 text-left text-xs"
      >
        <span className="shrink-0 font-medium text-on-surface">{title}</span>
        {!shown && <span className="min-w-0 truncate text-on-surface-variant">{summary.slice(0, 200)}</span>}
      </button>
      {shown && <div className="px-3 pt-1 pb-2">{children}</div>}
    </div>
  )
}

function Prose({ text, muted = false }: { text: string; muted?: boolean }) {
  return (
    <div
      className={`text-sm leading-relaxed break-words whitespace-pre-wrap ${
        muted ? 'text-on-surface-variant' : 'text-on-surface'
      }`}
    >
      {text}
    </div>
  )
}

function Raw({ value }: { value: unknown }) {
  return (
    <pre className="max-h-80 overflow-auto rounded-md3-s bg-surface-lowest p-2 font-mono text-xs whitespace-pre-wrap text-on-surface">
      {typeof value === 'string' ? value : JSON.stringify(value, null, 2)}
    </pre>
  )
}

function obj(v: unknown): Record<string, unknown> | null {
  return typeof v === 'object' && v !== null && !Array.isArray(v) ? (v as Record<string, unknown>) : null
}

function str(v: unknown): string {
  return typeof v === 'string' ? v : ''
}

function json(v: unknown): string {
  try {
    return JSON.stringify(v) ?? ''
  } catch {
    return ''
  }
}

/** What a part says, in one line, for its folded summary. */
function textOf(content: unknown): string {
  if (typeof content === 'string') return content.replace(/\s+/g, ' ')
  if (!Array.isArray(content)) return json(content)
  return content
    .map((b) => {
      const o = obj(b)
      if (o === null) return ''
      if (typeof o.text === 'string') return o.text
      if (typeof o.thinking === 'string') return o.thinking
      if (str(o.type) === 'tool_use') return `${str(o.name)}(${json(o.input)})`
      if (str(o.type) === 'tool_result') return textOf(o.content)
      return `[${str(o.type)}]`
    })
    .join(' ')
    .replace(/\s+/g, ' ')
}
