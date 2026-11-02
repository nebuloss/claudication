import { useEffect, useMemo, useState } from 'react'
import { api, type ChatTurn, type RequestRow } from '../../../api/client'
import { READS } from '../../../api/live'
import { useLoader } from '../../hooks'
import { millis, timeOfDay } from '../../format'
import { clientHref } from '../../route'
import { AppLink, Button, Card, ErrorState, Loading, Muted, Note, Stack } from '../../primitives'
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
 */
export function ChatView({ client, id }: { client: string; id: string }) {
  const focus = Number(new URLSearchParams(window.location.search).get('request') ?? '') || 0
  const chat = useLoader((signal) => api.traffic.chat(id, signal), { deps: [id], on: READS.chat })
  const transcript = useLoader((signal) => api.traffic.chatTranscript(id, signal), { deps: [id], on: READS.chat })
  const [shown, setShown] = useState<RequestRecord | null>(null)

  const turns = useMemo(() => new Map((transcript.data?.turns ?? []).map((t) => [t.event, t])), [transcript.data])
  const rows = chat.data?.requests ?? []

  // Once, when the rows first arrive: later refreshes leave the scroll where
  // the operator has taken it.
  const arrived = rows.length > 0
  useEffect(() => {
    if (focus === 0 || !arrived) return
    document.getElementById(`request-${String(focus)}`)?.scrollIntoView({ block: 'start', behavior: 'smooth' })
  }, [focus, arrived])

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
        {rows.map((row) => (
          <Turn
            key={row.id}
            row={row}
            turn={turns.get(row.id)}
            focused={row.id === focus}
            onOpen={() => setShown(new RequestRecord(row))}
          />
        ))}
      </div>

      {shown !== null && <RequestDetail record={shown} onClose={() => setShown(null)} inChat />}
    </Stack>
  )
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
      id={`request-${String(row.id)}`}
      // Clear of the app bar when scrolled to.
      className={`scroll-mt-24 rounded-md3-l bg-surface-container p-3 ${focused ? 'ring-2 ring-primary' : ''}`}
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
