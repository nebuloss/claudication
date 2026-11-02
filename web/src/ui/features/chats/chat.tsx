import { useCallback, useRef, useState } from 'react'
import { api, type ChatTurn, type RequestRow } from '../../../api/client'
import { READS } from '../../../api/live'
import { useLoader } from '../../hooks'
import { millis, timeOfDay } from '../../format'
import { clientHref } from '../../route'
import { AppLink, Button, Card, ErrorState, Loading, Muted, Note, Stack, Switch, VirtualList } from '../../primitives'
import ChatContextDetail from './context'
import { Conversation } from '../requests/conversation'
import { RequestDetail } from '../requests/detail'
import { RequestRecord } from '../requests/record'
import { StatusChip } from '../requests/row'

/**
 * One chat of a client's, read forwards: its context over its turns, then
 * each of its requests, oldest first. A request recorded while its client
 * was being recorded (internal/service/capture) shows what it added to the
 * conversation — the messages new since the turn before, and its answer —
 * so a recorded chat reads as a transcript; one that was not is a line with
 * its outcome. Every request opens its full record, as in the request log.
 *
 * ?request=<row> opens on one of them, which is how the request log's
 * dialog sends someone to the request's place in its chat.
 *
 * A chat runs to hundreds of requests, so they are one scrolling list that
 * draws only the rows in view (VirtualList), opened at the latest or at the
 * request asked for, and only the drawn rows' turns are read from their
 * recordings. Turns read once are kept while the page is open: a recording
 * never changes. The Transcript switch above it shows or hides what each
 * turn said, leaving the requests as one line each to scan.
 */
export function ChatView({ client, id }: { client: string; id: string }) {
  const focus = Number(new URLSearchParams(window.location.search).get('request') ?? '') || 0
  const chat = useLoader((signal) => api.traffic.chat(id, signal), { deps: [id], on: READS.chat })
  const [shown, setShown] = useState<RequestRecord | null>(null)
  const rows = chat.data?.requests ?? []
  const [transcript, setTranscript] = useState(readTranscriptSwitch)
  const switchTranscript = (on: boolean) => {
    setTranscript(on)
    try {
      localStorage.setItem(TRANSCRIPT_KEY, on ? '1' : '0')
    } catch {
      // Remembered where the browser allows it; the switch works either way.
    }
  }

  // The window drawn, as the rows' ids at its two ends; its turns are read
  // for it, and only when it holds a recorded request.
  const [range, setRange] = useState<[number, number]>([0, 0])
  const onRange = useCallback((start: number, end: number) => setRange([start, end]), [])
  const inWindow = rows.slice(range[0], range[1])
  const from = inWindow[0]?.id ?? 0
  const to = inWindow[inWindow.length - 1]?.id ?? 0
  const wanted = transcript && inWindow.some((r) => (r.capture ?? 0) > 0)
  const read = useLoader(
    (signal) => (wanted ? api.traffic.chatTranscript(id, from, to, signal) : Promise.resolve({ id, turns: [] })),
    { deps: [id, from, to, wanted], on: READS.chat },
  )
  const kept = useRef(new Map<number, ChatTurn>())
  for (const t of read.data?.turns ?? []) kept.current.set(t.event, t)
  const turns = kept.current

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

  const title = rows.find((r) => r.chat_title !== undefined && r.chat_title !== '')?.chat_title ?? ''
  const recorded = rows.filter((r) => (r.capture ?? 0) > 0).length

  return (
    <Stack>
      <Card
        title={title || 'Unnamed chat'}
        aside={<AppLink href={`${clientHref(client)}/chats`}>All chats</AppLink>}
      >
        <Stack gap={3}>
          <Muted>
            <code className="break-all">{id}</code> · {rows.length} {rows.length === 1 ? 'request' : 'requests'}
            {recorded > 0 && `, ${String(recorded)} recorded`}
          </Muted>
          <ChatContextDetail id={id} colour="var(--color-primary)" />
          {recorded === 0 && (
            <Note>
              None of these requests was recorded, so what they said is not here: switch on{' '}
              <strong>Record content</strong> on the{' '}
              <AppLink href={clientHref(client)}>client&rsquo;s overview</AppLink> and its next turns read as a
              transcript.
            </Note>
          )}
        </Stack>
      </Card>

      <div className="space-y-2">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          <Switch
            checked={transcript}
            onChange={switchTranscript}
            label="Transcript"
            disabled={recorded === 0}
          />
          <Muted as="span" size="xs">
            {recorded === 0
              ? 'Nothing recorded to show.'
              : transcript
                ? 'What each recorded request added, and its answer.'
                : 'One line per request.'}
          </Muted>
        </div>
        {rows.length > 0 && (
          <VirtualList
            items={rows}
            keyOf={rowKey}
            label="The chat's requests"
            estimate={transcript && recorded > 0 ? 160 : 52}
            focus={focus === 0 ? undefined : rows.findIndex((r) => r.id === focus)}
            onRange={onRange}
            render={(row) => (
              <Turn
                row={row}
                turn={transcript ? turns.get(row.id) : undefined}
                focused={row.id === focus}
                onOpen={() => setShown(new RequestRecord(row))}
              />
            )}
          />
        )}
      </div>

      {shown !== null && <RequestDetail record={shown} onClose={() => setShown(null)} inChat />}
    </Stack>
  )
}

const rowKey = (row: RequestRow) => String(row.id)

/** Where the Transcript switch is remembered, per browser. */
const TRANSCRIPT_KEY = 'claudication.chat.transcript'

function readTranscriptSwitch(): boolean {
  try {
    return localStorage.getItem(TRANSCRIPT_KEY) !== '0'
  } catch {
    return true
  }
}

function Turn({
  row,
  turn,
  focused,
  onOpen,
}: {
  row: RequestRow
  turn: ChatTurn | undefined
  focused: boolean
  onOpen: () => void
}) {
  const record = new RequestRecord(row)
  return (
    <section
      className={`rounded-md3-l bg-surface-container p-3 ${focused ? 'ring-2 ring-inset ring-primary' : ''}`}
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
        <span className="tabular-nums text-on-surface-variant">{timeOfDay(row.at, 'second')}</span>
        <StatusChip record={record} />
        <span className="text-on-surface">{record.modelLabel}</span>
        <Muted as="span">
          {record.tokensShort} · {millis(row.duration_ms)}
        </Muted>
        <span className="flex-grow" />
        <Button variant="text" size="sm" onClick={onOpen}>
          Details
        </Button>
      </div>
      {turn !== undefined && (
        <div className="mt-3 space-y-3">
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
  )
}
