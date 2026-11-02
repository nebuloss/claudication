import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, type ChatDetail, type ChatTurn, type RequestRow } from '../../../api/client'
import { READS } from '../../../api/live'
import { useLoader } from '../../hooks'
import { between, compact, dateTime, millis, timeOfDay } from '../../format'
import { chatHref, clientHref, type ChatPageId } from '../../route'
import { nameItem } from '../../names'
import {
  AppLink,
  Button,
  Card,
  Chip,
  ErrorState,
  Loading,
  Muted,
  Note,
  saveAs,
  Stack,
  Stat,
  StatGrid,
  VirtualList,
} from '../../primitives'
import ChatContextDetail from './context'
import { Conversation } from '../requests/conversation'
import { RequestDetail, useShownRequest } from '../requests/detail'
import { RequestRecord } from '../requests/record'
import { StatusChip } from '../requests/row'

/**
 * One chat of a client's, as an item with pages of its own (route.ts,
 * CHAT_PAGES):
 *
 *   /clients/<id>/chats/<chat>             what it is: its figures, the
 *                                           models it ran on by the names it
 *                                           asked by, and its context curve
 *   …/requests                             every request, a line each
 *   …/transcript                           the recorded ones, as what each
 *                                           added to the conversation
 *
 * The two lists are one scrolling box each that draws only the rows in view
 * (VirtualList) filling the window, opened at the latest or at ?at=<row>,
 * which is how a request's dialog sends someone to its place in its chat. A
 * request opens its dialog on a click, at ?request=<row> as in any list.
 */
export function ChatPage({ client, id, page }: { client: string; id: string; page: ChatPageId | '' }) {
  const chat = useLoader((signal) => api.traffic.chat(id, signal), { deps: [id], on: READS.chat })
  const rows = chat.data?.requests ?? []

  // Named for the header, the trail and the rail, which are drawn above it.
  const title = rows.find((r) => r.chat_title !== undefined && r.chat_title !== '')?.chat_title ?? ''
  useEffect(() => {
    if (title !== '') nameItem('chats', id, title)
  }, [id, title])

  if (chat.loading && chat.data === null) return <Loading>Reading the chat…</Loading>
  if (chat.data === null) {
    return (
      <ErrorState
        message={chat.error || 'Could not read the chat.'}
        onRetry={() => void chat.reload()}
        busy={chat.loading}
      />
    )
  }
  const thread = threadOf(chat.data)
  if (page === 'requests') return <ChatRequests id={id} rows={rows} thread={thread} />
  if (page === 'transcript') return <ChatTranscript client={client} id={id} rows={rows} thread={thread} />
  return <ChatOverview client={client} id={id} detail={chat.data} title={title} />
}

/** The request asked for by ?at=, or 0. */
function focusOf(): number {
  return Number(new URLSearchParams(window.location.search).get('at') ?? '') || 0
}

/** A list's rows as a file, under it. */
function Download({ count, noun, onSave }: { count: number; noun: string; onSave: () => void }) {
  return (
    <div className="flex items-center gap-3 pt-3">
      <Muted as="span" size="xs">
        {count} {noun}
      </Muted>
      <span className="flex-grow" />
      <Button variant="text" size="sm" icon="download" onClick={onSave}>
        Download JSON
      </Button>
    </div>
  )
}

/**
 * Where each request stands against the chat's main thread — the models that
 * carried the conversation (ChatContext.main_models): beside it (a title, a
 * quick check, on a model of its own), or on it, and then whether the main
 * thread had just switched to its model. A line on the main thread names no
 * model, since the chat's overview does; one beside it does, and a switch is
 * a divider.
 */
type Place = { side: boolean; switchedTo?: string }

function threadOf(detail: ChatDetail): Map<number, Place> {
  const main = new Set(detail.context.main_models ?? (detail.context.model !== '' ? [detail.context.model] : []))
  const out = new Map<number, Place>()
  let last = ''
  for (const r of detail.requests) {
    const model = r.model ?? ''
    if (model === '' || !main.has(model)) {
      out.set(r.id, { side: model !== '' && main.size > 0 })
      continue
    }
    out.set(r.id, { side: false, switchedTo: last !== '' && model !== last ? model : undefined })
    last = model
  }
  return out
}

function tokensOf(r: RequestRow): number {
  return r.input_tokens + r.output_tokens + r.cache_tokens
}

/** One model a chat ran on, by the name it was asked by, and when. */
type ModelUse = {
  asked: string
  model: string
  provider: string
  requests: number
  tokens: number
  first: string
  last: string
  main: boolean
}

/**
 * What the chat is: its figures, the models it ran on — each by the name the
 * client asked for, which is the client's own binding, and the real model
 * that served — and how its context grew. All of it from the chat's rows.
 */
function ChatOverview({
  client,
  id,
  detail,
  title,
}: {
  client: string
  id: string
  detail: ChatDetail
  title: string
}) {
  const rows = detail.requests
  const recorded = rows.filter((r) => (r.capture ?? 0) > 0).length
  const failed = rows.filter((r) => r.status === 0 || r.status >= 400).length
  const sum = (f: (r: RequestRow) => number) => rows.reduce((n, r) => n + f(r), 0)
  const first = rows[0]?.at ?? ''
  const last = rows[rows.length - 1]?.at ?? ''
  const ctx = detail.context

  const models = useMemo(() => {
    const main = new Set(ctx.main_models ?? [ctx.model])
    const by = new Map<string, ModelUse>()
    for (const r of rows) {
      const model = r.model ?? ''
      const asked = r.virtual_model ?? ''
      const key = `${asked}\u0000${model}`
      const u = by.get(key) ?? {
        asked,
        model,
        provider: r.provider ?? '',
        requests: 0,
        tokens: 0,
        first: r.at,
        last: r.at,
        main: main.has(model),
      }
      u.requests++
      u.tokens += tokensOf(r)
      u.last = r.at
      by.set(key, u)
    }
    // The main thread first, in the order it ran on them; then the rest.
    return [...by.values()].sort((a, b) =>
      a.main !== b.main ? (a.main ? -1 : 1) : a.main ? a.first.localeCompare(b.first) : b.tokens - a.tokens,
    )
  }, [rows, ctx])

  return (
    <Stack>
      <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <Muted>
          {title === '' && 'Unnamed · '}
          <code className="break-all">{id}</code>
        </Muted>
        <AppLink href={`${clientHref(client)}/chats`}>All chats</AppLink>
      </div>

      <StatGrid>
        <Stat
          label="Requests"
          value={<AppLink href={chatHref(client, id, 'requests')}>{rows.length}</AppLink>}
          hint={failed > 0 ? `${String(failed)} failed` : 'none failed'}
          tone={failed > 0 ? 'warn' : undefined}
        />
        <Stat
          label="Tokens"
          value={compact(sum(tokensOf))}
          hint={`${compact(sum((r) => r.input_tokens))} in · ${compact(sum((r) => r.output_tokens))} out · ${compact(sum((r) => r.cache_tokens))} cached`}
        />
        <Stat
          label="Context"
          value={ctx.model !== '' ? compact(ctx.now) : '—'}
          hint={
            ctx.model !== ''
              ? `peak ${compact(ctx.peak)} · ${ctx.compactions > 0 ? `${String(ctx.compactions)}× compacted` : 'never compacted'}`
              : 'no request succeeded'
          }
        />
        <Stat
          label="Span"
          value={first !== '' ? between(first, last) : '—'}
          hint={first !== '' ? `${dateTime(first)} → ${timeOfDay(last)}` : undefined}
        />
      </StatGrid>

      <Card title="Models">
        {models.length === 0 ? (
          <Muted>No request named a model.</Muted>
        ) : (
          <div className="divide-y divide-outline-variant">
            {models.map((m) => (
              <div key={`${m.asked}/${m.model}`} className="flex flex-wrap items-baseline gap-x-3 gap-y-1 py-2 text-sm">
                {/* The name the client asked by, which its binding turned into
                    the model that served; a direct name is the model itself. */}
                {m.asked !== '' && m.asked !== m.model && (
                  <>
                    <code className="text-on-surface">{m.asked}</code>
                    <Muted as="span">→</Muted>
                  </>
                )}
                <code className="text-on-surface">{m.model || '—'}</code>
                {m.provider !== '' && <Muted as="span">{m.provider}</Muted>}
                {!m.main && <Chip>beside the chat</Chip>}
                {/* When it was in use: two periods read as the chat switching. */}
                <Muted as="span">
                  {timeOfDay(m.first)}
                  {m.last !== m.first && `–${timeOfDay(m.last)}`}
                </Muted>
                <span className="flex-grow" />
                <span className="tabular-nums text-on-surface">{m.requests}</span>
                <Muted as="span">{m.requests === 1 ? 'request' : 'requests'}</Muted>
                <span className="tabular-nums text-on-surface">{compact(m.tokens)}</span>
                <Muted as="span">tokens</Muted>
              </div>
            ))}
          </div>
        )}
      </Card>

      <Card title="Context">
        <ChatContextDetail id={id} colour="var(--color-primary)" />
      </Card>

      {recorded > 0 ? (
        <Muted>
          {recorded} of its requests were recorded:{' '}
          <AppLink href={chatHref(client, id, 'transcript')}>read the transcript</AppLink>.
        </Muted>
      ) : (
        <Note>
          None of its requests was recorded, so what they said is not here: switch on <strong>Record content</strong>{' '}
          on the <AppLink href={clientHref(client)}>client&rsquo;s overview</AppLink> and its next turns read as a
          transcript.
        </Note>
      )}
    </Stack>
  )
}

const rowKey = (row: RequestRow) => String(row.id)

/** Every request of the chat, a line each, oldest first. */
function ChatRequests({ id, rows, thread }: { id: string; rows: RequestRow[]; thread: Map<number, Place> }) {
  const { shown, open, close } = useShownRequest()
  const focus = focusOf()
  return (
    <>
      <VirtualList
        items={rows}
        keyOf={rowKey}
        label="The chat's requests"
        estimate={52}
        fill
        focus={focus === 0 ? undefined : rows.findIndex((r) => r.id === focus)}
        render={(row) => (
          <Turn
            row={row}
            place={thread.get(row.id)}
            focused={row.id === focus || row.id === shown?.row.id}
            onOpen={() => open(new RequestRecord(row))}
          />
        )}
      />
      <Download
        count={rows.length}
        noun={rows.length === 1 ? 'request' : 'requests'}
        onSave={() => saveAs(`chat-${id}-requests.json`, JSON.stringify(rows, null, 2))}
      />
      {shown !== null && <RequestDetail record={shown} onClose={close} inChat />}
    </>
  )
}

/**
 * The recorded requests of the chat, each as what it added to the
 * conversation and its answer. Only the turns of the rows drawn are read from
 * their recordings, and each is kept once read: a recording never changes.
 */
function ChatTranscript({
  client,
  id,
  rows,
  thread,
}: {
  client: string
  id: string
  rows: RequestRow[]
  thread: Map<number, Place>
}) {
  const { shown, open, close } = useShownRequest()
  const focus = focusOf()
  const recorded = useMemo(() => rows.filter((r) => (r.capture ?? 0) > 0), [rows])
  // The whole transcript, read when it is downloaded: the page reads only
  // what is in view.
  const save = () =>
    void api.traffic.chatTranscript(id, 0, 0).then((t) => saveAs(`chat-${id}-transcript.json`, JSON.stringify(t, null, 2)))

  const [range, setRange] = useState<[number, number]>([0, 0])
  const onRange = useCallback((start: number, end: number) => setRange([start, end]), [])
  const inWindow = recorded.slice(range[0], range[1])
  const from = inWindow[0]?.id ?? 0
  const to = inWindow[inWindow.length - 1]?.id ?? 0
  const read = useLoader(
    (signal) => (from !== 0 ? api.traffic.chatTranscript(id, from, to, signal) : Promise.resolve({ id, turns: [] })),
    { deps: [id, from, to], on: READS.chat },
  )
  const kept = useRef(new Map<number, ChatTurn>())
  for (const t of read.data?.turns ?? []) kept.current.set(t.event, t)

  if (recorded.length === 0) {
    return (
      <Note>
        None of this chat&rsquo;s requests was recorded: switch on <strong>Record content</strong> on the{' '}
        <AppLink href={clientHref(client)}>client&rsquo;s overview</AppLink>, and its next turns read here. A recording
        is held in memory, and kept on disk too where the gateway is set to keep them (capture.retention-days).
      </Note>
    )
  }
  return (
    <>
      <VirtualList
        items={recorded}
        keyOf={rowKey}
        label="The chat's transcript"
        estimate={200}
        fill
        focus={focus === 0 ? undefined : recorded.findIndex((r) => r.id === focus)}
        onRange={onRange}
        render={(row) => (
          <Turn
            row={row}
            place={thread.get(row.id)}
            turn={kept.current.get(row.id)}
            focused={row.id === focus || row.id === shown?.row.id}
            onOpen={() => open(new RequestRecord(row))}
          />
        )}
      />
      <Download count={recorded.length} noun="recorded" onSave={save} />
      {shown !== null && <RequestDetail record={shown} onClose={close} inChat />}
    </>
  )
}

function Turn({
  row,
  place,
  turn,
  focused,
  onOpen,
}: {
  row: RequestRow
  place?: Place
  turn?: ChatTurn
  focused: boolean
  onOpen: () => void
}) {
  const record = new RequestRecord(row)
  return (
    <div>
      {place?.switchedTo !== undefined && (
        <div className="flex items-center gap-3 pb-2 text-xs text-tertiary">
          <span className="h-px flex-1 bg-tertiary/50" />
          <span>
            switched to <code>{place.switchedTo}</code>
          </span>
          <span className="h-px flex-1 bg-tertiary/50" />
        </div>
      )}
    <section className={`rounded-md3-l bg-surface-container ${focused ? 'ring-2 ring-inset ring-primary' : ''}`}>
      {/* The request's line opens it, as a row of the request log does; the
          turn's own folds under it are theirs. */}
      <button
        type="button"
        onClick={onOpen}
        className="state-layer flex w-full flex-wrap items-center gap-x-3 gap-y-1 rounded-md3-l p-3 text-left text-sm"
      >
        <span className="tabular-nums text-on-surface-variant">{timeOfDay(row.at, 'second')}</span>
        <StatusChip record={record} />
        <span className={place?.side === true ? 'text-on-surface-variant' : 'text-on-surface'}>
          {record.modelLabel}
        </span>
        <Muted as="span">
          {record.tokensShort} · {millis(row.duration_ms)}
        </Muted>
      </button>
      {turn !== undefined && (
        <div className="space-y-3 px-3 pb-3">
          {turn.unread !== undefined && turn.unread !== '' ? (
            <Muted>Recorded, but not readable as a conversation: {turn.unread}. Details has it raw.</Muted>
          ) : (
            <>
              {turn.restarted === true && (
                <Muted size="xs">
                  It carried fewer messages than the turn before it — the client compacted or started over, or
                  this is a side request — so all of them are shown.
                </Muted>
              )}
              {turn.added.length > 0 && <Conversation value={{ messages: turn.added }} start={turn.offset} open />}
              {turn.answer !== undefined && turn.answer !== null && <Conversation value={turn.answer} />}
            </>
          )}
        </div>
      )}
    </section>
    </div>
  )
}
