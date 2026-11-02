import { useState } from 'react'
import { api, type CaptureBody, type CaptureLeg, type CaptureMessage } from '../../../api/client'
import { useLoader } from '../../hooks'
import { bytesLabel, millis } from '../../format'
import {
  Banner,
  ButtonLink,
  Chip,
  CodeViewer,
  ErrorState,
  Loading,
  Muted,
  Overline,
  Stack,
  SubNav,
  type Lang,
} from '../../primitives'
import { Conversation } from './conversation'

/** The APIs a body can be in, as people call them. */
const DIALECT: Record<string, string> = {
  anthropic: 'Anthropic Messages',
  openai: 'OpenAI Responses',
  gemini: 'Gemini',
}

const dialectTitle = (d: string) => DIALECT[d] ?? d

/**
 * What was recorded of one request (internal/service/capture): the client's
 * leg — its request as the gateway read it and the answer it was sent — and
 * each attempt the relay made upstream, every body in its own API.
 *
 * Each body can be read two ways: as the gateway's common form — Anthropic
 * Messages, which every API is translated through — so a Codex turn and a
 * Claude Code turn read alike, or raw, the bytes as they passed. Raw is the
 * truth; the conversation is the gateway's reading of it, which is exactly
 * what to look at when a translation is the suspect.
 */
export function RecordedContent({ id }: { id: number }) {
  const { data, error, loading, reload } = useLoader((signal) => api.captures.get(id, signal), { deps: [id] })
  const [leg, setLeg] = useState('client')

  if (loading && data === null) return <Loading>Reading the recording…</Loading>
  if (error !== '' || data === null) {
    return <ErrorState message={error || 'Could not read the recording.'} onRetry={() => void reload()} busy={loading} />
  }

  const legs: { id: string; label: string; leg: CaptureLeg }[] = [
    { id: 'client', label: 'Client', leg: data.client },
    ...data.upstream.map((l, i) => ({
      id: `up${String(i)}`,
      label: data.upstream.length > 1 ? `Upstream, attempt ${String(i + 1)}` : 'Upstream',
      leg: l,
    })),
  ]
  const shown = legs.find((l) => l.id === leg) ?? legs[0]!

  return (
    <Stack gap={3}>
      <SubNav
        label="Which side"
        options={legs.map(({ id, label }) => ({ id, label }))}
        value={shown.id}
        onChange={setLeg}
        aside={
          <ButtonLink href={api.captures.harHref(id)} variant="text" size="sm" icon="download">
            HAR
          </ButtonLink>
        }
      />
      <LegView leg={shown.leg} side={shown.id === 'client' ? 'client' : 'upstream'} />
    </Stack>
  )
}

function LegView({ leg, side }: { leg: CaptureLeg; side: 'client' | 'upstream' }) {
  return (
    <Stack gap={3}>
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <Chip>{dialectTitle(leg.dialect)}</Chip>
        {leg.account !== undefined && leg.account !== '' && <Muted as="span">on {leg.account}</Muted>}
        {leg.ended !== undefined && (
          <Muted as="span">
            · {millis(new Date(leg.ended).getTime() - new Date(leg.started).getTime())}
          </Muted>
        )}
      </div>
      {leg.error !== undefined && <Banner tone="warn">No answer: {leg.error}</Banner>}
      <MessageView
        title={side === 'client' ? 'What the client sent' : 'What the gateway sent'}
        message={leg.request}
        line={`${leg.request.method ?? ''} ${leg.request.url ?? ''}`}
        file={side === 'client' ? 'client-request' : 'upstream-request'}
      />
      {leg.response === null ? (
        leg.error === undefined && <Muted>No answer was recorded.</Muted>
      ) : (
        <MessageView
          title={side === 'client' ? 'What the client was answered' : 'What the upstream answered'}
          message={leg.response}
          line={`${String(leg.response.status ?? '')}`}
          file={side === 'client' ? 'client-response' : 'upstream-response'}
        />
      )}
    </Stack>
  )
}

type View = 'conversation' | 'raw'

function MessageView({
  title,
  message: m,
  line,
  file,
}: {
  title: string
  message: CaptureMessage
  line: string
  file: string
}) {
  // The conversation where the gateway could read one; raw otherwise.
  const readable = m.messages !== undefined && m.messages !== null
  const [view, setView] = useState<View>(readable ? 'conversation' : 'raw')
  const body = presented(m.body)
  return (
    <section className="space-y-2">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <Overline as="span">{title}</Overline>
        <code className="min-w-0 break-all text-xs text-on-surface-variant">{line}</code>
        <span className="flex-grow" />
        <Muted as="span">
          {bytesLabel(m.body.size)}
          {m.body.parts > 1 && ` in ${String(m.body.parts)} parts over ${millis(m.body.last_ms ?? 0)}`}
        </Muted>
      </div>
      <Headers header={m.header} />
      {m.body.truncated === true && (
        <Banner tone="warn">Cut at the gateway&rsquo;s limit per body: what is past it was counted and not kept.</Banner>
      )}
      {m.body.size > 0 && (
        <>
          <SubNav
            label="How to read it"
            options={[
              { id: 'conversation' as View, label: 'Conversation' },
              { id: 'raw' as View, label: 'Raw' },
            ]}
            value={view}
            onChange={setView}
          />
          {view === 'conversation' ? (
            readable ? (
              <Conversation value={m.messages} />
            ) : (
              <Muted>
                The gateway could not read this body as a conversation
                {m.messages_error !== undefined && m.messages_error !== '' ? `: ${m.messages_error}` : ''}. Raw has
                it as it passed.
              </Muted>
            )
          ) : (
            <CodeViewer label={body.label} filename={`${file}.${body.ext}`} lang={body.lang} value={body.value} />
          )}
        </>
      )}
    </section>
  )
}

/** Headers, folded: read when they matter, which is rarely. */
function Headers({ header }: { header: [string, string][] }) {
  if (header.length === 0) return null
  return (
    <details className="rounded-md3-m bg-surface-container px-3 py-2 text-xs">
      <summary className="cursor-pointer text-on-surface-variant select-none">
        {header.length} {header.length === 1 ? 'header' : 'headers'}
      </summary>
      <div className="mt-2 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 font-mono">
        {header.map(([k, v], i) => (
          <div key={i} className="contents">
            <span className="text-on-surface-variant">{k}</span>
            <span className="min-w-0 break-all text-on-surface">{v}</span>
          </div>
        ))}
      </div>
    </details>
  )
}

/** A body as the viewer shows it: JSON indented, a stream as it came. */
function presented(b: CaptureBody): { value: string; lang: Lang; label: string; ext: string } {
  if (b.base64 === true) return { value: b.text, lang: 'text', label: 'base64', ext: 'b64' }
  const t = b.text
  try {
    return { value: JSON.stringify(JSON.parse(t), null, 2), lang: 'json', label: 'body', ext: 'json' }
  } catch {
    // Not one JSON document: a stream, or a cut one.
  }
  if (/^(?:event|data|id|retry|:)/m.test(t.slice(0, 200))) return { value: t, lang: 'sse', label: 'stream', ext: 'txt' }
  return { value: t, lang: 'text', label: 'body', ext: 'txt' }
}
